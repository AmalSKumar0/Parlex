package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RevocationRegistry tracks revoked device IDs in memory and notifies subscribers.
type RevocationRegistry struct {
	mu           sync.RWMutex
	revoked      map[uuid.UUID]bool
	listeners    map[uuid.UUID][]chan struct{}
	listenersMu  sync.Mutex
	revokedFetch func(ctx context.Context) ([]uuid.UUID, error)
	logger       *slog.Logger
}

// NewRevocationRegistry creates a revocation registry.
func NewRevocationRegistry(logger *slog.Logger, fetcher func(ctx context.Context) ([]uuid.UUID, error)) *RevocationRegistry {
	return &RevocationRegistry{
		revoked:      make(map[uuid.UUID]bool),
		listeners:    make(map[uuid.UUID][]chan struct{}),
		revokedFetch: fetcher,
		logger:       logger,
	}
}

// IsRevoked checks if a device ID is marked revoked.
func (r *RevocationRegistry) IsRevoked(id uuid.UUID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.revoked[id]
}

// MarkRevoked marks a device as revoked and fires all listeners.
func (r *RevocationRegistry) MarkRevoked(id uuid.UUID) {
	r.mu.Lock()
	r.revoked[id] = true
	r.mu.Unlock()

	r.notifyListeners(id)
}

// Subscribe returns a channel that is closed if/when the device is revoked.
func (r *RevocationRegistry) Subscribe(id uuid.UUID) (<-chan struct{}, func()) {
	r.listenersMu.Lock()
	defer r.listenersMu.Unlock()

	ch := make(chan struct{})
	r.listeners[id] = append(r.listeners[id], ch)

	// If already revoked, close immediately
	if r.IsRevoked(id) {
		close(ch)
	}

	cancel := func() {
		r.listenersMu.Lock()
		defer r.listenersMu.Unlock()
		list := r.listeners[id]
		for i, c := range list {
			if c == ch {
				r.listeners[id] = append(list[:i], list[i+1:]...)
				break
			}
		}
	}

	return ch, cancel
}

func (r *RevocationRegistry) notifyListeners(id uuid.UUID) {
	r.listenersMu.Lock()
	defer r.listenersMu.Unlock()

	for _, ch := range r.listeners[id] {
		select {
		case <-ch:
			// already closed
		default:
			close(ch)
		}
	}
	delete(r.listeners, id)
}

// StartBackgroundPoller polls the database every interval (e.g., 5 seconds) to refresh revoked list.
func (r *RevocationRegistry) StartBackgroundPoller(ctx context.Context, interval time.Duration) {
	if r.revokedFetch == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.poll(ctx)
			}
		}
	}()
}

func (r *RevocationRegistry) poll(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	ids, err := r.revokedFetch(timeoutCtx)
	if err != nil {
		r.logger.WarnContext(ctx, "failed polling revoked devices", slog.Any("error", err))
		return
	}

	for _, id := range ids {
		if !r.IsRevoked(id) {
			r.MarkRevoked(id)
		}
	}
}
