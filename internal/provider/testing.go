package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunProviderContractSuite runs the standardized provider contract tests across all provider implementations.
func RunProviderContractSuite(t *testing.T, p Provider) {
	t.Run("Provider info and language support", func(t *testing.T) {
		assert.NotEmpty(t, p.Name())
		dir := Direction{Src: "en", Dst: "es"}
		assert.True(t, p.Supports(dir))
	})

	t.Run("Standard session audio flow and graceful drain", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		dir := Direction{Src: "en", Dst: "es"}
		sess, err := p.Open(ctx, dir)
		require.NoError(t, err)
		defer sess.Abort()

		inFmt := sess.InputFormat()
		outFmt := sess.OutputFormat()
		assert.Greater(t, inFmt.SampleRate, 0)
		assert.Greater(t, outFmt.SampleRate, 0)

		// Send 400ms of audio (2 frames at 24kHz PCM16: 9600 bytes each)
		frameBytes := inFmt.SampleRate * (inFmt.FrameMs) / 1000 * 2
		chunk := make([]byte, frameBytes)
		for i := range chunk {
			chunk[i] = byte(i % 256)
		}

		err = sess.SendAudio(ctx, chunk)
		require.NoError(t, err)

		err = sess.SendAudio(ctx, chunk)
		require.NoError(t, err)

		// Signal graceful end
		err = sess.CloseInput(ctx)
		require.NoError(t, err)

		// Drain events until Done or Failed
		var audioOutBytes int
		var receivedDone bool
		var receivedText bool

		events := sess.Events()
		for ev := range events {
			switch ev.Kind {
			case EventAudioOut:
				audioOutBytes += len(ev.PCM)
			case EventSourceText, EventTargetText:
				receivedText = true
			case EventDone:
				receivedDone = true
			case EventFailed:
				t.Fatalf("unexpected provider failure: %v", ev.Err)
			}
		}

		assert.True(t, receivedDone, "expected EventDone at end of graceful drain")
		assert.Greater(t, audioOutBytes, 0, "trailing audio must not be lost")
		_ = receivedText
	})

	t.Run("Immediate abort cancels session cleanly", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		dir := Direction{Src: "en", Dst: "es"}
		sess, err := p.Open(ctx, dir)
		require.NoError(t, err)

		// Send audio then abort immediately
		chunk := make([]byte, 1920)
		_ = sess.SendAudio(ctx, chunk)

		sess.Abort()

		// Events channel should close promptly
		select {
		case _, ok := <-sess.Events():
			if ok {
				// Draining until closed
				for range sess.Events() {
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Events channel did not close promptly after Abort()")
		}
	})
}
