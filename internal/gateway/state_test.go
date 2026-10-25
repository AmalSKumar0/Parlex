package gateway_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"parlex/internal/gateway"
)

func TestStateMachine_Transitions(t *testing.T) {
	t.Run("Standard full happy turn", func(t *testing.T) {
		sm := gateway.NewStateMachine()
		assert.Equal(t, gateway.StateConnected, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypeHello))
		assert.Equal(t, gateway.StateIdle, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypeTurnStart))
		assert.Equal(t, gateway.StateStarting, sm.Current())

		require.NoError(t, sm.Transition("session.ready"))
		assert.Equal(t, gateway.StateListening, sm.Current())

		require.NoError(t, sm.Transition("uplink.audio"))
		assert.Equal(t, gateway.StateListening, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypeTurnStop))
		assert.Equal(t, gateway.StateDraining, sm.Current())

		require.NoError(t, sm.Transition("first.audio"))
		assert.Equal(t, gateway.StatePlaying, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypePlaybackDone))
		assert.Equal(t, gateway.StateIdle, sm.Current())
	})

	t.Run("Turn cancel from starting", func(t *testing.T) {
		sm := gateway.NewStateMachine()
		_ = sm.Transition(gateway.TypeHello)
		_ = sm.Transition(gateway.TypeTurnStart)
		assert.Equal(t, gateway.StateStarting, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypeTurnCancel))
		assert.Equal(t, gateway.StateIdle, sm.Current())
	})

	t.Run("Turn cancel from listening", func(t *testing.T) {
		sm := gateway.NewStateMachine()
		_ = sm.Transition(gateway.TypeHello)
		_ = sm.Transition(gateway.TypeTurnStart)
		_ = sm.Transition("session.ready")
		assert.Equal(t, gateway.StateListening, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypeTurnCancel))
		assert.Equal(t, gateway.StateIdle, sm.Current())
	})

	t.Run("Turn cancel from draining", func(t *testing.T) {
		sm := gateway.NewStateMachine()
		_ = sm.Transition(gateway.TypeHello)
		_ = sm.Transition(gateway.TypeTurnStart)
		_ = sm.Transition("session.ready")
		_ = sm.Transition(gateway.TypeTurnStop)
		assert.Equal(t, gateway.StateDraining, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypeTurnCancel))
		assert.Equal(t, gateway.StateIdle, sm.Current())
	})

	t.Run("Turn cancel from playing", func(t *testing.T) {
		sm := gateway.NewStateMachine()
		_ = sm.Transition(gateway.TypeHello)
		_ = sm.Transition(gateway.TypeTurnStart)
		_ = sm.Transition("session.ready")
		_ = sm.Transition(gateway.TypeTurnStop)
		_ = sm.Transition("first.audio")
		assert.Equal(t, gateway.StatePlaying, sm.Current())

		require.NoError(t, sm.Transition(gateway.TypeTurnCancel))
		assert.Equal(t, gateway.StateIdle, sm.Current())
	})

	t.Run("Provider finishes with no audio in draining", func(t *testing.T) {
		sm := gateway.NewStateMachine()
		_ = sm.Transition(gateway.TypeHello)
		_ = sm.Transition(gateway.TypeTurnStart)
		_ = sm.Transition("session.ready")
		_ = sm.Transition(gateway.TypeTurnStop)
		assert.Equal(t, gateway.StateDraining, sm.Current())

		require.NoError(t, sm.Transition("provider.done"))
		assert.Equal(t, gateway.StateIdle, sm.Current())
	})

	t.Run("Table-driven invalid transitions", func(t *testing.T) {
		invalidCases := []struct {
			setupEvents   []string
			expectedState gateway.ConnectionState
			badEvent      string
		}{
			{nil, gateway.StateConnected, gateway.TypeTurnStart},
			{nil, gateway.StateConnected, gateway.TypeTurnStop},
			{nil, gateway.StateConnected, "uplink.audio"},
			{[]string{gateway.TypeHello}, gateway.StateIdle, gateway.TypeHello},
			{[]string{gateway.TypeHello}, gateway.StateIdle, gateway.TypeTurnStop},
			{[]string{gateway.TypeHello}, gateway.StateIdle, "first.audio"},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart}, gateway.StateStarting, gateway.TypeHello},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart}, gateway.StateStarting, gateway.TypeTurnStart},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart, "session.ready"}, gateway.StateListening, gateway.TypeHello},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart, "session.ready"}, gateway.StateListening, gateway.TypeTurnStart},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart, "session.ready", gateway.TypeTurnStop}, gateway.StateDraining, "uplink.audio"},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart, "session.ready", gateway.TypeTurnStop}, gateway.StateDraining, gateway.TypeTurnStart},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart, "session.ready", gateway.TypeTurnStop, "first.audio"}, gateway.StatePlaying, gateway.TypeTurnStart},
			{[]string{gateway.TypeHello, gateway.TypeTurnStart, "session.ready", gateway.TypeTurnStop, "first.audio"}, gateway.StatePlaying, "uplink.audio"},
		}

		for _, tc := range invalidCases {
			sm := gateway.NewStateMachine()
			for _, ev := range tc.setupEvents {
				_ = sm.Transition(ev)
			}
			assert.Equal(t, tc.expectedState, sm.Current())
			assert.False(t, sm.CanAccept(tc.badEvent))
			err := sm.Transition(tc.badEvent)
			assert.ErrorIs(t, err, gateway.ErrInvalidStateTransition)
		}
	})
}
