package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"parlex/internal/audio"
	"parlex/internal/config"
	"parlex/internal/httpapi"
	"parlex/internal/observability"
	"parlex/internal/provider"
	"parlex/internal/store"
)

type OutgoingMessage struct {
	IsBinary bool
	Data     []byte
}

type IncomingCommand struct {
	IsBinary bool
	Binary   *BinaryMessage
	TextType string
	RawText  []byte
}

type BinaryMessage struct {
	Header  BinaryHeader
	Payload []byte
}

type TurnMetrics struct {
	TurnID        uint16
	TurnNo        int32
	Button        string
	SrcLang       string
	DstLang       string
	Provider      string
	Status        string
	StartTime     time.Time
	FirstAudioAt  time.Time
	EndTime       time.Time
	AudioInBytes  int
	AudioOutBytes int
	ErrorCode     *string
}

type Actor struct {
	deviceID   uuid.UUID
	cfg        *config.Config
	sm         *StateMachine
	db         *store.DB
	provRouter *provider.Router
	metrics    *observability.Metrics
	logger     *slog.Logger
	conn       *websocket.Conn

	ctx    context.Context
	cancel context.CancelFunc

	inbox  chan IncomingCommand
	outbox chan OutgoingMessage

	settingsMu sync.RWMutex
	settings   store.DeviceSetting

	currentTurn    *TurnMetrics
	activeSession  provider.Session
	activeEvents   <-chan provider.Event
	firstAudioSent bool
	downlinkChunk  *audio.FrameChunker
	expectedSeq    uint32
	downlinkSeq    uint32

	maxTurnTimer  *time.Timer
	drainTimer    *time.Timer
	playbackTimer *time.Timer
}

func NewActor(
	deviceID uuid.UUID,
	cfg *config.Config,
	db *store.DB,
	provRouter *provider.Router,
	metrics *observability.Metrics,
	logger *slog.Logger,
	conn *websocket.Conn,
	initialSettings store.DeviceSetting,
) *Actor {
	ctx, cancel := context.WithCancel(context.Background())

	return &Actor{
		deviceID:      deviceID,
		cfg:           cfg,
		sm:            NewStateMachine(),
		db:            db,
		provRouter:    provRouter,
		metrics:       metrics,
		logger:        logger.With(slog.String("device_id", deviceID.String())),
		conn:          conn,
		ctx:           ctx,
		cancel:        cancel,
		inbox:         make(chan IncomingCommand, 64),
		outbox:        make(chan OutgoingMessage, 128),
		settings:      initialSettings,
		downlinkChunk: audio.NewFrameChunker(640), // 20 ms @ 16 kHz
	}
}

func (a *Actor) State() ConnectionState {
	return a.sm.Current()
}

func (a *Actor) Start() {
	if a.metrics != nil {
		a.metrics.WSConnections.Inc()
	}

	go a.writerLoop()
	go a.readerLoop()
	go a.actorLoop()
}

func (a *Actor) Close(reason string) {
	a.cancel()
	_ = a.conn.Close(websocket.StatusNormalClosure, reason)
	if a.metrics != nil {
		a.metrics.WSConnections.Dec()
	}
}

// PushSettingsUpdate pushes settings.changed to device if connection active.
func (a *Actor) PushSettingsUpdate(s store.DeviceSetting) {
	a.settingsMu.Lock()
	a.settings = s
	a.settingsMu.Unlock()

	msg := SettingsChangedMessage{
		Type:     TypeSettingsChanged,
		Settings: httpapi.FormatSettingsResponse(s),
	}
	data, _ := json.Marshal(msg)
	a.sendText(data)
}

func (a *Actor) sendText(data []byte) {
	select {
	case a.outbox <- OutgoingMessage{IsBinary: false, Data: data}:
	case <-time.After(2 * time.Second):
		if a.metrics != nil {
			a.metrics.QueueOverflowTotal.WithLabelValues("outbox").Inc()
		}
		a.logger.Error("outbox full for 2s, closing connection")
		a.Close("outbox_overflow")
	}
}

func (a *Actor) sendBinary(data []byte) {
	select {
	case a.outbox <- OutgoingMessage{IsBinary: true, Data: data}:
	case <-time.After(2 * time.Second):
		if a.metrics != nil {
			a.metrics.QueueOverflowTotal.WithLabelValues("outbox").Inc()
		}
		a.logger.Error("outbox full for 2s, closing connection")
		a.Close("outbox_overflow")
	}
}

func (a *Actor) sendError(code, message string, retryable bool, turnID *uint16) {
	if a.metrics != nil {
		a.metrics.ProtocolViolations.WithLabelValues(code).Inc()
	}
	errPayload, _ := json.Marshal(ErrorMessage{
		Type:      TypeError,
		Code:      code,
		Message:   message,
		Retryable: retryable,
		TurnID:    turnID,
	})
	a.sendText(errPayload)
}

func (a *Actor) writerLoop() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case msg, ok := <-a.outbox:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
			msgType := websocket.MessageText
			if msg.IsBinary {
				msgType = websocket.MessageBinary
			}

			err := a.conn.Write(writeCtx, msgType, msg.Data)
			cancel()
			if err != nil {
				a.logger.Warn("websocket write failed", slog.Any("error", err))
				a.cancel()
				return
			}
		}
	}
}

func (a *Actor) readerLoop() {
	for {
		msgType, data, err := a.conn.Read(a.ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				a.logger.Debug("websocket read closed", slog.Any("error", err))
			}
			a.cancel()
			return
		}

		if msgType == websocket.MessageBinary {
			header, payload, err := ParseBinaryFrame(data)
			if err != nil {
				a.logger.Warn("bad binary frame", slog.Any("error", err))
				a.sendError(httpapi.CodeBadFrame, err.Error(), false, nil)
				continue
			}
			cmd := IncomingCommand{
				IsBinary: true,
				Binary: &BinaryMessage{
					Header:  header,
					Payload: payload,
				},
			}
			a.pushInbox(cmd)
		} else if msgType == websocket.MessageText {
			t, err := ParseTextMessage(data)
			if err != nil {
				a.logger.Warn("bad json frame", slog.Any("error", err))
				a.sendError(httpapi.CodeValidationFailed, "invalid JSON", false, nil)
				continue
			}
			cmd := IncomingCommand{
				IsBinary: false,
				TextType: t,
				RawText:  data,
			}
			a.pushInbox(cmd)
		}
	}
}

func (a *Actor) pushInbox(cmd IncomingCommand) {
	select {
	case a.inbox <- cmd:
	case <-time.After(2 * time.Second):
		if a.metrics != nil {
			a.metrics.QueueOverflowTotal.WithLabelValues("inbox").Inc()
		}
		a.logger.Error("inbox full for 2s, closing connection")
		a.Close("inbox_overflow")
	}
}

func (a *Actor) actorLoop() {
	defer a.cleanupOnClose()

	for {
		var provEvCh <-chan provider.Event
		if a.activeSession != nil {
			provEvCh = a.activeEvents
		}

		var maxTurnCh <-chan time.Time
		if a.maxTurnTimer != nil {
			maxTurnCh = a.maxTurnTimer.C
		}

		var drainCh <-chan time.Time
		if a.drainTimer != nil {
			drainCh = a.drainTimer.C
		}

		var playbackCh <-chan time.Time
		if a.playbackTimer != nil {
			playbackCh = a.playbackTimer.C
		}

		select {
		case <-a.ctx.Done():
			return

		case cmd := <-a.inbox:
			a.handleCommand(cmd)

		case ev, ok := <-provEvCh:
			if !ok {
				a.handleProviderClosed()
			} else {
				a.handleProviderEvent(ev)
			}

		case <-maxTurnCh:
			a.logger.Warn("max turn seconds reached, forcing stop", slog.Int("turn_id", int(a.currentTurnID())))
			a.handleTurnStop([]byte(fmt.Sprintf(`{"t":"turn.stop","turn_id":%d}`, a.currentTurnID())))

		case <-drainCh:
			a.logger.Warn("drain timeout exceeded, forcing audio.end", slog.Int("turn_id", int(a.currentTurnID())))
			a.sendAudioEnd("error")
			a.finishTurn("timeout", nil)

		case <-playbackCh:
			a.logger.Warn("playback ack timeout exceeded", slog.Int("turn_id", int(a.currentTurnID())))
			a.finishTurn("completed", nil)
		}
	}
}

func (a *Actor) handleCommand(cmd IncomingCommand) {
	if cmd.IsBinary {
		a.handleBinaryUplink(cmd.Binary)
		return
	}

	switch cmd.TextType {
	case TypeHello:
		a.handleHello(cmd.RawText)
	case TypeTurnStart:
		a.handleTurnStart(cmd.RawText)
	case TypeTurnStop:
		a.handleTurnStop(cmd.RawText)
	case TypeTurnCancel:
		a.handleTurnCancel(cmd.RawText)
	case TypePlaybackDone:
		a.handlePlaybackDone(cmd.RawText)
	default:
		a.logger.Warn("unhandled control message", slog.String("type", cmd.TextType))
	}
}

func (a *Actor) handleHello(raw []byte) {
	if !a.sm.CanAccept(TypeHello) {
		a.sendError(httpapi.CodeBadState, fmt.Sprintf("cannot accept hello in %s", a.sm.Current()), false, nil)
		return
	}

	var hello HelloMessage
	if err := json.Unmarshal(raw, &hello); err != nil {
		a.sendError(httpapi.CodeValidationFailed, "invalid hello payload", false, nil)
		return
	}

	_ = a.sm.Transition(TypeHello)

	a.settingsMu.RLock()
	curSettings := a.settings
	a.settingsMu.RUnlock()

	ready := ReadyMessage{
		Type:       TypeReady,
		Codec:      "pcm16",
		Settings:   httpapi.FormatSettingsResponse(curSettings),
		ServerTime: time.Now().Unix(),
		Limits: map[string]any{
			"max_turn_seconds":    a.cfg.MaxTurnSeconds,
			"daily_minutes_limit": a.cfg.DeviceDailyMinutesLimit,
		},
	}
	readyBytes, _ := json.Marshal(ready)
	a.sendText(readyBytes)
}

func (a *Actor) handleTurnStart(raw []byte) {
	if !a.sm.CanAccept(TypeTurnStart) {
		a.sendError(httpapi.CodeBadState, fmt.Sprintf("cannot accept turn.start in %s", a.sm.Current()), false, nil)
		return
	}

	var ts TurnStartMessage
	if err := json.Unmarshal(raw, &ts); err != nil || (ts.Button != "a" && ts.Button != "b") {
		a.sendError(httpapi.CodeValidationFailed, "invalid turn.start payload", false, nil)
		return
	}

	// Check Quota
	if a.db != nil {
		todayStart := time.Now().UTC().Truncate(24 * time.Hour)
		tomorrow := todayStart.Add(24 * time.Hour)
		usedMins, err := a.db.Queries.GetDeviceDailyUsageMinutes(a.ctx, store.GetDeviceDailyUsageMinutesParams{
			DeviceID:    a.deviceID,
			StartedAt:   todayStart,
			StartedAt_2: tomorrow,
		})
		if err == nil && float64(usedMins) >= float64(a.cfg.DeviceDailyMinutesLimit) {
			a.sendError(httpapi.CodeQuotaExceeded, "daily translation limit reached", false, &ts.TurnID)
			return
		}
	}

	_ = a.sm.Transition(TypeTurnStart)

	a.settingsMu.RLock()
	var src, dst string
	if ts.Button == "a" {
		src, dst = a.settings.ButtonASrc, a.settings.ButtonADst
	} else {
		src, dst = a.settings.ButtonBSrc, a.settings.ButtonBDst
	}
	a.settingsMu.RUnlock()

	dir := provider.Direction{Src: src, Dst: dst}
	openStart := time.Now()
	sess, providerName, err := a.provRouter.ResolveAndOpen(a.ctx, dir)
	if a.metrics != nil {
		a.metrics.ProviderOpenSeconds.Observe(time.Since(openStart).Seconds())
	}
	if err != nil {
		a.logger.Error("failed opening provider session", slog.Any("error", err))
		a.sendError(httpapi.CodeProviderUnavailable, "translation provider unavailable", true, &ts.TurnID)
		_ = a.sm.Transition(TypeTurnCancel)
		return
	}

	_ = a.sm.Transition("session.ready")

	a.activeSession = sess
	a.activeEvents = sess.Events()
	a.expectedSeq = 0
	a.downlinkSeq = 0
	a.firstAudioSent = false
	a.currentTurn = &TurnMetrics{
		TurnID:    ts.TurnID,
		TurnNo:    int32(ts.TurnID),
		Button:    ts.Button,
		SrcLang:   src,
		DstLang:   dst,
		Provider:  providerName,
		Status:    "running",
		StartTime: time.Now(),
	}

	// Arm max turn timer
	a.maxTurnTimer = time.NewTimer(a.cfg.MaxTurnDuration())

	// Send turn.accepted
	acc, _ := json.Marshal(TurnAcceptedMessage{
		Type:   TypeTurnAccepted,
		TurnID: ts.TurnID,
		Src:    src,
		Dst:    dst,
	})
	a.sendText(acc)
}

func (a *Actor) handleBinaryUplink(bin *BinaryMessage) {
	if a.sm.Current() != StateListening {
		// Drop and ignore audio outside LISTENING
		return
	}

	turn := a.currentTurn
	sess := a.activeSession
	expectedSeq := a.expectedSeq

	if turn == nil || bin.Header.TurnID != turn.TurnID {
		a.sendError(httpapi.CodeBadFrame, "turn_id mismatch", false, &bin.Header.TurnID)
		return
	}

	if bin.Header.Seq != expectedSeq {
		a.logger.Warn("sequence gap or repeat", slog.Int("got", int(bin.Header.Seq)), slog.Int("expected", int(expectedSeq)))
		a.sendError(httpapi.CodeBadFrame, "seq violation", false, &bin.Header.TurnID)
		a.abortCurrentTurn("bad_frame")
		return
	}

	a.expectedSeq++
	turn.AudioInBytes += len(bin.Payload)

	if a.cfg.DeviceDailyMinutesLimit > 0 {
		maxBytes := a.cfg.DeviceDailyMinutesLimit * 60 * 32000
		if turn.AudioInBytes > maxBytes {
			a.sendError(httpapi.CodeQuotaExceeded, "daily audio quota exceeded", false, &turn.TurnID)
			a.abortCurrentTurn("quota")
			return
		}
	}

	// Resample 16 kHz to provider input format (24 kHz)
	resampled, err := audio.ResamplePCM16(bin.Payload, 16000, sess.InputFormat().SampleRate)
	if err != nil {
		a.logger.Error("resampling uplink audio failed", slog.Any("error", err))
		return
	}

	if err := sess.SendAudio(a.ctx, resampled); err != nil {
		a.logger.Warn("sending audio to provider failed", slog.Any("error", err))
		a.abortCurrentTurn("provider_error")
		return
	}

	if bin.Header.IsLastFrame() {
		a.handleTurnStop([]byte(fmt.Sprintf(`{"turn_id":%d}`, turn.TurnID)))
	}
}

func (a *Actor) handleTurnStop(raw []byte) {
	if a.sm.Current() != StateListening {
		return
	}

	var ts TurnStopMessage
	_ = json.Unmarshal(raw, &ts)

	turn := a.currentTurn
	sess := a.activeSession
	if a.maxTurnTimer != nil {
		a.maxTurnTimer.Stop()
		a.maxTurnTimer = nil
	}

	if turn == nil || (ts.TurnID != 0 && ts.TurnID != turn.TurnID) {
		a.sendError(httpapi.CodeBadFrame, "turn_id mismatch on turn.stop", false, nil)
		return
	}

	_ = a.sm.Transition(TypeTurnStop)

	// Arm drain timeout
	a.drainTimer = time.NewTimer(a.cfg.DrainTimeout())

	if sess != nil {
		_ = sess.CloseInput(a.ctx)
	}
}

func (a *Actor) handleTurnCancel(raw []byte) {
	var tc TurnCancelMessage
	_ = json.Unmarshal(raw, &tc)

	a.abortCurrentTurn("cancelled")
}

func (a *Actor) handlePlaybackDone(raw []byte) {
	if a.sm.Current() != StatePlaying {
		return
	}

	var pd PlaybackDoneMessage
	_ = json.Unmarshal(raw, &pd)

	a.finishTurn("completed", nil)
}

func (a *Actor) abortCurrentTurn(reason string) {
	turn := a.currentTurn
	sess := a.activeSession
	if sess != nil {
		sess.Abort()
	}

	if turn != nil {
		endMsg, _ := json.Marshal(AudioEndMessage{
			Type:   TypeAudioEnd,
			TurnID: turn.TurnID,
			Reason: reason,
		})
		a.sendText(endMsg)
		a.finishTurn(reason, &reason)
	} else {
		_ = a.sm.Transition(TypeTurnCancel)
	}
}

func (a *Actor) finishTurn(status string, errCode *string) {
	turn := a.currentTurn
	sess := a.activeSession
	a.currentTurn = nil
	a.activeSession = nil
	a.activeEvents = nil
	a.firstAudioSent = false

	if a.maxTurnTimer != nil {
		a.maxTurnTimer.Stop()
		a.maxTurnTimer = nil
	}
	if a.drainTimer != nil {
		a.drainTimer.Stop()
		a.drainTimer = nil
	}
	if a.playbackTimer != nil {
		a.playbackTimer.Stop()
		a.playbackTimer = nil
	}

	_ = a.sm.Transition(TypePlaybackDone)

	if turn != nil {
		turn.EndTime = time.Now()
		turn.Status = status
		turn.ErrorCode = errCode

		audioInMs := int32(turn.AudioInBytes / 32) // 16 kHz 16-bit mono = 32 bytes/ms
		audioOutMs := int32(turn.AudioOutBytes / 32)
		totalMs := int32(turn.EndTime.Sub(turn.StartTime).Milliseconds())
		var firstAudioMs *int32
		if !turn.FirstAudioAt.IsZero() {
			fa := int32(turn.FirstAudioAt.Sub(turn.StartTime).Milliseconds())
			firstAudioMs = &fa
		}

		// Log completion line (no audio content)
		a.logger.Info("turn_completed",
			slog.Int("turn_id", int(turn.TurnID)),
			slog.String("src", turn.SrcLang),
			slog.String("dst", turn.DstLang),
			slog.String("provider", turn.Provider),
			slog.String("status", turn.Status),
			slog.Int("audio_in_ms", int(audioInMs)),
			slog.Int("audio_out_ms", int(audioOutMs)),
			slog.Any("first_audio_ms", firstAudioMs),
			slog.Int("total_ms", int(totalMs)),
			slog.Any("error_code", turn.ErrorCode),
		)

		if a.metrics != nil {
			dir := fmt.Sprintf("%s->%s", turn.SrcLang, turn.DstLang)
			a.metrics.TurnsTotal.WithLabelValues(dir, turn.Provider, turn.Status).Inc()
			a.metrics.AudioInSecondsTotal.Add(float64(audioInMs) / 1000.0)
			a.metrics.AudioOutSecondsTotal.Add(float64(audioOutMs) / 1000.0)
			a.metrics.TurnTotalSeconds.Observe(float64(totalMs) / 1000.0)
			if firstAudioMs != nil {
				a.metrics.FirstAudioSeconds.Observe(float64(*firstAudioMs) / 1000.0)
			}
		}

		// Persist turn in database
		if a.db != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = a.db.Queries.CreateTurn(ctx, store.CreateTurnParams{
					ID:           uuid.New(),
					DeviceID:     a.deviceID,
					TurnNo:       turn.TurnNo,
					SrcLang:      turn.SrcLang,
					DstLang:      turn.DstLang,
					Provider:     turn.Provider,
					Status:       turn.Status,
					AudioInMs:    audioInMs,
					AudioOutMs:   audioOutMs,
					FirstAudioMs: store.NullInt4(firstAudioMs),
					TotalMs:      store.Int4(totalMs),
					ErrorCode:    store.NullText(turn.ErrorCode),
					StartedAt:    turn.StartTime,
				})
			}()
		}
	}

	if sess != nil {
		sess.Abort()
	}
}

func (a *Actor) handleProviderClosed() {
	if a.activeSession != nil {
		a.activeSession = nil
		a.activeEvents = nil
	}
	if a.sm.Current() != StateIdle {
		if !a.firstAudioSent {
			_ = a.sm.Transition("provider.done")
			a.finishTurn("completed", nil)
		} else {
			a.finishTurn("completed", nil)
		}
	}
}

func (a *Actor) handleProviderEvent(ev provider.Event) {
	if a.currentTurn == nil {
		return
	}
	turnID := a.currentTurn.TurnID
	sess := a.activeSession

	switch ev.Kind {
	case provider.EventSourceText:
		a.settingsMu.RLock()
		captions := a.settings.Captions
		a.settingsMu.RUnlock()
		if captions {
			txtMsg, _ := json.Marshal(TextMessage{
				Type:   TypeText,
				TurnID: turnID,
				Kind:   "source",
				Delta:  ev.Text,
			})
			a.sendText(txtMsg)
		}

	case provider.EventTargetText:
		a.settingsMu.RLock()
		captions := a.settings.Captions
		a.settingsMu.RUnlock()
		if captions {
			txtMsg, _ := json.Marshal(TextMessage{
				Type:   TypeText,
				TurnID: turnID,
				Kind:   "target",
				Delta:  ev.Text,
			})
			a.sendText(txtMsg)
		}

	case provider.EventAudioOut:
		if !a.firstAudioSent {
			a.firstAudioSent = true
			if a.currentTurn != nil {
				a.currentTurn.FirstAudioAt = time.Now()
			}
			if a.drainTimer != nil {
				a.drainTimer.Stop()
				a.drainTimer = nil
			}

			// Transition to PLAYING
			_ = a.sm.Transition("first.audio")

			// Send audio.start
			startMsg, _ := json.Marshal(AudioStartMessage{
				Type:     TypeAudioStart,
				TurnID:   turnID,
				Codec:    "pcm16",
				Rate:     16000,
				Channels: 1,
			})
			a.sendText(startMsg)
		}

		if sess == nil {
			return
		}

		// Resample from provider rate to 16 kHz
		resampled16k, err := audio.ResamplePCM16(ev.PCM, sess.OutputFormat().SampleRate, 16000)
		if err != nil {
			a.logger.Error("resampling downlink audio failed", slog.Any("error", err))
			return
		}

		// Slice into 20 ms frames (640 bytes)
		frames := a.downlinkChunk.Push(resampled16k)
		for _, frame := range frames {
			seq := a.downlinkSeq
			a.downlinkSeq++
			if a.currentTurn != nil {
				a.currentTurn.AudioOutBytes += len(frame)
			}

			bin := EncodeBinaryFrame(BinaryTypeDownlink, 0, turnID, seq, frame)
			a.sendBinary(bin)
		}

	case provider.EventDone:
		// Flush any remaining partial audio
		if remaining := a.downlinkChunk.Flush(true); remaining != nil && a.firstAudioSent {
			seq := a.downlinkSeq
			a.downlinkSeq++
			if a.currentTurn != nil {
				a.currentTurn.AudioOutBytes += len(remaining)
			}

			bin := EncodeBinaryFrame(BinaryTypeDownlink, FlagLastFrame, turnID, seq, remaining)
			a.sendBinary(bin)
		}

		if a.drainTimer != nil {
			a.drainTimer.Stop()
			a.drainTimer = nil
		}

		if !a.firstAudioSent {
			// Provider ended with no audio
			_ = a.sm.Transition("provider.done")
			a.finishTurn("completed", nil)
			return
		}

		// Send audio.end
		endMsg, _ := json.Marshal(AudioEndMessage{
			Type:   TypeAudioEnd,
			TurnID: turnID,
			Reason: "done",
		})
		a.sendText(endMsg)

		// Arm playback ack timeout (30s)
		a.playbackTimer = time.NewTimer(a.cfg.PlaybackAckTimeout())

	case provider.EventFailed:
		a.logger.Error("provider session failed", slog.Any("error", ev.Err))
		a.sendError(httpapi.CodeProviderError, "provider error mid-turn", false, &turnID)
		a.abortCurrentTurn("failed")
	}
}

func (a *Actor) sendAudioEnd(reason string) {
	if a.currentTurn == nil {
		return
	}
	endMsg, _ := json.Marshal(AudioEndMessage{
		Type:   TypeAudioEnd,
		TurnID: a.currentTurn.TurnID,
		Reason: reason,
	})
	a.sendText(endMsg)
}

func (a *Actor) currentTurnID() uint16 {
	if a.currentTurn != nil {
		return a.currentTurn.TurnID
	}
	return 0
}

func (a *Actor) cleanupOnClose() {
	if a.activeSession != nil {
		a.activeSession.Abort()
		a.activeSession = nil
		a.activeEvents = nil
	}
	if a.maxTurnTimer != nil {
		a.maxTurnTimer.Stop()
		a.maxTurnTimer = nil
	}
	if a.drainTimer != nil {
		a.drainTimer.Stop()
		a.drainTimer = nil
	}
	if a.playbackTimer != nil {
		a.playbackTimer.Stop()
		a.playbackTimer = nil
	}
}
