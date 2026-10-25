package pipeline

import (
	"context"
	"parlex/internal/provider"
)

type Adapter struct{}

func New() *Adapter {
	return &Adapter{}
}

func (a *Adapter) Name() string {
	return "pipeline"
}

func (a *Adapter) Supports(d provider.Direction) bool {
	// Stub in Phase 2
	return false
}

func (a *Adapter) Open(ctx context.Context, d provider.Direction) (provider.Session, error) {
	return nil, provider.ErrUnsupportedDirection
}
