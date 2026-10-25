package groq

import (
	"context"
	"testing"
	"time"

	"parlex/internal/provider"
)

func TestGroqProvider_Interface(t *testing.T) {
	p := New(Config{APIKey: "dummy"})
	if p.Name() != "groq" {
		t.Fatalf("expected name groq, got %s", p.Name())
	}

	if !p.Supports(provider.Direction{Src: "ml", Dst: "en"}) {
		t.Fatal("expected support for ml->en")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sess, err := p.Open(ctx, provider.Direction{Src: "ml", Dst: "en"})
	if err != nil {
		t.Fatalf("failed to open session: %v", err)
	}

	if sess.InputFormat().SampleRate != 16000 {
		t.Fatalf("expected 16kHz, got %d", sess.InputFormat().SampleRate)
	}

	// Abort cleans up
	sess.Abort()
}
