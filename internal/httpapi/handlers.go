package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"parlex/internal/auth"
	"parlex/internal/catalog"
	"parlex/internal/config"
	"parlex/internal/store"
	"parlex/internal/usage"
)

type DeviceStatusProvider interface {
	IsOnline(deviceID uuid.UUID) bool
	TurnState(deviceID uuid.UUID) string
	OnSettingsUpdated(deviceID uuid.UUID, settings store.DeviceSetting)
}

type Handlers struct {
	cfg            *config.Config
	db             *store.DB
	tm             *auth.TokenManager
	rev            *auth.RevocationRegistry
	cat            *catalog.Catalog
	statusProvider DeviceStatusProvider
	claimTracker   *usage.ClaimFailureTracker
	claimLimiter   *usage.KeyedRateLimiter
	tokenLimiter   *usage.KeyedRateLimiter
	settingsLimit  *usage.KeyedRateLimiter
	adminLimiter   *usage.KeyedRateLimiter
	logger         *slog.Logger
}

func NewHandlers(
	cfg *config.Config,
	db *store.DB,
	tm *auth.TokenManager,
	rev *auth.RevocationRegistry,
	cat *catalog.Catalog,
	sp DeviceStatusProvider,
	logger *slog.Logger,
) *Handlers {
	return &Handlers{
		cfg:            cfg,
		db:             db,
		tm:             tm,
		rev:            rev,
		cat:            cat,
		statusProvider: sp,
		claimTracker:   usage.NewClaimFailureTracker(10, 15*time.Minute),
		claimLimiter:   usage.NewKeyedRateLimiter(5, time.Minute),
		tokenLimiter:   usage.NewKeyedRateLimiter(10, time.Minute),
		settingsLimit:  usage.NewKeyedRateLimiter(30, time.Minute),
		adminLimiter:   usage.NewKeyedRateLimiter(60, time.Minute),
		logger:         logger,
	}
}

func getClientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// HealthLive responds to liveness probes.
func (h *Handlers) HealthLive(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HealthReady responds to readiness probes, checking DB reachability and config.
func (h *Handlers) HealthReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		h.logger.ErrorContext(r.Context(), "health_ready_db_unreachable", slog.Any("error", err))
		WriteProblem(w, r, http.StatusServiceUnavailable, CodeProviderUnavailable, "Service Unavailable", "Database is unreachable.")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// GetLanguages returns catalog languages and routing info.
func (h *Handlers) GetLanguages(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, h.cat.Languages())
}

// AdminCreateDevice registers a new device and returns credentials.
func (h *Handlers) AdminCreateDevice(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !h.adminLimiter.Allow(ip) {
		WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "Rate Limited", "Admin rate limit exceeded.")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid JSON payload.")
		return
	}

	secretHex, secretHash, err := auth.GenerateDeviceSecret()
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed to generate device secret.")
		return
	}

	claimCode, err := auth.GenerateClaimCode()
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed to generate claim code.")
		return
	}

	ctx := r.Context()
	dev, err := h.db.Queries.CreateDevice(ctx, store.CreateDeviceParams{
		Name:       req.Name,
		SecretHash: secretHash,
		ClaimCode:  store.Text(claimCode),
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "failed creating device", slog.Any("error", err))
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed to store device.")
		return
	}

	// Create initial default settings
	_, err = h.db.Queries.CreateDeviceSettings(ctx, store.CreateDeviceSettingsParams{
		DeviceID:   dev.ID,
		Version:    1,
		ButtonASrc: "ml",
		ButtonADst: "en",
		ButtonBSrc: "en",
		ButtonBDst: "ml",
		Volume:     70,
		Captions:   false,
		LedLevel:   50,
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "failed creating default device settings", slog.Any("error", err))
	}

	// Audit log
	detailJSON, _ := json.Marshal(map[string]any{"name": req.Name})
	_, _ = h.db.Queries.CreateAuditLog(ctx, store.CreateAuditLogParams{
		Actor:    "admin",
		Action:   "create_device",
		DeviceID: store.PgUUID(dev.ID),
		Detail:   detailJSON,
	})

	WriteJSON(w, http.StatusCreated, map[string]any{
		"device_id":     dev.ID,
		"device_secret": secretHex,
		"claim_code":    claimCode,
	})
}

// AdminRevokeDevice revokes a device and triggers connection close.
func (h *Handlers) AdminRevokeDevice(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !h.adminLimiter.Allow(ip) {
		WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "Rate Limited", "Admin rate limit exceeded.")
		return
	}

	idStr := chi.URLParam(r, "id")
	devID, err := uuid.Parse(idStr)
	if err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid device ID UUID.")
		return
	}

	ctx := r.Context()
	dev, err := h.db.Queries.RevokeDevice(ctx, devID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Not Found", "Device not found.")
			return
		}
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed revoking device.")
		return
	}

	h.rev.MarkRevoked(dev.ID)

	detailJSON, _ := json.Marshal(map[string]any{"revoked_at": dev.RevokedAt.Time})
	_, _ = h.db.Queries.CreateAuditLog(ctx, store.CreateAuditLogParams{
		Actor:    "admin",
		Action:   "revoke_device",
		DeviceID: store.PgUUID(dev.ID),
		Detail:   detailJSON,
	})

	WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// AdminRegisterFirmware registers a new firmware release.
func (h *Handlers) AdminRegisterFirmware(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !h.adminLimiter.Allow(ip) {
		WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "Rate Limited", "Admin rate limit exceeded.")
		return
	}

	var req struct {
		Version    string  `json:"version"`
		URL        string  `json:"url"`
		SHA256     string  `json:"sha256"`
		MinVersion *string `json:"min_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid JSON body.")
		return
	}
	if req.Version == "" || req.URL == "" || req.SHA256 == "" {
		WriteProblem(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, "Validation Failed", "version, url, and sha256 are required.")
		return
	}

	ctx := r.Context()
	fw, err := h.db.Queries.CreateFirmwareRelease(ctx, store.CreateFirmwareReleaseParams{
		Version:    req.Version,
		Url:        req.URL,
		Sha256:     req.SHA256,
		MinVersion: store.NullText(req.MinVersion),
	})
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed storing firmware release.")
		return
	}

	detailJSON, _ := json.Marshal(map[string]any{"version": req.Version})
	_, _ = h.db.Queries.CreateAuditLog(ctx, store.CreateAuditLogParams{
		Actor:  "admin",
		Action: "register_firmware",
		Detail: detailJSON,
	})

	WriteJSON(w, http.StatusCreated, fw)
}

// ClaimDevice exchanges a claim code for an owner token.
func (h *Handlers) ClaimDevice(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if h.claimTracker.IsLockedOut(ip) {
		WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "Rate Limited", "Too many failed claim attempts. Temporarily locked out.")
		return
	}
	if !h.claimLimiter.Allow(ip) {
		WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "Rate Limited", "Claim rate limit exceeded.")
		return
	}

	var req struct {
		ClaimCode string `json:"claim_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ClaimCode == "" {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "claim_code is required.")
		return
	}

	ctx := r.Context()
	trimmedCode := strings.ToUpper(strings.TrimSpace(req.ClaimCode))
	dev, err := h.db.Queries.GetDeviceByClaimCode(ctx, store.Text(trimmedCode))
	if err != nil {
		h.claimTracker.RecordFailure(ip)
		WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Not Found", "Invalid or already claimed code.")
		return
	}

	claimedDev, err := h.db.Queries.ClaimDevice(ctx, dev.ID)
	if err != nil {
		h.claimTracker.RecordFailure(ip)
		WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Not Found", "Device cannot be claimed.")
		return
	}

	h.claimTracker.RecordSuccess(ip)

	ownerToken, exp, err := h.tm.IssueOwnerToken(claimedDev.ID)
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed to issue owner token.")
		return
	}

	detailJSON, _ := json.Marshal(map[string]any{"ip": ip})
	_, _ = h.db.Queries.CreateAuditLog(ctx, store.CreateAuditLogParams{
		Actor:    "claim",
		Action:   "claim_device",
		DeviceID: store.PgUUID(claimedDev.ID),
		Detail:   detailJSON,
	})

	WriteJSON(w, http.StatusOK, map[string]any{
		"device_id":   claimedDev.ID,
		"owner_token": ownerToken,
		"expires_at":  exp.Format(time.RFC3339),
	})
}

// IssueDeviceToken verifies device secret and returns a 15-minute stream token.
func (h *Handlers) IssueDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid JSON payload.")
		return
	}

	devID, err := uuid.Parse(req.DeviceID)
	if err != nil || req.DeviceSecret == "" {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "device_id (UUID) and device_secret are required.")
		return
	}

	if !h.tokenLimiter.Allow(devID.String()) {
		WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "Rate Limited", "Device token rate limit exceeded.")
		return
	}

	ctx := r.Context()
	dev, err := h.db.Queries.GetDeviceByID(ctx, devID)
	if err != nil {
		WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Invalid credentials.")
		return
	}

	if dev.RevokedAt.Valid || h.rev.IsRevoked(dev.ID) {
		WriteProblem(w, r, http.StatusForbidden, CodeDeviceRevoked, "Forbidden", "Device has been revoked.")
		return
	}

	if !auth.VerifyDeviceSecret(req.DeviceSecret, dev.SecretHash) {
		WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Invalid credentials.")
		return
	}

	token, exp, err := h.tm.IssueDeviceStreamToken(dev.ID)
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed issuing token.")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": exp.Format(time.RFC3339),
		"ws_url":     "/v1/stream",
	})
}

// GetDeviceSettings returns device settings with ETag.
func (h *Handlers) GetDeviceSettings(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Authentication required.")
		return
	}

	idStr := chi.URLParam(r, "id")
	devID, err := uuid.Parse(idStr)
	if err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid UUID.")
		return
	}

	if claims.Role != "admin" && claims.DeviceID != devID.String() {
		WriteProblem(w, r, http.StatusForbidden, CodeForbidden, "Forbidden", "Access denied for this device.")
		return
	}

	ctx := r.Context()
	settings, err := h.db.Queries.GetDeviceSettings(ctx, devID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Not Found", "Settings not found.")
			return
		}
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed fetching settings.")
		return
	}

	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, settings.Version))
	WriteJSON(w, http.StatusOK, formatSettingsResponse(settings))
}

// UpdateDeviceSettings handles partial updates, requiring If-Match.
func (h *Handlers) UpdateDeviceSettings(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Authentication required.")
		return
	}

	idStr := chi.URLParam(r, "id")
	devID, err := uuid.Parse(idStr)
	if err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid UUID.")
		return
	}

	if claims.Role != "admin" && (claims.DeviceID != devID.String() || claims.Role != "owner") {
		WriteProblem(w, r, http.StatusForbidden, CodeForbidden, "Forbidden", "Only device owner may update settings.")
		return
	}

	if !h.settingsLimit.Allow(devID.String()) {
		WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "Rate Limited", "Settings update rate limit exceeded.")
		return
	}

	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		WriteProblem(w, r, http.StatusPreconditionFailed, CodePreconditionFailed, "Precondition Failed", "If-Match header is required.")
		return
	}
	expectedVersion, err := parseETagVersion(ifMatch)
	if err != nil {
		WriteProblem(w, r, http.StatusPreconditionFailed, CodePreconditionFailed, "Precondition Failed", "Invalid If-Match ETag.")
		return
	}

	var req struct {
		ButtonA *struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"button_a"`
		ButtonB *struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"button_b"`
		Volume   *int16 `json:"volume"`
		Captions *bool  `json:"captions"`
		LEDLevel *int16 `json:"led_level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid JSON payload.")
		return
	}

	ctx := r.Context()
	cur, err := h.db.Queries.GetDeviceSettings(ctx, devID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Not Found", "Settings not found.")
			return
		}
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed loading existing settings.")
		return
	}

	btnASrc, btnADst := cur.ButtonASrc, cur.ButtonADst
	if req.ButtonA != nil {
		btnASrc, btnADst = req.ButtonA.Source, req.ButtonA.Target
	}
	btnBSrc, btnBDst := cur.ButtonBSrc, cur.ButtonBDst
	if req.ButtonB != nil {
		btnBSrc, btnBDst = req.ButtonB.Source, req.ButtonB.Target
	}

	// Validate language pairs in catalog
	if err := h.cat.ValidatePair(btnASrc, btnADst); err != nil {
		WriteProblem(w, r, http.StatusUnprocessableEntity, CodeUnsupportedPair, "Unsupported Pair", fmt.Sprintf("button_a: %v", err))
		return
	}
	if err := h.cat.ValidatePair(btnBSrc, btnBDst); err != nil {
		WriteProblem(w, r, http.StatusUnprocessableEntity, CodeUnsupportedPair, "Unsupported Pair", fmt.Sprintf("button_b: %v", err))
		return
	}

	volume := cur.Volume
	if req.Volume != nil {
		if *req.Volume < 0 || *req.Volume > 100 {
			WriteProblem(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, "Validation Failed", "volume must be between 0 and 100.")
			return
		}
		volume = *req.Volume
	}

	captions := cur.Captions
	if req.Captions != nil {
		captions = *req.Captions
	}

	ledLevel := cur.LedLevel
	if req.LEDLevel != nil {
		if *req.LEDLevel < 0 || *req.LEDLevel > 100 {
			WriteProblem(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, "Validation Failed", "led_level must be between 0 and 100.")
			return
		}
		ledLevel = *req.LEDLevel
	}

	updated, err := h.db.Queries.UpdateDeviceSettings(ctx, store.UpdateDeviceSettingsParams{
		DeviceID:   devID,
		ButtonASrc: btnASrc,
		ButtonADst: btnADst,
		ButtonBSrc: btnBSrc,
		ButtonBDst: btnBDst,
		Volume:     volume,
		Captions:   captions,
		LedLevel:   ledLevel,
		Version:    expectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteProblem(w, r, http.StatusPreconditionFailed, CodePreconditionFailed, "Precondition Failed", "Stale version or version mismatch.")
			return
		}
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed updating settings.")
		return
	}

	if h.statusProvider != nil {
		h.statusProvider.OnSettingsUpdated(devID, updated)
	}

	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, updated.Version))
	WriteJSON(w, http.StatusOK, formatSettingsResponse(updated))
}

// GetDeviceStatus returns online flag, firmware, last seen, and turn state.
func (h *Handlers) GetDeviceStatus(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Authentication required.")
		return
	}

	idStr := chi.URLParam(r, "id")
	devID, err := uuid.Parse(idStr)
	if err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid UUID.")
		return
	}

	if claims.Role != "admin" && claims.DeviceID != devID.String() {
		WriteProblem(w, r, http.StatusForbidden, CodeForbidden, "Forbidden", "Access denied.")
		return
	}

	ctx := r.Context()
	dev, err := h.db.Queries.GetDeviceByID(ctx, devID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Not Found", "Device not found.")
			return
		}
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed loading device.")
		return
	}

	online := false
	turnState := "IDLE"
	if h.statusProvider != nil {
		online = h.statusProvider.IsOnline(devID)
		turnState = h.statusProvider.TurnState(devID)
	}

	var lastSeenStr *string
	if dev.LastSeenAt.Valid {
		s := dev.LastSeenAt.Time.Format(time.RFC3339)
		lastSeenStr = &s
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"online":       online,
		"last_seen_at": lastSeenStr,
		"firmware":     dev.Firmware,
		"turn_state":   turnState,
	})
}

// GetDeviceUsage returns minutes used per day within a range.
func (h *Handlers) GetDeviceUsage(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Authentication required.")
		return
	}

	idStr := chi.URLParam(r, "id")
	devID, err := uuid.Parse(idStr)
	if err != nil {
		WriteProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "Validation Failed", "Invalid UUID.")
		return
	}

	if claims.Role != "admin" && claims.DeviceID != devID.String() {
		WriteProblem(w, r, http.StatusForbidden, CodeForbidden, "Forbidden", "Access denied.")
		return
	}

	fromTime := time.Now().AddDate(0, 0, -30)
	toTime := time.Now()

	fromParam := r.URL.Query().Get("from")
	if fromParam != "" {
		if t, err := parseDateOrRFC3339(fromParam); err == nil {
			fromTime = t
		}
	}
	toParam := r.URL.Query().Get("to")
	if toParam != "" {
		if t, err := parseDateOrRFC3339(toParam); err == nil {
			toTime = t
		}
	}

	ctx := r.Context()
	rows, err := h.db.Queries.GetUsageByDateRange(ctx, store.GetUsageByDateRangeParams{
		DeviceID:    devID,
		StartedAt:   fromTime,
		StartedAt_2: toTime,
	})
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed querying usage.")
		return
	}

	type usageItem struct {
		Date        string  `json:"date"`
		MinutesUsed float64 `json:"minutes_used"`
	}
	res := make([]usageItem, 0, len(rows))
	for _, row := range rows {
		dStr := ""
		if row.Day.Valid {
			dStr = row.Day.Time.Format("2006-01-02")
		}
		res = append(res, usageItem{
			Date:        dStr,
			MinutesUsed: float64(row.MinutesUsed),
		})
	}

	WriteJSON(w, http.StatusOK, res)

}

// GetLatestFirmware returns the latest firmware or 204 if up to date.
func (h *Handlers) GetLatestFirmware(w http.ResponseWriter, r *http.Request) {
	currentVer := r.URL.Query().Get("current")
	ctx := r.Context()

	fw, err := h.db.Queries.GetLatestFirmwareRelease(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Internal Error", "Failed retrieving firmware.")
		return
	}

	if currentVer != "" && fw.Version == currentVer {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	WriteJSON(w, http.StatusOK, fw)
}

func formatSettingsResponse(s store.DeviceSetting) map[string]any {
	return map[string]any{
		"version": s.Version,
		"button_a": map[string]string{
			"source": s.ButtonASrc,
			"target": s.ButtonADst,
		},
		"button_b": map[string]string{
			"source": s.ButtonBSrc,
			"target": s.ButtonBDst,
		},
		"volume":     s.Volume,
		"captions":   s.Captions,
		"led_level":  s.LedLevel,
		"updated_at": s.UpdatedAt.Format(time.RFC3339),
	}
}

func parseETagVersion(etag string) (int64, error) {
	clean := strings.Trim(etag, ` "W/`)
	return strconv.ParseInt(clean, 10, 64)
}

func parseDateOrRFC3339(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}
