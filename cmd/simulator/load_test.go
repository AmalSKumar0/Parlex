package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
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

func TestSimulator_100ConcurrentDevicesLoad(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("time.Sleep"),
		goleak.IgnoreTopFunction("net/http.(*Server).Serve"),
		goleak.IgnoreTopFunction("github.com/coder/websocket.(*Conn).timeoutLoop"),
	)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tm := auth.NewTokenManagerFromKeys("key-1", priv, "", nil)

	logger := observability.NewLogger("error")
	rev := auth.NewRevocationRegistry(logger, nil)

	cat, err := catalog.LoadFromFile("../../config/languages.yaml")
	require.NoError(t, err)

	provRouter := provider.NewRouter(cat, 500)
	mockAdapter := mock.New(mock.Config{EchoDelay: 1 * time.Millisecond})
	provRouter.Register(mockAdapter)

	cfg := &config.Config{
		DeviceDailyMinutesLimit: 60,
		MaxTurnSeconds:          5,
		DrainTimeoutSeconds:     5,
		PlaybackAckTimeoutSecs:  5,
		WSPingIntervalSeconds:   2,
		WSPongTimeoutSeconds:    5,
		MaxProviderSessions:     500,
	}

	gw := gateway.NewGateway(cfg, nil, tm, rev, provRouter, nil, logger)
	server := httptest.NewServer(http.HandlerFunc(gw.ServeHTTP))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/stream"

	// Measure baseline memory
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	const concurrency = 100
	const turnsPerWorker = 2

	var wg sync.WaitGroup
	errCount := 0
	var mu sync.Mutex

	start := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			devID := uuid.New()
			token, _, err := tm.IssueDeviceStreamToken(devID)
			if err != nil {
				mu.Lock()
				errCount++
				mu.Unlock()
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
				Subprotocols: []string{"translator.v1"},
				HTTPHeader: http.Header{
					"Authorization": []string{"Bearer " + token},
				},
			})
			if err != nil {
				mu.Lock()
				errCount++
				mu.Unlock()
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "test done")

			// Handshake
			helloMsg, _ := json.Marshal(gateway.HelloMessage{
				Type:   gateway.TypeHello,
				FW:     "1.0.0",
				Proto:  1,
				Codecs: []string{"pcm16"},
			})
			if err := conn.Write(ctx, websocket.MessageText, helloMsg); err != nil {
				mu.Lock()
				errCount++
				mu.Unlock()
				return
			}

			_, readyBytes, err := conn.Read(ctx)
			if err != nil || !strings.Contains(string(readyBytes), `"t":"ready"`) {
				mu.Lock()
				errCount++
				mu.Unlock()
				return
			}

			pcmAudio := generateSinePCM16(16000, 440, 40) // 2 frames

			for turnNo := 1; turnNo <= turnsPerWorker; turnNo++ {
				stat := executeTurn(ctx, conn, uint16(turnNo), "a", pcmAudio)
				if !stat.Success {
					mu.Lock()
					errCount++
					mu.Unlock()
					return
				}
			}
		}()
	}

	wg.Wait()
	duration := time.Since(start)

	// Measure memory after
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	t.Logf("100 concurrent workers finished in %v, errorCount=%d", duration, errCount)
	t.Logf("HeapAlloc before: %d KB, after: %d KB", memBefore.HeapAlloc/1024, memAfter.HeapAlloc/1024)

	require.Equal(t, 0, errCount, "error rate must be zero under load")
}
