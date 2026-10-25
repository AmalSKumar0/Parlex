package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"parlex/internal/provider"
	"parlex/internal/provider/pipeline"
)

func TestPipelineAdapter_MalayalamSupport(t *testing.T) {
	adapter := pipeline.New()
	assert.Equal(t, "pipeline", adapter.Name())

	assert.True(t, adapter.Supports(provider.Direction{Src: "ml", Dst: "en"}))
	assert.True(t, adapter.Supports(provider.Direction{Src: "en", Dst: "ml"}))
	assert.False(t, adapter.Supports(provider.Direction{Src: "en", Dst: "es"}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := adapter.Open(ctx, provider.Direction{Src: "ml", Dst: "en"})
	require.NoError(t, err)
	defer sess.Abort()

	err = sess.SendAudio(ctx, make([]byte, 4800))
	require.NoError(t, err)

	err = sess.CloseInput(ctx)
	require.NoError(t, err)

	var audioCount int
	var receivedDone bool
	for ev := range sess.Events() {
		if ev.Kind == provider.EventAudioOut {
			audioCount++
		} else if ev.Kind == provider.EventDone {
			receivedDone = true
		}
	}

	assert.True(t, receivedDone)
	assert.Greater(t, audioCount, 0)
}
