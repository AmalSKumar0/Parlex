package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	deviceIDKey  contextKey = "device_id"
	turnIDKey    contextKey = "turn_id"
)

// WithRequestID returns a new context carrying the request ID.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// WithDeviceID returns a new context carrying the device ID.
func WithDeviceID(ctx context.Context, deviceID string) context.Context {
	return context.WithValue(ctx, deviceIDKey, deviceID)
}

// WithTurnID returns a new context carrying the turn ID.
func WithTurnID(ctx context.Context, turnID int) context.Context {
	return context.WithValue(ctx, turnIDKey, turnID)
}

type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if reqID, ok := ctx.Value(requestIDKey).(string); ok && reqID != "" {
		r.AddAttrs(slog.String("request_id", reqID))
	}
	if devID, ok := ctx.Value(deviceIDKey).(string); ok && devID != "" {
		r.AddAttrs(slog.String("device_id", devID))
	}
	if turnID, ok := ctx.Value(turnIDKey).(int); ok && turnID != 0 {
		r.AddAttrs(slog.Int("turn_id", turnID))
	}
	return h.Handler.Handle(ctx, r)
}

// NewLogger initializes the standard slog JSON logger with appropriate log level.
func NewLogger(levelStr string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}
	base := slog.NewJSONHandler(os.Stdout, opts)
	return slog.New(&contextHandler{Handler: base})
}
