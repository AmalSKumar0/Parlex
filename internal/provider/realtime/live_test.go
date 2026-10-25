//go:build live

package realtime_test

import (
	"os"
	"testing"
	"time"

	"parlex/internal/provider"
	"parlex/internal/provider/realtime"
)

func TestRealtimeProvider_Live(t *testing.T) {
	apiKey := os.Getenv("PROVIDER_REALTIME_API_KEY")
	if apiKey == "" {
		t.Skip("skipping live test: PROVIDER_REALTIME_API_KEY is not set")
	}

	baseURL := os.Getenv("PROVIDER_REALTIME_BASE_URL")
	authMode := os.Getenv("PROVIDER_AUTH_MODE")
	if authMode == "" {
		authMode = "bearer"
	}

	prov := realtime.New(realtime.Config{
		BaseURL:  baseURL,
		APIKey:   apiKey,
		AuthMode: authMode,
		Timeout:  10 * time.Second,
	})

	provider.RunProviderContractSuite(t, prov)
}
