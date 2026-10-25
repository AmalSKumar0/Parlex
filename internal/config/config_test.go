package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"parlex/internal/config"
)

func TestConfig_Validation(t *testing.T) {
	// Clear relevant env vars
	envVars := []string{
		"HTTP_ADDR", "INTERNAL_ADDR", "DATABASE_URL", "JWT_SIGNING_KEY_FILE",
		"ADMIN_API_KEY", "PROVIDER_REALTIME_API_KEY", "PROVIDER_DEFAULT",
		"PROVIDER_AUTH_MODE", "CORS_ALLOWED_ORIGINS",
	}
	for _, k := range envVars {
		orig := os.Getenv(k)
		defer os.Setenv(k, orig)
		os.Unsetenv(k)
	}

	t.Run("fails when required fields missing", func(t *testing.T) {
		_, err := config.LoadFromEnv()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DATABASE_URL is required")
		assert.Contains(t, err.Error(), "JWT_SIGNING_KEY_FILE is required")
		assert.Contains(t, err.Error(), "ADMIN_API_KEY is required")
	})

	t.Run("succeeds with mock provider and valid required fields", func(t *testing.T) {
		os.Setenv("DATABASE_URL", "postgres://localhost/test")
		os.Setenv("JWT_SIGNING_KEY_FILE", "/path/to/key.pem")
		os.Setenv("ADMIN_API_KEY", "secret-admin-key")
		os.Setenv("PROVIDER_DEFAULT", "mock")
		os.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000, http://example.com")

		cfg, err := config.LoadFromEnv()
		require.NoError(t, err)
		assert.Equal(t, ":8080", cfg.HTTPAddr)
		assert.Equal(t, "127.0.0.1:9090", cfg.InternalAddr)
		assert.Equal(t, "mock", cfg.ProviderDefault)
		assert.Equal(t, []string{"http://localhost:3000", "http://example.com"}, cfg.CORSAllowedOrigins)
		assert.Equal(t, 60, cfg.DeviceDailyMinutesLimit)
	})

	t.Run("fails when realtime provider lacks API key", func(t *testing.T) {
		os.Setenv("DATABASE_URL", "postgres://localhost/test")
		os.Setenv("JWT_SIGNING_KEY_FILE", "/path/to/key.pem")
		os.Setenv("ADMIN_API_KEY", "secret-admin-key")
		os.Setenv("PROVIDER_DEFAULT", "realtime")
		os.Unsetenv("PROVIDER_REALTIME_API_KEY")

		_, err := config.LoadFromEnv()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PROVIDER_REALTIME_API_KEY is required")
	})
}
