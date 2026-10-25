package mock

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"parlex/internal/provider"
)

type Config struct {
	EchoDelay         time.Duration
	SlowDrainDuration time.Duration
	InjectOpenError   error
	InjectMidTurnErr  error
	ReverseAudio      bool
}

type Provider struct {
	mu     sync.RWMutex
	cfg    Config
	opened int
}

func New(cfg Config) *Provider {
	if cfg.EchoDelay == 0 {
		cfg.EchoDelay = 50 * time.Millisecond
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string {
	return "mock"
}

func (p *Provider) Supports(d provider.Direction) bool {
	return true
}

func (p *Provider) SetConfig(cfg Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
}

func (p *Provider) Open(ctx context.Context, d provider.Direction) (provider.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cfg.InjectOpenError != nil {
		return nil, p.cfg.InjectOpenError
	}

	p.opened++
	sessCtx, cancel := context.WithCancel(context.Background())

	sess := &Session{
		cfg:        p.cfg,
		direction:  d,
		ctx:        sessCtx,
		cancel:     cancel,
		eventsCh:   make(chan provider.Event, 128),
		audioBuf:   make([]byte, 0, 96000), // ~2 seconds of 24kHz audio
		audioQueue: make(chan []byte, 64),
	}

	go sess.run()
	return sess, nil
}

type Session struct {
	cfg        Config
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
		FrameMs:    200, // 4800 samples = 9600 bytes
	}
}

func (s *Session) OutputFormat() provider.AudioFormat {
	return provider.AudioFormat{
		SampleRate: 24000,
		Channels:   1,
		FrameMs:    20, // 480 samples = 960 bytes
	}
}

func (s *Session) SendAudio(ctx context.Context, pcm []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("mock session input already closed")
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

	// Consume audio chunks
	for {
		select {
		case <-s.ctx.Done():
			return
		case pcm, ok := <-s.audioQueue:
			if !ok {
				goto drain
			}
			s.mu.Lock()
			s.audioBuf = append(s.audioBuf, pcm...)
			s.mu.Unlock()

			// Optional fake source text delta
			select {
			case <-s.ctx.Done():
				return
			case s.eventsCh <- provider.Event{
				Kind: provider.EventSourceText,
				Text: "...",
			}:
			}
		}
	}

drain:
	// Audio input closed -> start drain
	if s.cfg.SlowDrainDuration > 0 {
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(s.cfg.SlowDrainDuration):
		}
	} else if s.cfg.EchoDelay > 0 {
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(s.cfg.EchoDelay):
		}
	}

	if s.cfg.InjectMidTurnErr != nil {
		select {
		case <-s.ctx.Done():
			return
		case s.eventsCh <- provider.Event{
			Kind: provider.EventFailed,
			Err:  s.cfg.InjectMidTurnErr,
		}:
		}
		return
	}

	// Emit translated caption
	select {
	case <-s.ctx.Done():
		return
	case s.eventsCh <- provider.Event{
		Kind: provider.EventTargetText,
		Text: fmt.Sprintf("[%s->%s translated text]", s.direction.Src, s.direction.Dst),
	}:
	}

	// Echo back audio in 20 ms frames (480 samples = 960 bytes @ 24 kHz)
	s.mu.Lock()
	audioCopy := make([]byte, len(s.audioBuf))
	copy(audioCopy, s.audioBuf)
	s.mu.Unlock()

	if s.cfg.ReverseAudio {
		reversePCM16(audioCopy)
	}

	chunkSize := 960 // 20 ms @ 24 kHz
	for i := 0; i < len(audioCopy); i += chunkSize {
		end := i + chunkSize
		if end > len(audioCopy) {
			end = len(audioCopy)
		}

		select {
		case <-s.ctx.Done():
			return
		case s.eventsCh <- provider.Event{
			Kind: provider.EventAudioOut,
			PCM:  audioCopy[i:end],
		}:
		}
	}

	// Final event: Done
	select {
	case <-s.ctx.Done():
		return
	case s.eventsCh <- provider.Event{Kind: provider.EventDone}:
	}
}

func reversePCM16(pcm []byte) {
	numSamples := len(pcm) / 2
	for i := 0; i < numSamples/2; i++ {
		j := numSamples - 1 - i
		pcm[i*2], pcm[j*2] = pcm[j*2], pcm[i*2]
		pcm[i*2+1], pcm[j*2+1] = pcm[j*2+1], pcm[i*2+1]
	}
}
