package gateway

import (
	"errors"
	"fmt"
)

type ConnectionState string

const (
	StateConnected ConnectionState = "CONNECTED"
	StateIdle      ConnectionState = "IDLE"
	StateStarting  ConnectionState = "STARTING"
	StateListening ConnectionState = "LISTENING"
	StateDraining  ConnectionState = "DRAINING"
	StatePlaying   ConnectionState = "PLAYING"
)

var (
	ErrInvalidStateTransition = errors.New("invalid state transition")
)

// StateMachine manages connection state transitions strictly.
type StateMachine struct {
	current ConnectionState
}

func NewStateMachine() *StateMachine {
	return &StateMachine{
		current: StateConnected,
	}
}

func (sm *StateMachine) Current() ConnectionState {
	return sm.current
}

// CanAccept checks if an event/message is valid in the current state.
func (sm *StateMachine) CanAccept(event string) bool {
	switch sm.current {
	case StateConnected:
		return event == TypeHello
	case StateIdle:
		return event == TypeTurnStart
	case StateStarting:
		return event == "session.ready" || event == TypeTurnCancel
	case StateListening:
		return event == "uplink.audio" || event == TypeTurnStop || event == TypeTurnCancel
	case StateDraining:
		return event == "first.audio" || event == "provider.done" || event == TypeTurnCancel
	case StatePlaying:
		return event == TypePlaybackDone || event == TypeTurnCancel
	default:
		return false
	}
}

// Transition performs a valid state transition or returns error.
func (sm *StateMachine) Transition(event string) error {
	switch sm.current {
	case StateConnected:
		if event == TypeHello {
			sm.current = StateIdle
			return nil
		}
	case StateIdle:
		if event == TypeTurnStart {
			sm.current = StateStarting
			return nil
		}
	case StateStarting:
		if event == "session.ready" {
			sm.current = StateListening
			return nil
		}
		if event == TypeTurnCancel {
			sm.current = StateIdle
			return nil
		}
	case StateListening:
		if event == TypeTurnStop {
			sm.current = StateDraining
			return nil
		}
		if event == TypeTurnCancel {
			sm.current = StateIdle
			return nil
		}
		if event == "uplink.audio" {
			return nil // stays in LISTENING
		}
	case StateDraining:
		if event == "first.audio" {
			sm.current = StatePlaying
			return nil
		}
		if event == "provider.done" || event == TypeTurnCancel {
			sm.current = StateIdle
			return nil
		}
	case StatePlaying:
		if event == TypePlaybackDone || event == TypeTurnCancel {
			sm.current = StateIdle
			return nil
		}
	}

	return fmt.Errorf("%w: cannot accept event %q in state %q", ErrInvalidStateTransition, event, sm.current)
}
