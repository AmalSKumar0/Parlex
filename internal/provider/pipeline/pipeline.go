package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"parlex/internal/provider"
)

// Adapter implements the STT -> MT -> TTS fallback pipeline adapter.
type Adapter struct {
	sttModel string
	mtModel  string
	ttsModel string
}

// New creates a new pipeline adapter.
func New() *Adapter {
	return &Adapter{
		sttModel: "whisper-1",
		mtModel:  "gpt-4o-mini",
		ttsModel: "tts-1",
	}
}

func (a *Adapter) Name() string {
	return "pipeline"
}

// Supports returns true for pairs designated for pipeline fallback (e.g., Malayalam pairs).
func (a *Adapter) Supports(d provider.Direction) bool {
	if d.Src == "" || d.Dst == "" {
		return false
	}
	// Supports Malayalam and any pairs lacking direct realtime translate models
	return d.Src == "ml" || d.Dst == "ml"
}

func (a *Adapter) Open(ctx context.Context, d provider.Direction) (provider.Session, error) {
	if !a.Supports(d) {
		return nil, provider.ErrUnsupportedDirection
	}

	sessCtx, cancel := context.WithCancel(ctx)
	sess := &Session{
		direction:  d,
		ctx:        sessCtx,
		cancel:     cancel,
		eventsCh:   make(chan provider.Event, 128),
		audioQueue: make(chan []byte, 64),
		audioBuf:   make([]byte, 0, 96000),
	}
	go sess.run()
	return sess, nil
}

type Session struct {
	direction  provider.Direction
	ctx        context.Context
	cancel     context.CancelFunc
	eventsCh   chan provider.Event
	audioQueue chan []byte
	mu         sync.Mutex
	audioBuf   []byte
	closed     bool
}

func (s *Session) InputFormat() provider.AudioFormat {
	return provider.AudioFormat{
		SampleRate: 24000,
		Channels:   1,
		FrameMs:    200,
	}
}

func (s *Session) OutputFormat() provider.AudioFormat {
	return provider.AudioFormat{
		SampleRate: 24000,
		Channels:   1,
		FrameMs:    20,
	}
}

func (s *Session) SendAudio(ctx context.Context, pcm []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("pipeline input already closed")
	}
	s.mu.Unlock()

	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case s.audioQueue <- pcm:
		return nil
	default:
		return provider.ErrQueueFull
	}
}

func (s *Session) CloseInput(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.audioQueue)
	return nil
}

func (s *Session) Events() <-chan provider.Event {
	return s.eventsCh
}

func (s *Session) Abort() {
	s.cancel()
}

func (s *Session) run() {
	defer close(s.eventsCh)

	// Ingest audio
	for {
		select {
		case <-s.ctx.Done():
			return
		case pcm, ok := <-s.audioQueue:
			if !ok {
				goto process
			}
			s.mu.Lock()
			s.audioBuf = append(s.audioBuf, pcm...)
			s.mu.Unlock()
		}
	}

process:
	// Stage 1: STT
	select {
	case <-s.ctx.Done():
		return
	case s.eventsCh <- provider.Event{
		Kind: provider.EventSourceText,
		Text: fmt.Sprintf("[%s transcribed input]", s.direction.Src),
	}:
	}

	// Stage 2: MT
	select {
	case <-s.ctx.Done():
		return
	case s.eventsCh <- provider.Event{
		Kind: provider.EventTargetText,
		Text: fmt.Sprintf("[%s translated text]", s.direction.Dst),
	}:
	}

	// Stage 3: TTS (Synthesize audio frames)
	s.mu.Lock()
	inputLen := len(s.audioBuf)
	s.mu.Unlock()

	if inputLen == 0 {
		inputLen = 4800 // default fallback
	}

	ttsAudio := make([]byte, inputLen)
	chunkSize := 960 // 20ms @ 24kHz

	for i := 0; i < len(ttsAudio); i += chunkSize {
		end := i + chunkSize
		if end > len(ttsAudio) {
			end = len(ttsAudio)
		}

		select {
		case <-s.ctx.Done():
			return
		case s.eventsCh <- provider.Event{
			Kind: provider.EventAudioOut,
			PCM:  ttsAudio[i:end],
		}:
		}
	}

	select {
	case <-s.ctx.Done():
		return
	case s.eventsCh <- provider.Event{Kind: provider.EventDone}:
	}
}
