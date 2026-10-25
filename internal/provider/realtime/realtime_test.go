package realtime_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"parlex/internal/provider"
	"parlex/internal/provider/realtime"
)

func startMockRealtimeServer(t *testing.T) *httptest.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		apiKey := r.Header.Get("api-key")
		if auth == "" && apiKey == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx := r.Context()

		// Read session.update
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var updateMsg map[string]any
		_ = json.Unmarshal(data, &updateMsg)

		// Loop reading input audio appends until session.close
		var accumulatedAudio []byte
		for {
			_, msgData, err := conn.Read(ctx)
			if err != nil {
				return
			}

			var event struct {
				Type  string `json:"type"`
				Audio string `json:"audio"`
			}
			if err := json.Unmarshal(msgData, &event); err != nil {
				continue
			}

			if event.Type == "session.input_audio_buffer.append" {
				raw, _ := base64.StdEncoding.DecodeString(event.Audio)
				accumulatedAudio = append(accumulatedAudio, raw...)
			} else if event.Type == "session.close" {
				break
			}
		}

		// Emit transcript delta
		txtMsg, _ := json.Marshal(map[string]any{
			"type":  "session.output_transcript.delta",
			"delta": "translated speech text",
		})
		_ = conn.Write(ctx, websocket.MessageText, txtMsg)

		// Echo back trailing audio
		if len(accumulatedAudio) > 0 {
			audioMsg, _ := json.Marshal(map[string]any{
				"type":  "session.output_audio.delta",
				"delta": base64.StdEncoding.EncodeToString(accumulatedAudio),
			})
			_ = conn.Write(ctx, websocket.MessageText, audioMsg)
		}

		// Emit session.closed
		closedMsg, _ := json.Marshal(map[string]any{
			"type": "session.closed",
		})
		_ = conn.Write(ctx, websocket.MessageText, closedMsg)
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestRealtimeProvider_Contract(t *testing.T) {
	server := startMockRealtimeServer(t)

	prov := realtime.New(realtime.Config{
		BaseURL:  server.URL,
		APIKey:   "test-openai-key",
		AuthMode: "bearer",
		Timeout:  3 * time.Second,
	})

	provider.RunProviderContractSuite(t, prov)
}
