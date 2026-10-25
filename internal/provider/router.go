package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"parlex/internal/catalog"
)

type Router struct {
	mu           sync.RWMutex
	cat          *catalog.Catalog
	providers    map[string]Provider
	breakers     map[string]*CircuitBreaker
	maxSessions  int
	currSessions int
}

func NewRouter(cat *catalog.Catalog, maxSessions ...int) *Router {
	limit := 200
	if len(maxSessions) > 0 && maxSessions[0] > 0 {
		limit = maxSessions[0]
	}
	return &Router{
		cat:         cat,
		providers:   make(map[string]Provider),
		breakers:    make(map[string]*CircuitBreaker),
		maxSessions: limit,
	}
}

func (r *Router) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
	r.breakers[p.Name()] = NewCircuitBreaker(5, 30*time.Second, 20*time.Second)
}

func (r *Router) ResolveAndOpen(ctx context.Context, d Direction) (Session, string, error) {
	providerName, err := r.cat.ResolveProvider(d.Src, d.Dst)
	if err != nil {
		return nil, "", fmt.Errorf("resolving provider for %s: %w", d, err)
	}

	r.mu.Lock()
	if r.maxSessions > 0 && r.currSessions >= r.maxSessions {
		r.mu.Unlock()
		return nil, providerName, ErrProviderUnavailable
	}
	r.currSessions++
	r.mu.Unlock()

	decrementOnce := sync.Once{}
	release := func() {
		decrementOnce.Do(func() {
			r.mu.Lock()
			r.currSessions--
			r.mu.Unlock()
		})
	}

	r.mu.RLock()
	p, ok := r.providers[providerName]
	cb, hasBreaker := r.breakers[providerName]
	r.mu.RUnlock()

	if !ok {
		// Fallback to groq or mock if resolved provider is not registered
		r.mu.RLock()
		if groqP, groqOk := r.providers["groq"]; groqOk {
			p = groqP
			cb = r.breakers["groq"]
			providerName = "groq"
			ok = true
		} else if mockP, mockOk := r.providers["mock"]; mockOk {
			p = mockP
			cb = r.breakers["mock"]
			providerName = "mock"
			ok = true
		}
		r.mu.RUnlock()
		if !ok {
			release()
			return nil, "", fmt.Errorf("provider %q not registered: %w", providerName, ErrProviderUnavailable)
		}
	}

	if hasBreaker && cb != nil && !cb.Allow() {
		release()
		return nil, providerName, ErrCircuitOpen
	}

	sess, err := p.Open(ctx, d)
	if err != nil {
		release()
		if hasBreaker && cb != nil {
			cb.RecordFailure()
		}
		return nil, providerName, err
	}

	if hasBreaker && cb != nil {
		cb.RecordSuccess()
	}

	return &trackedSession{Session: sess, release: release}, providerName, nil
}

func (r *Router) GetBreakerState(name string) BreakerState {
	r.mu.RLock()
	cb, ok := r.breakers[name]
	r.mu.RUnlock()
	if !ok || cb == nil {
		return StateClosed
	}
	return cb.State()
}

type trackedSession struct {
	Session
	release func()
}

func (s *trackedSession) Abort() {
	s.release()
	s.Session.Abort()
}

func (s *trackedSession) Events() <-chan Event {
	orig := s.Session.Events()
	ch := make(chan Event, 64)
	go func() {
		defer s.release()
		defer close(ch)
		for ev := range orig {
			ch <- ev
		}
	}()
	return ch
}
