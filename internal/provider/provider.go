package provider

import (
	"context"
	"errors"
)

var (
	ErrUnsupportedDirection = errors.New("unsupported direction for provider")
	ErrProviderUnavailable  = errors.New("provider unavailable")
	ErrQueueFull            = errors.New("provider audio queue full")
)

type Direction struct {
	Src, Dst string
}

func (d Direction) String() string {
	return d.Src + "->" + d.Dst
}

type AudioFormat struct {
	SampleRate int // Hz
	Channels   int
	FrameMs    int // preferred input frame size
}

type EventKind int

const (
	EventAudioOut EventKind = iota
	EventSourceText
	EventTargetText
	EventDone
	EventFailed
)

type Event struct {
	Kind EventKind // AudioOut, SourceText, TargetText, Done, Failed
	PCM  []byte    // PCM16 LE at OutputFormat.SampleRate for AudioOut
	Text string
	Err  error
}

type Session interface {
	InputFormat() AudioFormat
	OutputFormat() AudioFormat
	SendAudio(ctx context.Context, pcm []byte) error // returns promptly; bounded
	CloseInput(ctx context.Context) error            // graceful: flush and drain
	Events() <-chan Event                            // closed after Done or Failed
	Abort()                                          // immediate teardown, no drain
}

type Provider interface {
	Name() string
	Supports(d Direction) bool
	Open(ctx context.Context, d Direction) (Session, error)
}
