package httpapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"parlex/internal/auth"
)

type claimsContextKey struct{}

// ClaimsFromContext extracts JWT claims from request context.
func ClaimsFromContext(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(claimsContextKey{}).(*auth.Claims)
	return c, ok
}

// AdminAuth validates the X-Admin-Key header against expected admin API key in constant time.
func AdminAuth(adminKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("X-Admin-Key")
			if provided == "" {
				authHdr := r.Header.Get("Authorization")
				if strings.HasPrefix(authHdr, "Bearer ") {
					provided = strings.TrimPrefix(authHdr, "Bearer ")
				}
			}

			if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(adminKey)) != 1 {
				WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Invalid or missing admin key.")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireToken validates Bearer JWT token and ensures the device is not revoked.
func RequireToken(tm *auth.TokenManager, rev *auth.RevocationRegistry, requiredRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHdr := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHdr, "Bearer ") {
				WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Missing Bearer token.")
				return
			}

			tokenStr := strings.TrimPrefix(authHdr, "Bearer ")
			claims, err := tm.ValidateToken(tokenStr)
			if err != nil {
				WriteProblem(w, r, http.StatusUnauthorized, CodeUnauthorized, "Unauthorized", "Invalid or expired token.")
				return
			}

			if requiredRole != "" && claims.Role != requiredRole && claims.Role != "admin" {
				WriteProblem(w, r, http.StatusForbidden, CodeForbidden, "Forbidden", "Token does not have required permissions.")
				return
			}

			devUUID, err := uuid.Parse(claims.DeviceID)
			if err == nil && rev != nil && rev.IsRevoked(devUUID) {
				WriteProblem(w, r, http.StatusForbidden, CodeDeviceRevoked, "Device Revoked", "This device has been revoked.")
				return
			}

			ctx := context.WithValue(r.Context(), claimsContextKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
