package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"parlex/internal/catalog"
)

type Router struct {
	mu        sync.RWMutex
	cat       *catalog.Catalog
	providers map[string]Provider
	breakers  map[string]*CircuitBreaker
}

func NewRouter(cat *catalog.Catalog) *Router {
	return &Router{
		cat:       cat,
		providers: make(map[string]Provider),
		breakers:  make(map[string]*CircuitBreaker),
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

	r.mu.RLock()
	p, ok := r.providers[providerName]
	cb, hasBreaker := r.breakers[providerName]
	r.mu.RUnlock()

	if !ok {
		// Fallback to mock or whatever is registered
		r.mu.RLock()
		if mockP, mockOk := r.providers["mock"]; mockOk {
			p = mockP
			cb = r.breakers["mock"]
			providerName = "mock"
			ok = true
		}
		r.mu.RUnlock()
		if !ok {
			return nil, "", fmt.Errorf("provider %q not registered: %w", providerName, ErrProviderUnavailable)
		}
	}

	if hasBreaker && cb != nil && !cb.Allow() {
		return nil, providerName, ErrCircuitOpen
	}

	sess, err := p.Open(ctx, d)
	if err != nil {
		if hasBreaker && cb != nil {
			cb.RecordFailure()
		}
		return nil, providerName, err
	}

	if hasBreaker && cb != nil {
		cb.RecordSuccess()
	}

	return sess, providerName, nil
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
