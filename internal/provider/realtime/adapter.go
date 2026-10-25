package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"parlex/internal/provider"
)

// Config configures the cloud realtime translation provider.
type Config struct {
	BaseURL  string // default: "wss://api.openai.com"
	APIKey   string
	AuthMode string // "bearer" (default) or "azure-api-key"
	Model    string // default: "gpt-realtime-translate"
	Timeout  time.Duration
}

// Provider implements the cloud streaming translation adapter.
type Provider struct {
	cfg Config
}

// New creates a new realtime Provider.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "wss://api.openai.com"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-realtime-translate"
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = "bearer"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string {
	return "realtime"
}

// Supports checks if the realtime model supports this translation direction.
func (p *Provider) Supports(d provider.Direction) bool {
	// Standard supported realtime language codes
	// Malayalam (ml) is routed to pipeline fallback adapter if needed.
	if d.Src == "" || d.Dst == "" {
		return false
	}
	return true
}

// Open establishes a WebSocket translation session for a specific direction.
func (p *Provider) Open(ctx context.Context, d provider.Direction) (provider.Session, error) {
	if p.cfg.APIKey == "" {
		return nil, errors.New("PROVIDER_REALTIME_API_KEY is not set")
	}

	endpoint := p.cfg.BaseURL
	if !strings.HasPrefix(endpoint, "ws://") && !strings.HasPrefix(endpoint, "wss://") {
		if strings.HasPrefix(endpoint, "https://") {
			endpoint = "wss://" + strings.TrimPrefix(endpoint, "https://")
		} else if strings.HasPrefix(endpoint, "http://") {
			endpoint = "ws://" + strings.TrimPrefix(endpoint, "http://")
		} else {
			endpoint = "wss://" + endpoint
		}
	}
	endpoint = strings.TrimRight(endpoint, "/") + fmt.Sprintf("/v1/realtime/translations?model=%s", p.cfg.Model)

	headers := http.Header{}
	if p.cfg.AuthMode == "azure-api-key" {
		headers.Set("api-key", p.cfg.APIKey)
	} else {
		headers.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancelDial()

	conn, _, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("realtime dial failed: %w", err)
	}

	sessCtx, cancelSess := context.WithCancel(context.Background())
	sess := newSession(conn, d, sessCtx, cancelSess)

	// Send initial session.update to configure target language and transcription
	updateMsg := map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"audio": map[string]any{
				"output": map[string]any{
					"language": d.Dst,
				},
				"input": map[string]any{
					"transcription": map[string]any{
						"model": "gpt-realtime-whisper",
					},
				},
			},
		},
	}
	updateBytes, err := json.Marshal(updateMsg)
	if err != nil {
		sess.Abort()
		return nil, err
	}

	if err := conn.Write(dialCtx, websocket.MessageText, updateBytes); err != nil {
		sess.Abort()
		return nil, fmt.Errorf("sending session.update failed: %w", err)
	}

	sess.start()
	return sess, nil
}
