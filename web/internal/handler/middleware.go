package handler

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/lynxlee/lynx-ollama-web/internal/model"
)

// apiKeyAuth returns a middleware that validates the API key for /api/* routes.
// Static files and health check endpoints are exempt from authentication.
func apiKeyAuth(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip auth for static files (non-API routes)
		if !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// Exempt endpoints: ping/health (for monitoring probes) and version info
		if path == "/api/ping" || path == "/api/health" || path == "/api/version" {
			next.ServeHTTP(w, r)
			return
		}

		// Auth endpoint itself is exempt (used to validate key from frontend)
		if path == "/api/auth/verify" {
			next.ServeHTTP(w, r)
			return
		}

		// Extract API key from request
		key := extractAPIKey(r)
		if key == "" {
			slog.Warn("unauthorized request: missing API key", "path", path, "remote", r.RemoteAddr)
			writeAuthError(w, http.StatusUnauthorized, "需要 API Key 认证。请在请求头中添加 X-API-Key 或在 URL 中添加 ?key= 参数。")
			return
		}

		// Constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(key), []byte(apiKey)) != 1 {
			slog.Warn("unauthorized request: invalid API key", "path", path, "remote", r.RemoteAddr)
			writeAuthError(w, http.StatusForbidden, "API Key 无效。")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// extractAPIKey extracts the API key from the request.
// Supported methods (in priority order):
//  1. Header: X-API-Key
//  2. Header: Authorization Bearer <key>
//  3. Query parameter: key (used by WebSocket connections that can't set headers)
func extractAPIKey(r *http.Request) string {
	// 1. X-API-Key header
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}

	// 2. Authorization: Bearer <key>
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}

	// 3. Query parameter
	if key := r.URL.Query().Get("key"); key != "" {
		return key
	}

	return ""
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.APIResponse{Success: false, Error: msg})
}
