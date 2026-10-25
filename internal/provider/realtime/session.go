package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"parlex/internal/provider"
)

type Session struct {
	conn      *websocket.Conn
	direction provider.Direction
	ctx       context.Context
	cancel    context.CancelFunc

	eventsCh chan provider.Event
	outbox   chan []byte
	wg       sync.WaitGroup

	mu          sync.Mutex
	inputClosed bool
}

func newSession(
	conn *websocket.Conn,
	d provider.Direction,
	ctx context.Context,
	cancel context.CancelFunc,
) *Session {
	return &Session{
		conn:      conn,
		direction: d,
		ctx:       ctx,
		cancel:    cancel,
		eventsCh:  make(chan provider.Event, 128),
		outbox:    make(chan []byte, 64),
	}
}

func (s *Session) start() {
	s.wg.Add(2)
	go s.writeLoop()
	go s.readLoop()

	go func() {
		s.wg.Wait()
		close(s.eventsCh)
		_ = s.conn.Close(websocket.StatusNormalClosure, "session ended")
	}()
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
		FrameMs:    20,
	}
}

func (s *Session) SendAudio(ctx context.Context, pcm []byte) error {
	s.mu.Lock()
	if s.inputClosed {
		s.mu.Unlock()
		return errors.New("realtime session input already closed")
	}
	s.mu.Unlock()

	b64Audio := base64.StdEncoding.EncodeToString(pcm)
	appendMsg := map[string]any{
		"type":  "session.input_audio_buffer.append",
		"audio": b64Audio,
	}
	data, err := json.Marshal(appendMsg)
	if err != nil {
		return err
	}

	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case s.outbox <- data:
		return nil
	default:
		return provider.ErrQueueFull
	}
}

func (s *Session) CloseInput(ctx context.Context) error {
	s.mu.Lock()
	if s.inputClosed {
		s.mu.Unlock()
		return nil
	}
	s.inputClosed = true
	s.mu.Unlock()

	closeMsg := []byte(`{"type":"session.close"}`)
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case s.outbox <- closeMsg:
		return nil
	}
}

func (s *Session) Events() <-chan provider.Event {
	return s.eventsCh
}

func (s *Session) Abort() {
	s.cancel()
	_ = s.conn.Close(websocket.StatusNormalClosure, "aborted")
}

func (s *Session) writeLoop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case msg, ok := <-s.outbox:
			if !ok {
				return
			}
			writeCtx, cancelWrite := context.WithTimeout(s.ctx, 5*time.Second)
			err := s.conn.Write(writeCtx, websocket.MessageText, msg)
			cancelWrite()
			if err != nil {
				select {
				case <-s.ctx.Done():
					return
				default:
					s.emitEvent(provider.Event{
						Kind: provider.EventFailed,
						Err:  fmt.Errorf("realtime websocket write failed: %w", err),
					})
					s.Abort()
					return
				}
			}
		}
	}
}

type realtimeServerEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func (s *Session) readLoop() {
	defer s.wg.Done()
	defer s.cancel()

	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			select {
			case <-s.ctx.Done():
				// Expected teardown
				return
			default:
				s.emitEvent(provider.Event{
					Kind: provider.EventFailed,
					Err:  fmt.Errorf("realtime read error: %w", err),
				})
				return
			}
		}

		var ev realtimeServerEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "session.output_audio.delta":
			rawPCM, err := base64.StdEncoding.DecodeString(ev.Delta)
			if err == nil && len(rawPCM) > 0 {
				s.emitEvent(provider.Event{
					Kind: provider.EventAudioOut,
					PCM:  rawPCM,
				})
			}

		case "session.output_transcript.delta":
			if ev.Delta != "" {
				s.emitEvent(provider.Event{
					Kind: provider.EventTargetText,
					Text: ev.Delta,
				})
			}

		case "session.input_transcript.delta":
			if ev.Delta != "" {
				s.emitEvent(provider.Event{
					Kind: provider.EventSourceText,
					Text: ev.Delta,
				})
			}

		case "session.closed":
			s.emitEvent(provider.Event{
				Kind: provider.EventDone,
			})
			return

		case "error":
			errMsg := "unknown realtime error"
			if ev.Error != nil && ev.Error.Message != "" {
				errMsg = ev.Error.Message
			}
			s.emitEvent(provider.Event{
				Kind: provider.EventFailed,
				Err:  errors.New(errMsg),
			})
			return
		}
	}
}

func (s *Session) emitEvent(ev provider.Event) {
	select {
	case <-s.ctx.Done():
	case s.eventsCh <- ev:
	}
}
