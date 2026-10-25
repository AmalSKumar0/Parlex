package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"parlex/internal/gateway"
)

type TurnStats struct {
	TurnIndex    int
	FirstAudioMs int64
	TotalMs      int64
	Success      bool
	ErrorMsg     string
}

func main() {
	serverURL := flag.String("server", "http://localhost:8080", "Base HTTP URL of server")
	adminKey := flag.String("admin-key", "test-admin-secret", "Admin API key for self-registration")
	deviceID := flag.String("device-id", "", "Device UUID")
	secret := flag.String("secret", "", "Device 32-byte secret (hex)")
	wavPath := flag.String("wav", "", "Path to 16kHz 16-bit mono WAV input file (optional)")
	button := flag.String("button", "a", "Speaking button to press ('a' or 'b')")
	concurrency := flag.Int("concurrency", 1, "Concurrent simulated devices")
	loops := flag.Int("loops", 1, "Number of consecutive turns per worker")
	flag.Parse()

	if *serverURL == "" {
		log.Fatal("--server is required")
	}

	fmt.Printf("Starting simulator: server=%s, concurrency=%d, loops=%d, button=%s\n",
		*serverURL, *concurrency, *loops, *button)

	var pcmAudio []byte
	var err error
	if *wavPath != "" {
		pcmAudio, err = readWAVFile(*wavPath)
		if err != nil {
			log.Fatalf("failed reading wav file %s: %v", *wavPath, err)
		}
	} else {
		// Generate 1 second of 16 kHz 440Hz PCM16 audio
		pcmAudio = generateSinePCM16(16000, 440, 1000)
	}

	var wg sync.WaitGroup
	statsChan := make(chan TurnStats, *concurrency**loops)

	startOverall := time.Now()

	for workerID := 0; workerID < *concurrency; workerID++ {
		wg.Add(1)
		go func(wID int) {
			defer wg.Done()
			devID := *deviceID
			sec := *secret

			// If device credentials not passed, simulate self-registration
			if devID == "" || sec == "" {
				d, s, err := registerTestDevice(*serverURL, *adminKey)
				if err != nil {
					log.Printf("Worker %d failed registering test device: %v", wID, err)
					return
				}
				devID, sec = d, s
			}

			// Get device stream token
			token, err := getDeviceToken(*serverURL, devID, sec)
			if err != nil {
				log.Printf("Worker %d failed getting device stream token: %v", wID, err)
				return
			}

			wsURL := strings.Replace(*serverURL, "http://", "ws://", 1)
			wsURL = strings.Replace(wsURL, "https://", "wss://", 1) + "/v1/stream"

			ctx := context.Background()
			conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
				Subprotocols: []string{"translator.v1"},
				HTTPHeader: http.Header{
					"Authorization": []string{"Bearer " + token},
				},
			})
			if err != nil {
				log.Printf("Worker %d failed connecting websocket: %v", wID, err)
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "done")

			// Send hello
			helloMsg, _ := json.Marshal(gateway.HelloMessage{
				Type:   gateway.TypeHello,
				FW:     "sim-v1.0.0",
				Proto:  1,
				Codecs: []string{"pcm16"},
			})
			if err := conn.Write(ctx, websocket.MessageText, helloMsg); err != nil {
				log.Printf("Worker %d failed sending hello: %v", wID, err)
				return
			}

			// Read ready
			_, readyBytes, err := conn.Read(ctx)
			if err != nil {
				log.Printf("Worker %d failed reading ready: %v", wID, err)
				return
			}
			var ready gateway.ReadyMessage
			_ = json.Unmarshal(readyBytes, &ready)

			// Run loops
			for turnIdx := 1; turnIdx <= *loops; turnIdx++ {
				stat := executeTurn(ctx, conn, uint16(turnIdx), *button, pcmAudio)
				statsChan <- stat
			}
		}(workerID)
	}

	wg.Wait()
	close(statsChan)
	totalDuration := time.Since(startOverall)

	var allStats []TurnStats
	var firstAudioLatencies []float64
	var totalLatencies []float64
	successCount := 0

	for s := range statsChan {
		allStats = append(allStats, s)
		if s.Success {
			successCount++
			if s.FirstAudioMs > 0 {
				firstAudioLatencies = append(firstAudioLatencies, float64(s.FirstAudioMs))
			}
			totalLatencies = append(totalLatencies, float64(s.TotalMs))
		}
	}

	fmt.Printf("\n--- Simulator Summary ---\n")
	fmt.Printf("Total turns attempted: %d, succeeded: %d, duration: %v\n",
		len(allStats), successCount, totalDuration)

	if len(firstAudioLatencies) > 0 {
		sort.Float64s(firstAudioLatencies)
		p50 := percentile(firstAudioLatencies, 0.50)
		p95 := percentile(firstAudioLatencies, 0.95)
		fmt.Printf("First audio latency: p50 = %.1f ms, p95 = %.1f ms\n", p50, p95)
	}
	if len(totalLatencies) > 0 {
		sort.Float64s(totalLatencies)
		p50 := percentile(totalLatencies, 0.50)
		p95 := percentile(totalLatencies, 0.95)
		fmt.Printf("Turn total latency: p50 = %.1f ms, p95 = %.1f ms\n", p50, p95)
	}
}

func executeTurn(ctx context.Context, conn *websocket.Conn, turnID uint16, button string, pcm []byte) TurnStats {
	start := time.Now()
	var firstAudioMs int64

	// Send turn.start
	tsMsg, _ := json.Marshal(gateway.TurnStartMessage{
		Type:   gateway.TypeTurnStart,
		TurnID: turnID,
		Button: button,
	})
	if err := conn.Write(ctx, websocket.MessageText, tsMsg); err != nil {
		return TurnStats{TurnIndex: int(turnID), Success: false, ErrorMsg: err.Error()}
	}

	// Read turn.accepted
	_, accBytes, err := conn.Read(ctx)
	if err != nil {
		return TurnStats{TurnIndex: int(turnID), Success: false, ErrorMsg: err.Error()}
	}
	var acc gateway.TurnAcceptedMessage
	_ = json.Unmarshal(accBytes, &acc)

	// Stream audio in 20 ms frames (640 bytes @ 16 kHz)
	frameSize := 640
	seq := uint32(0)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for offset := 0; offset < len(pcm); offset += frameSize {
		end := offset + frameSize
		isLast := false
		if end >= len(pcm) {
			end = len(pcm)
			isLast = true
		}

		payload := pcm[offset:end]
		var flags byte
		if isLast {
			flags = gateway.FlagLastFrame
		}

		frame := gateway.EncodeBinaryFrame(gateway.BinaryTypeUplink, flags, turnID, seq, payload)
		seq++

		<-ticker.C
		if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
			return TurnStats{TurnIndex: int(turnID), Success: false, ErrorMsg: err.Error()}
		}
	}

	// Send turn.stop
	stopMsg, _ := json.Marshal(gateway.TurnStopMessage{
		Type:   gateway.TypeTurnStop,
		TurnID: turnID,
	})
	if err := conn.Write(ctx, websocket.MessageText, stopMsg); err != nil {
		return TurnStats{TurnIndex: int(turnID), Success: false, ErrorMsg: err.Error()}
	}

	// Read downlink events until audio.end
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return TurnStats{TurnIndex: int(turnID), Success: false, ErrorMsg: err.Error()}
		}

		if msgType == websocket.MessageBinary {
			if firstAudioMs == 0 {
				firstAudioMs = time.Since(start).Milliseconds()
			}
		} else if msgType == websocket.MessageText {
			var base gateway.BaseMessage
			_ = json.Unmarshal(data, &base)

			if base.Type == gateway.TypeAudioStart && firstAudioMs == 0 {
				firstAudioMs = time.Since(start).Milliseconds()
			} else if base.Type == gateway.TypeAudioEnd {
				break
			} else if base.Type == gateway.TypeError {
				var errResp gateway.ErrorMessage
				_ = json.Unmarshal(data, &errResp)
				return TurnStats{TurnIndex: int(turnID), Success: false, ErrorMsg: errResp.Message}
			}
		}
	}

	// Send playback.done
	doneMsg, _ := json.Marshal(gateway.PlaybackDoneMessage{
		Type:   gateway.TypePlaybackDone,
		TurnID: turnID,
	})
	_ = conn.Write(ctx, websocket.MessageText, doneMsg)

	totalMs := time.Since(start).Milliseconds()
	return TurnStats{
		TurnIndex:    int(turnID),
		FirstAudioMs: firstAudioMs,
		TotalMs:      totalMs,
		Success:      true,
	}
}

func registerTestDevice(baseURL, adminKey string) (string, string, error) {
	// Call admin endpoint
	reqBody := `{"name":"Simulator-Device"}`
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/admin/devices", bytes.NewBufferString(reqBody))
	req.Header.Set("X-Admin-Key", adminKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	return out.DeviceID, out.DeviceSecret, nil
}

func getDeviceToken(baseURL, devID, secret string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"device_id":     devID,
		"device_secret": secret,
	})
	resp, err := http.Post(baseURL+"/v1/auth/device-token", "application/json", bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

func generateSinePCM16(sampleRate int, freq float64, durationMs int) []byte {
	numSamples := sampleRate * durationMs / 1000
	buf := make([]byte, numSamples*2)

	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		val := math.Sin(2 * math.Pi * freq * t)
		sample := int16(val * 25000)
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(sample))
	}
	return buf
}

func readWAVFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("invalid WAV file")
	}
	// Skip 44-byte standard header
	return data[44:], nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}
