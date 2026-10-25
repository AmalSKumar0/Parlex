package main

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

func TestSimulator_100ConsecutiveTurns(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("time.Sleep"),
		goleak.IgnoreTopFunction("net/http.(*Server).Serve"),
		goleak.IgnoreTopFunction("github.com/coder/websocket.(*Conn).timeoutLoop"),
	)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tm := auth.NewTokenManagerFromKeys("key-1", priv, "", nil)

	logger := observability.NewLogger("warn")
	rev := auth.NewRevocationRegistry(logger, nil)

	cat, err := catalog.LoadFromFile("../../config/languages.yaml")
	require.NoError(t, err)

	provRouter := provider.NewRouter(cat)
	mockAdapter := mock.New(mock.Config{EchoDelay: 2 * time.Millisecond})
	provRouter.Register(mockAdapter)

	cfg := &config.Config{
		DeviceDailyMinutesLimit: 60,
		MaxTurnSeconds:          5,
		DrainTimeoutSeconds:     5,
		PlaybackAckTimeoutSecs:  5,
		WSPingIntervalSeconds:   2,
		WSPongTimeoutSeconds:    5,
	}

	gw := gateway.NewGateway(cfg, nil, tm, rev, provRouter, nil, logger)
	server := httptest.NewServer(http.HandlerFunc(gw.ServeHTTP))
	defer server.Close()

	devID := uuid.New()
	token, _, err := tm.IssueDeviceStreamToken(devID)
	require.NoError(t, err)

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/stream"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"translator.v1"},
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + token},
		},
	})
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "test finished")

	// Handshake
	helloMsg, _ := json.Marshal(gateway.HelloMessage{
		Type:   gateway.TypeHello,
		FW:     "1.0.0",
		Proto:  1,
		Codecs: []string{"pcm16"},
	})
	err = conn.Write(ctx, websocket.MessageText, helloMsg)
	require.NoError(t, err)

	_, readyBytes, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(readyBytes), `"t":"ready"`)

	// Generate 60ms PCM16 audio (3 frames)
	pcmAudio := generateSinePCM16(16000, 440, 60)

	// Run 100 consecutive turns
	const totalTurns = 100
	for turnNo := 1; turnNo <= totalTurns; turnNo++ {
		stat := executeTurn(ctx, conn, uint16(turnNo), "a", pcmAudio)
		require.True(t, stat.Success, "turn %d failed: %s", turnNo, stat.ErrorMsg)
		require.Greater(t, stat.FirstAudioMs, int64(0), "turn %d first audio should be > 0", turnNo)
	}
}
