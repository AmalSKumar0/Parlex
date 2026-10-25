package provider_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"parlex/internal/provider"
	"parlex/internal/provider/mock"
)

func TestCircuitBreaker_TripsAfter5Failures(t *testing.T) {
	cb := provider.NewCircuitBreaker(5, 30*time.Second, 20*time.Second)

	// Inject error into mock adapter
	injectErr := errors.New("upstream provider connect timeout")
	mockAdapter := mock.New(mock.Config{InjectOpenError: injectErr})
	protected := provider.WrapWithBreaker(mockAdapter, cb)

	ctx := context.Background()
	dir := provider.Direction{Src: "en", Dst: "es"}

	// First 4 failures: recorded, circuit remains closed
	for i := 1; i <= 4; i++ {
		_, err := protected.Open(ctx, dir)
		require.ErrorIs(t, err, injectErr)
		assert.Equal(t, provider.StateClosed, cb.State())
	}

	// 5th failure: trips breaker into StateOpen
	_, err := protected.Open(ctx, dir)
	require.ErrorIs(t, err, injectErr)
	assert.Equal(t, provider.StateOpen, cb.State())

	// Next call: immediately rejected by breaker with ErrCircuitOpen without reaching inner provider
	_, err = protected.Open(ctx, dir)
	require.ErrorIs(t, err, provider.ErrCircuitOpen)
}
