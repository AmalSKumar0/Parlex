package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"parlex/internal/auth"

	"parlex/internal/config"
	"parlex/internal/httpapi"
	"parlex/internal/observability"
	"parlex/internal/provider"
	"parlex/internal/store"
)

const (
	SubprotocolV1     = "translator.v1"
	CloseCodeReplaced = 4001
)

type Gateway struct {
	cfg        *config.Config
	db         *store.DB
	tm         *auth.TokenManager
	rev        *auth.RevocationRegistry
	provRouter *provider.Router
	metrics    *observability.Metrics
	logger     *slog.Logger

	mu     sync.RWMutex
	actors map[uuid.UUID]*Actor
}

func NewGateway(
	cfg *config.Config,
	db *store.DB,
	tm *auth.TokenManager,
	rev *auth.RevocationRegistry,
	provRouter *provider.Router,
	metrics *observability.Metrics,
	logger *slog.Logger,
) *Gateway {
	return &Gateway{
		cfg:        cfg,
		db:         db,
		tm:         tm,
		rev:        rev,
		provRouter: provRouter,
		metrics:    metrics,
		logger:     logger,
		actors:     make(map[uuid.UUID]*Actor),
	}
}

// ServeHTTP handles the WebSocket handshake and actor lifecycle.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Validate Authorization
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Bearer ") {
		httpapi.WriteProblem(w, r, http.StatusUnauthorized, httpapi.CodeUnauthorized, "Unauthorized", "Missing Bearer stream token.")
		return
	}

	tokenStr := strings.TrimPrefix(authHdr, "Bearer ")
	claims, err := g.tm.ValidateToken(tokenStr)
	if err != nil {
		httpapi.WriteProblem(w, r, http.StatusUnauthorized, httpapi.CodeUnauthorized, "Unauthorized", "Invalid or expired stream token.")
		return
	}

	if claims.Role != "device" {
		httpapi.WriteProblem(w, r, http.StatusForbidden, httpapi.CodeForbidden, "Forbidden", "Only device stream tokens may connect to /v1/stream.")
		return
	}

	devID, err := uuid.Parse(claims.DeviceID)
	if err != nil {
		httpapi.WriteProblem(w, r, http.StatusBadRequest, httpapi.CodeValidationFailed, "Validation Failed", "Invalid device ID UUID.")
		return
	}

	if g.rev.IsRevoked(devID) {
		httpapi.WriteProblem(w, r, http.StatusForbidden, httpapi.CodeDeviceRevoked, "Device Revoked", "This device has been revoked.")
		return
	}

	// Upgrade WebSocket
	opts := &websocket.AcceptOptions{
		Subprotocols:   []string{SubprotocolV1},
		OriginPatterns: []string{"*"},
	}

	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		g.logger.Warn("websocket upgrade failed", slog.Any("error", err))
		return
	}

	if conn.Subprotocol() != SubprotocolV1 {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol translator.v1 required")
		return
	}

	// Fetch current settings
	ctx := r.Context()
	var currentSettings store.DeviceSetting
	if g.db != nil {

		settings, err := g.db.Queries.GetDeviceSettings(ctx, devID)
		if err == nil {
			currentSettings = settings
		}
	}
	if currentSettings.ButtonASrc == "" {
		currentSettings = store.DeviceSetting{
			DeviceID:   devID,
			Version:    1,
			ButtonASrc: "en",
			ButtonADst: "es",
			ButtonBSrc: "es",
			ButtonBDst: "en",
			Volume:     70,
			Captions:   true,
			LedLevel:   50,
		}
	}

	// Single connection per device: close any existing connection with code 4001
	g.mu.Lock()
	if oldActor, exists := g.actors[devID]; exists {
		g.logger.Info("replacing existing connection", slog.String("device_id", devID.String()))
		oldActor.Close("connection replaced")
	}

	actor := NewActor(devID, g.cfg, g.db, g.provRouter, g.metrics, g.logger, conn, currentSettings)
	g.actors[devID] = actor
	g.mu.Unlock()

	actor.Start()

	// Update last seen
	if g.db != nil {
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = g.db.Queries.UpdateDeviceLastSeen(c, store.UpdateDeviceLastSeenParams{
				ID: devID,
			})
		}()
	}

	// Watch for revocation in background
	revCh, cancelRev := g.rev.Subscribe(devID)
	defer cancelRev()

	// Heartbeat pinger
	go g.heartbeatLoop(actor, conn, devID)

	// Block until connection teardown or revocation
	select {
	case <-actor.ctx.Done():
	case <-revCh:
		g.logger.Warn("device revoked during active stream, closing", slog.String("device_id", devID.String()))
		byeMsg, _ := json.Marshal(ByeMessage{Type: TypeBye, Reason: "device_revoked"})
		actor.sendText(byeMsg)
		time.Sleep(50 * time.Millisecond)
		actor.Close("device_revoked")
	}

	g.mu.Lock()
	if current, exists := g.actors[devID]; exists && current == actor {
		delete(g.actors, devID)
	}
	g.mu.Unlock()
}

func (g *Gateway) heartbeatLoop(actor *Actor, conn *websocket.Conn, devID uuid.UUID) {
	pingInterval := g.cfg.WSPingInterval()
	pongTimeout := g.cfg.WSPongTimeout()

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-actor.ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(actor.ctx, pongTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				g.logger.Warn("websocket ping timeout, closing", slog.String("device_id", devID.String()), slog.Any("error", err))
				actor.Close("ping_timeout")
				return
			}
		}
	}
}

// DeviceStatusProvider implementation

func (g *Gateway) IsOnline(deviceID uuid.UUID) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, exists := g.actors[deviceID]
	return exists
}

func (g *Gateway) TurnState(deviceID uuid.UUID) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if actor, exists := g.actors[deviceID]; exists {
		return string(actor.State())
	}
	return "IDLE"
}

func (g *Gateway) OnSettingsUpdated(deviceID uuid.UUID, settings store.DeviceSetting) {
	g.mu.RLock()
	actor, exists := g.actors[deviceID]
	g.mu.RUnlock()
	if exists && actor != nil {
		actor.PushSettingsUpdate(settings)
	}
}
