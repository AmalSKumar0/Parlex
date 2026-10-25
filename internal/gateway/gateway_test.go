package gateway_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"parlex/internal/auth"
	"parlex/internal/catalog"
	"parlex/internal/config"
	"parlex/internal/gateway"
	"parlex/internal/observability"
	"parlex/internal/provider"
	"parlex/internal/provider/mock"
)

func setupTestGateway(t *testing.T) (*gateway.Gateway, *auth.TokenManager, *auth.RevocationRegistry, *httptest.Server, *mock.Provider) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tm := auth.NewTokenManagerFromKeys("key-1", priv, "", nil)

	logger := observability.NewLogger("debug")
	rev := auth.NewRevocationRegistry(logger, nil)

	cat, err := catalog.LoadFromFile("../../config/languages.yaml")
	require.NoError(t, err)

	provRouter := provider.NewRouter(cat)
	mockAdapter := mock.New(mock.Config{EchoDelay: 10 * time.Millisecond})
	provRouter.Register(mockAdapter)

	cfg := &config.Config{
		DeviceDailyMinutesLimit: 60,
		MaxTurnSeconds:          2,
		DrainTimeoutSeconds:     2,
		PlaybackAckTimeoutSecs:  2,
		WSPingIntervalSeconds:   1,
		WSPongTimeoutSeconds:    2,
	}

	gw := gateway.NewGateway(cfg, nil, tm, rev, provRouter, nil, logger)

	server := httptest.NewServer(http.HandlerFunc(gw.ServeHTTP))
	t.Cleanup(func() {
		server.Close()
	})

	return gw, tm, rev, server, mockAdapter
}

func TestGateway_E2E(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("time.Sleep"),
		goleak.IgnoreTopFunction("net/http.(*Server).Serve"),
		goleak.IgnoreTopFunction("github.com/coder/websocket.(*Conn).timeoutLoop"),
	)

	_, tm, rev, server, _ := setupTestGateway(t)
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	devID := uuid.New()

	token, _, err := tm.IssueDeviceStreamToken(devID)
	require.NoError(t, err)

	t.Run("Full turn workflow with mock provider", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols: []string{"translator.v1"},
			HTTPHeader: http.Header{
				"Authorization": []string{"Bearer " + token},
			},
		})
		require.NoError(t, err)
		defer conn.Close(websocket.StatusNormalClosure, "test done")

		// 1. Send hello
		helloBytes, _ := json.Marshal(gateway.HelloMessage{
			Type:   gateway.TypeHello,
			FW:     "v1.0.0",
			Proto:  1,
			Codecs: []string{"pcm16"},
		})
		err = conn.Write(ctx, websocket.MessageText, helloBytes)
		require.NoError(t, err)

		// 2. Read ready
		_, readyData, err := conn.Read(ctx)
		require.NoError(t, err)
		var ready gateway.ReadyMessage
		err = json.Unmarshal(readyData, &ready)
		require.NoError(t, err)
		assert.Equal(t, gateway.TypeReady, ready.Type)

		// 3. Send turn.start
		startBytes, _ := json.Marshal(gateway.TurnStartMessage{
			Type:   gateway.TypeTurnStart,
			TurnID: 1,
			Button: "a",
		})
		err = conn.Write(ctx, websocket.MessageText, startBytes)
		require.NoError(t, err)

		// 4. Read turn.accepted
		_, accData, err := conn.Read(ctx)
		require.NoError(t, err)
		var acc gateway.TurnAcceptedMessage
		err = json.Unmarshal(accData, &acc)
		require.NoError(t, err)
		assert.Equal(t, gateway.TypeTurnAccepted, acc.Type)
		assert.Equal(t, uint16(1), acc.TurnID)

		// 5. Stream 2 uplink audio frames
		pcmFrame := make([]byte, 640) // 20 ms
		frame0 := gateway.EncodeBinaryFrame(gateway.BinaryTypeUplink, 0, 1, 0, pcmFrame)
		err = conn.Write(ctx, websocket.MessageBinary, frame0)
		require.NoError(t, err)

		frame1 := gateway.EncodeBinaryFrame(gateway.BinaryTypeUplink, gateway.FlagLastFrame, 1, 1, pcmFrame)
		err = conn.Write(ctx, websocket.MessageBinary, frame1)
		require.NoError(t, err)

		// 6. Send turn.stop
		stopBytes, _ := json.Marshal(gateway.TurnStopMessage{
			Type:   gateway.TypeTurnStop,
			TurnID: 1,
		})
		err = conn.Write(ctx, websocket.MessageText, stopBytes)
		require.NoError(t, err)

		// 7. Receive downlink events: audio.start, audio chunks, audio.end
		receivedAudioBytes := 0
		audioEndReceived := false

		for !audioEndReceived {
			msgType, data, err := conn.Read(ctx)
			require.NoError(t, err)

			if msgType == websocket.MessageBinary {
				hdr, p, err := gateway.ParseBinaryFrame(data)
				require.NoError(t, err)
				assert.Equal(t, gateway.BinaryTypeDownlink, hdr.Type)
				assert.Equal(t, uint16(1), hdr.TurnID)
				receivedAudioBytes += len(p)
			} else if msgType == websocket.MessageText {
				var base gateway.BaseMessage
				_ = json.Unmarshal(data, &base)
				if base.Type == gateway.TypeAudioEnd {
					audioEndReceived = true
				}
			}
		}

		assert.True(t, audioEndReceived)
		assert.Greater(t, receivedAudioBytes, 0)

		// 8. Send playback.done
		doneBytes, _ := json.Marshal(gateway.PlaybackDoneMessage{
			Type:   gateway.TypePlaybackDone,
			TurnID: 1,
		})
		err = conn.Write(ctx, websocket.MessageText, doneBytes)
		require.NoError(t, err)
	})

	t.Run("Cancel turn returns immediately to idle", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols: []string{"translator.v1"},
			HTTPHeader: http.Header{
				"Authorization": []string{"Bearer " + token},
			},
		})
		require.NoError(t, err)
		defer conn.Close(websocket.StatusNormalClosure, "test done")

		// Hello -> Ready
		helloBytes, _ := json.Marshal(gateway.HelloMessage{Type: gateway.TypeHello, Proto: 1})
		_ = conn.Write(ctx, websocket.MessageText, helloBytes)
		_, _, _ = conn.Read(ctx)

		// Start turn
		startBytes, _ := json.Marshal(gateway.TurnStartMessage{Type: gateway.TypeTurnStart, TurnID: 2, Button: "b"})
		_ = conn.Write(ctx, websocket.MessageText, startBytes)
		_, _, _ = conn.Read(ctx) // accepted

		// Immediate cancel
		cancelBytes, _ := json.Marshal(gateway.TurnCancelMessage{Type: gateway.TypeTurnCancel, TurnID: 2})
		_ = conn.Write(ctx, websocket.MessageText, cancelBytes)

		// Read audio.end with reason cancelled
		_, data, err := conn.Read(ctx)
		require.NoError(t, err)
		var endMsg gateway.AudioEndMessage
		_ = json.Unmarshal(data, &endMsg)
		assert.Equal(t, gateway.TypeAudioEnd, endMsg.Type)
		assert.Equal(t, "cancelled", endMsg.Reason)
	})

	t.Run("Sequence gap triggers bad_frame error", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols: []string{"translator.v1"},
			HTTPHeader: http.Header{
				"Authorization": []string{"Bearer " + token},
			},
		})
		require.NoError(t, err)
		defer conn.Close(websocket.StatusNormalClosure, "test done")

		// Hello -> Ready
		helloBytes, _ := json.Marshal(gateway.HelloMessage{Type: gateway.TypeHello, Proto: 1})
		_ = conn.Write(ctx, websocket.MessageText, helloBytes)
		_, _, _ = conn.Read(ctx)

		// Start turn
		startBytes, _ := json.Marshal(gateway.TurnStartMessage{Type: gateway.TypeTurnStart, TurnID: 3, Button: "a"})
		_ = conn.Write(ctx, websocket.MessageText, startBytes)
		_, _, _ = conn.Read(ctx) // accepted

		// Send seq 1 instead of seq 0
		frame := gateway.EncodeBinaryFrame(gateway.BinaryTypeUplink, 0, 3, 1, make([]byte, 640))
		_ = conn.Write(ctx, websocket.MessageBinary, frame)

		// Must receive error bad_frame
		_, errData, err := conn.Read(ctx)
		require.NoError(t, err)
		var errMsg gateway.ErrorMessage
		_ = json.Unmarshal(errData, &errMsg)
		assert.Equal(t, gateway.TypeError, errMsg.Type)
		assert.Equal(t, "bad_frame", errMsg.Code)
	})

	t.Run("Replacing existing connection closes old one", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		conn1, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols: []string{"translator.v1"},
			HTTPHeader: http.Header{
				"Authorization": []string{"Bearer " + token},
			},
		})
		require.NoError(t, err)

		// Second connection for same device
		conn2, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols: []string{"translator.v1"},
			HTTPHeader: http.Header{
				"Authorization": []string{"Bearer " + token},
			},
		})
		require.NoError(t, err)
		defer conn2.Close(websocket.StatusNormalClosure, "done")

		// conn1 should be closed
		_, _, err = conn1.Read(ctx)
		require.Error(t, err)
	})

	t.Run("Revocation while connected terminates stream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols: []string{"translator.v1"},
			HTTPHeader: http.Header{
				"Authorization": []string{"Bearer " + token},
			},
		})
		require.NoError(t, err)

		// Revoke device in registry
		rev.MarkRevoked(devID)

		// Must receive bye message or connection close
		_, data, err := conn.Read(ctx)
		if err == nil {
			var bye gateway.ByeMessage
			_ = json.Unmarshal(data, &bye)
			assert.Equal(t, gateway.TypeBye, bye.Type)
			assert.Equal(t, "device_revoked", bye.Reason)
		}
	})
}
