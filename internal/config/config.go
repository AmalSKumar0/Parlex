package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all validated configuration for the server.
type Config struct {
	HTTPAddr                string
	InternalAddr            string
	DatabaseURL             string
	JWTSigningKeyFile       string
	JWTRotationKeyFile      string
	AdminAPIKey             string
	ProviderRealtimeAPIKey  string
	ProviderRealtimeBaseURL string
	ProviderAuthMode        string
	ProviderDefault         string
	DeviceDailyMinutesLimit int
	MaxTurnSeconds          int
	DrainTimeoutSeconds     int
	PlaybackAckTimeoutSecs  int
	WSPingIntervalSeconds   int
	WSPongTimeoutSeconds    int
	PrewarmTTLSeconds       int
	MaxProviderSessions     int
	CORSAllowedOrigins      []string
	LogLevel                string
}

// DrainTimeout returns the drain timeout as a time.Duration.
func (c *Config) DrainTimeout() time.Duration {
	return time.Duration(c.DrainTimeoutSeconds) * time.Second
}

// MaxTurnDuration returns max turn duration.
func (c *Config) MaxTurnDuration() time.Duration {
	return time.Duration(c.MaxTurnSeconds) * time.Second
}

// PlaybackAckTimeout returns the playback ack timeout.
func (c *Config) PlaybackAckTimeout() time.Duration {
	return time.Duration(c.PlaybackAckTimeoutSecs) * time.Second
}

// WSPingInterval returns WebSocket ping interval.
func (c *Config) WSPingInterval() time.Duration {
	return time.Duration(c.WSPingIntervalSeconds) * time.Second
}

// WSPongTimeout returns WebSocket pong timeout.
func (c *Config) WSPongTimeout() time.Duration {
	return time.Duration(c.WSPongTimeoutSeconds) * time.Second
}

// LoadFromEnv loads and validates environment variables.
func LoadFromEnv() (*Config, error) {
	cfg := &Config{
		HTTPAddr:                getEnv("HTTP_ADDR", ":8080"),
		InternalAddr:            getEnv("INTERNAL_ADDR", "127.0.0.1:9090"),
		DatabaseURL:             os.Getenv("DATABASE_URL"),
		JWTSigningKeyFile:       os.Getenv("JWT_SIGNING_KEY_FILE"),
		JWTRotationKeyFile:      os.Getenv("JWT_ROTATION_KEY_FILE"),
		AdminAPIKey:             os.Getenv("ADMIN_API_KEY"),
		ProviderRealtimeAPIKey:  os.Getenv("PROVIDER_REALTIME_API_KEY"),
		ProviderRealtimeBaseURL: getEnv("PROVIDER_REALTIME_BASE_URL", "wss://api.openai.com/v1/realtime/translations"),
		ProviderAuthMode:        getEnv("PROVIDER_AUTH_MODE", "bearer"),
		ProviderDefault:         getEnv("PROVIDER_DEFAULT", "realtime"),
		LogLevel:                getEnv("LOG_LEVEL", "info"),
	}

	var errs []string

	if cfg.DatabaseURL == "" {
		errs = append(errs, "DATABASE_URL is required")
	}
	if cfg.JWTSigningKeyFile == "" {
		errs = append(errs, "JWT_SIGNING_KEY_FILE is required")
	}
	if cfg.AdminAPIKey == "" {
		errs = append(errs, "ADMIN_API_KEY is required")
	}

	if cfg.ProviderDefault != "mock" && cfg.ProviderDefault != "pipeline" && cfg.ProviderDefault != "realtime" {
		errs = append(errs, fmt.Sprintf("invalid PROVIDER_DEFAULT %q: must be realtime, pipeline, or mock", cfg.ProviderDefault))
	}

	if cfg.ProviderDefault == "realtime" && cfg.ProviderRealtimeAPIKey == "" {
		errs = append(errs, "PROVIDER_REALTIME_API_KEY is required when PROVIDER_DEFAULT is realtime")
	}

	if cfg.ProviderAuthMode != "bearer" && cfg.ProviderAuthMode != "azure-api-key" {
		errs = append(errs, fmt.Sprintf("invalid PROVIDER_AUTH_MODE %q: must be bearer or azure-api-key", cfg.ProviderAuthMode))
	}

	var err error
	cfg.DeviceDailyMinutesLimit, err = parseEnvInt("DEVICE_DAILY_MINUTES_LIMIT", 60)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.MaxTurnSeconds, err = parseEnvInt("MAX_TURN_SECONDS", 60)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.DrainTimeoutSeconds, err = parseEnvInt("DRAIN_TIMEOUT_SECONDS", 10)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.PlaybackAckTimeoutSecs, err = parseEnvInt("PLAYBACK_ACK_TIMEOUT_SECONDS", 30)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.WSPingIntervalSeconds, err = parseEnvInt("WS_PING_INTERVAL_SECONDS", 15)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.WSPongTimeoutSeconds, err = parseEnvInt("WS_PONG_TIMEOUT_SECONDS", 40)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.PrewarmTTLSeconds, err = parseEnvInt("PREWARM_TTL_SECONDS", 0)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.MaxProviderSessions, err = parseEnvInt("MAX_PROVIDER_SESSIONS", 200)
	if err != nil {
		errs = append(errs, err.Error())
	}

	origins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if origins != "" {
		for _, o := range strings.Split(origins, ",") {
			trimmed := strings.TrimSpace(o)
			if trimmed != "" {
				cfg.CORSAllowedOrigins = append(cfg.CORSAllowedOrigins, trimmed)
			}
		}
	}

	if len(errs) > 0 {
		return nil, errors.New("configuration validation errors:\n  - " + strings.Join(errs, "\n  - "))
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func parseEnvInt(key string, defaultVal int) (int, error) {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return n, nil
}
