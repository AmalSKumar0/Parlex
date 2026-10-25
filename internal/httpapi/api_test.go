package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"parlex/internal/auth"
	"parlex/internal/catalog"
	"parlex/internal/config"
	"parlex/internal/httpapi"
	"parlex/internal/observability"
	"parlex/internal/store"
)

func setupTestApp(t *testing.T) (*config.Config, *store.DB, *auth.TokenManager, *auth.RevocationRegistry, *catalog.Catalog, http.Handler) {
	ctx := context.Background()

	pgContainer, err := testpostgres.Run(ctx,
		"postgres:16-alpine",
		testpostgres.WithDatabase("translator_test"),
		testpostgres.WithUsername("testuser"),
		testpostgres.WithPassword("testpass"),
		testpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pgContainer.Terminate(ctx)
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	// Run migrations
	err = store.RunMigrations(connStr)
	require.NoError(t, err)

	db, err := store.NewDB(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Close()
	})

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tm := auth.NewTokenManagerFromKeys("key-1", priv, "", nil)

	logger := observability.NewLogger("debug")
	rev := auth.NewRevocationRegistry(logger, func(c context.Context) ([]uuid.UUID, error) {
		return db.Queries.ListRevokedDeviceIDs(c)
	})

	cat, err := catalog.LoadFromFile("../../config/languages.yaml")
	require.NoError(t, err)

	cfg := &config.Config{
		HTTPAddr:                ":8080",
		InternalAddr:            "127.0.0.1:9090",
		DatabaseURL:             connStr,
		AdminAPIKey:             "test-admin-secret",
		ProviderDefault:         "mock",
		DeviceDailyMinutesLimit: 60,
		MaxTurnSeconds:          60,
		DrainTimeoutSeconds:     10,
		PlaybackAckTimeoutSecs:  30,
		WSPingIntervalSeconds:   15,
		WSPongTimeoutSeconds:    40,
	}

	handlers := httpapi.NewHandlers(cfg, db, tm, rev, cat, nil, logger)
	router := httpapi.NewRouter(cfg, handlers, tm, rev, nil, logger)

	return cfg, db, tm, rev, cat, router
}

func TestREST_Endpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres integration test in short mode")
	}

	cfg, _, tm, _, _, router := setupTestApp(t)

	t.Run("Health Endpoints", func(t *testing.T) {
		// Live
		req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"status":"ok"`)

		// Ready
		req = httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"status":"ready"`)
	})

	t.Run("Languages Catalog", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/languages", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var langs []map[string]any
		err := json.Unmarshal(rec.Body.Bytes(), &langs)
		require.NoError(t, err)
		assert.NotEmpty(t, langs)
	})

	var deviceID string
	var deviceSecret string
	var claimCode string
	var ownerToken string
	var deviceToken string

	t.Run("Admin Create Device", func(t *testing.T) {
		body := `{"name":"Device-Integration-1"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/devices", bytes.NewBufferString(body))
		req.Header.Set("X-Admin-Key", cfg.AdminAPIKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusCreated, rec.Code)
		var resp map[string]string
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)

		deviceID = resp["device_id"]
		deviceSecret = resp["device_secret"]
		claimCode = resp["claim_code"]
		assert.NotEmpty(t, deviceID)
		assert.NotEmpty(t, deviceSecret)
		assert.NotEmpty(t, claimCode)
	})

	t.Run("Claim Device", func(t *testing.T) {
		body := fmt.Sprintf(`{"claim_code":%q}`, claimCode)
		req := httptest.NewRequest(http.MethodPost, "/v1/devices/claim", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)

		ownerToken = resp["owner_token"].(string)
		assert.NotEmpty(t, ownerToken)

		// Reclaiming same code must fail
		req2 := httptest.NewRequest(http.MethodPost, "/v1/devices/claim", bytes.NewBufferString(body))
		req2.Header.Set("Content-Type", "application/json")
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, req2)
		assert.Equal(t, http.StatusNotFound, rec2.Code)
	})

	t.Run("Exchange Device Token", func(t *testing.T) {
		body := fmt.Sprintf(`{"device_id":%q,"device_secret":%q}`, deviceID, deviceSecret)
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/device-token", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]string
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)

		deviceToken = resp["token"]
		assert.NotEmpty(t, deviceToken)

		// Invalid secret fails with 401
		badBody := fmt.Sprintf(`{"device_id":%q,"device_secret":"bad-secret"}`, deviceID)
		reqBad := httptest.NewRequest(http.MethodPost, "/v1/auth/device-token", bytes.NewBufferString(badBody))
		recBad := httptest.NewRecorder()
		router.ServeHTTP(recBad, reqBad)
		assert.Equal(t, http.StatusUnauthorized, recBad.Code)
	})

	var currentVersion int64 = 1

	t.Run("Get and Patch Device Settings with ETag", func(t *testing.T) {
		// Read settings
		req := httptest.NewRequest(http.MethodGet, "/v1/devices/"+deviceID+"/settings", nil)
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		etag := rec.Header().Get("ETag")
		assert.Equal(t, `"1"`, etag)

		// Patch settings with stale If-Match must fail (412)
		patchBody := `{"volume":85}`
		reqStale := httptest.NewRequest(http.MethodPatch, "/v1/devices/"+deviceID+"/settings", bytes.NewBufferString(patchBody))
		reqStale.Header.Set("Authorization", "Bearer "+ownerToken)
		reqStale.Header.Set("If-Match", `"99"`)
		reqStale.Header.Set("Content-Type", "application/json")
		recStale := httptest.NewRecorder()
		router.ServeHTTP(recStale, reqStale)
		assert.Equal(t, http.StatusPreconditionFailed, recStale.Code)

		// Patch settings with unsupported pair must return 422
		badPairBody := `{"button_a":{"source":"invalid","target":"en"}}`
		reqBadPair := httptest.NewRequest(http.MethodPatch, "/v1/devices/"+deviceID+"/settings", bytes.NewBufferString(badPairBody))
		reqBadPair.Header.Set("Authorization", "Bearer "+ownerToken)
		reqBadPair.Header.Set("If-Match", `"1"`)
		reqBadPair.Header.Set("Content-Type", "application/json")
		recBadPair := httptest.NewRecorder()
		router.ServeHTTP(recBadPair, reqBadPair)
		assert.Equal(t, http.StatusUnprocessableEntity, recBadPair.Code)

		// Successful patch
		validPatch := `{"volume":90,"captions":true,"button_a":{"source":"en","target":"es"}}`
		reqValid := httptest.NewRequest(http.MethodPatch, "/v1/devices/"+deviceID+"/settings", bytes.NewBufferString(validPatch))
		reqValid.Header.Set("Authorization", "Bearer "+ownerToken)
		reqValid.Header.Set("If-Match", `"1"`)
		reqValid.Header.Set("Content-Type", "application/json")
		recValid := httptest.NewRecorder()
		router.ServeHTTP(recValid, reqValid)

		require.Equal(t, http.StatusOK, recValid.Code)
		newEtag := recValid.Header().Get("ETag")
		assert.Equal(t, `"2"`, newEtag)
		currentVersion = 2
		_ = currentVersion
	})

	t.Run("Device Status and Usage", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/devices/"+deviceID+"/status", nil)
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var status map[string]any
		err := json.Unmarshal(rec.Body.Bytes(), &status)
		require.NoError(t, err)
		assert.False(t, status["online"].(bool))
		assert.Equal(t, "IDLE", status["turn_state"])

		// Usage
		reqUsage := httptest.NewRequest(http.MethodGet, "/v1/devices/"+deviceID+"/usage", nil)
		reqUsage.Header.Set("Authorization", "Bearer "+ownerToken)
		recUsage := httptest.NewRecorder()
		router.ServeHTTP(recUsage, reqUsage)
		assert.Equal(t, http.StatusOK, recUsage.Code)
	})

	t.Run("Firmware Release Management", func(t *testing.T) {
		// Register firmware
		fwBody := `{"version":"v1.2.0","url":"https://example.com/fw.bin","sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/firmware", bytes.NewBufferString(fwBody))
		req.Header.Set("X-Admin-Key", cfg.AdminAPIKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusCreated, rec.Code)

		// Latest check - when up to date, 204
		reqLatestUpToDate := httptest.NewRequest(http.MethodGet, "/v1/firmware/latest?current=v1.2.0", nil)
		recLatestUpToDate := httptest.NewRecorder()
		router.ServeHTTP(recLatestUpToDate, reqLatestUpToDate)
		assert.Equal(t, http.StatusNoContent, recLatestUpToDate.Code)

		// Latest check - when older, returns new firmware
		reqLatestOlder := httptest.NewRequest(http.MethodGet, "/v1/firmware/latest?current=v1.0.0", nil)
		recLatestOlder := httptest.NewRecorder()
		router.ServeHTTP(recLatestOlder, reqLatestOlder)
		assert.Equal(t, http.StatusOK, recLatestOlder.Code)
		assert.Contains(t, recLatestOlder.Body.String(), `"version":"v1.2.0"`)
	})

	t.Run("Revocation Enforcement", func(t *testing.T) {
		// Revoke device
		reqRevoke := httptest.NewRequest(http.MethodPost, "/v1/admin/devices/"+deviceID+"/revoke", nil)
		reqRevoke.Header.Set("X-Admin-Key", cfg.AdminAPIKey)
		recRevoke := httptest.NewRecorder()
		router.ServeHTTP(recRevoke, reqRevoke)
		require.Equal(t, http.StatusOK, recRevoke.Code)

		// Issuing new token must fail with 403 Forbidden
		body := fmt.Sprintf(`{"device_id":%q,"device_secret":%q}`, deviceID, deviceSecret)
		reqToken := httptest.NewRequest(http.MethodPost, "/v1/auth/device-token", bytes.NewBufferString(body))
		recToken := httptest.NewRecorder()
		router.ServeHTTP(recToken, reqToken)
		assert.Equal(t, http.StatusForbidden, recToken.Code)

		// Using existing device stream token must fail with 403 Device Revoked
		reqSettings := httptest.NewRequest(http.MethodGet, "/v1/devices/"+deviceID+"/settings", nil)
		reqSettings.Header.Set("Authorization", "Bearer "+deviceToken)
		recSettings := httptest.NewRecorder()
		router.ServeHTTP(recSettings, reqSettings)
		assert.Equal(t, http.StatusForbidden, recSettings.Code)
	})
	_ = tm
}
