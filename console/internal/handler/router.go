package handler

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/lynxlee/lynx-ollama-console/internal/config"
	"github.com/lynxlee/lynx-ollama-console/internal/service"
)

//go:embed static/*
var staticFS embed.FS

// NewRouter creates the HTTP router with all API endpoints.
func NewRouter(ollamaSvc *service.OllamaService, dockerSvc *service.DockerService, systemSvc *service.SystemService, cfg *config.Config, version string) http.Handler {
	mux := http.NewServeMux()

	api := NewAPIHandler(ollamaSvc, dockerSvc, systemSvc, cfg, version)

	// ── Auth ────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/auth/verify", api.VerifyAPIKey)

	// ── Version ────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/version", api.GetVersion)
	mux.HandleFunc("GET /api/changelog", api.GetChangelog)

	// ── Service control ─────────────────────────────────────────────
	mux.HandleFunc("GET /api/status", api.GetStatus)
	mux.HandleFunc("GET /api/status/lite", api.GetStatusLite)
	mux.HandleFunc("POST /api/service/start", api.StartService)
	mux.HandleFunc("POST /api/service/stop", api.StopService)
	mux.HandleFunc("POST /api/service/restart", api.RestartService)
	mux.HandleFunc("POST /api/service/update", api.UpdateService)

	// ── Models ──────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/models", api.ListModels)
	mux.HandleFunc("GET /api/models/running", api.ListRunningModels)
	mux.HandleFunc("GET /api/models/search", api.SearchMarketModels)
	mux.HandleFunc("POST /api/models/search/translate", api.TranslateModelDescriptions)
	mux.HandleFunc("POST /api/models/pull", api.PullModel)
	mux.HandleFunc("DELETE /api/models/{name}", api.DeleteModel)
	mux.HandleFunc("GET /api/models/{name}/info", api.ShowModel)
	mux.HandleFunc("GET /api/models/check", api.CheckModelsCompatibility)

	// ── Health & Diagnostics ────────────────────────────────────────
	mux.HandleFunc("GET /api/ping", api.Ping)
	mux.HandleFunc("GET /api/health", api.HealthCheck)
	mux.HandleFunc("GET /api/gpu", api.GetGPUInfo)
	mux.HandleFunc("GET /api/logs", api.GetLogs)

	// ── Configuration ───────────────────────────────────────────────
	mux.HandleFunc("GET /api/config", api.GetConfig)
	mux.HandleFunc("PUT /api/config", api.UpdateConfig)
	mux.HandleFunc("POST /api/optimize", api.Optimize)

	// ── Clean ───────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/clean", api.Clean)

	// ── WebSocket for logs streaming ────────────────────────────────
	mux.HandleFunc("GET /api/ws/logs", api.StreamLogs)
	mux.HandleFunc("GET /api/ws/pull", api.StreamPull)
	mux.HandleFunc("GET /api/ws/update", api.StreamUpdate)
	mux.HandleFunc("GET /api/ws/service", api.StreamServiceControl)
	mux.HandleFunc("GET /api/ws/status", api.StreamStatus)
	mux.HandleFunc("GET /api/ws/chat", api.StreamChat)

	// ── Performance Monitor ─────────────────────────────────────────
	mux.HandleFunc("GET /api/ws/perf", api.StreamPerf)
	mux.HandleFunc("GET /api/infer/events", api.GetInferenceEvents)

	// ── Chat ────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/chat/upload", api.UploadChatFile)
	mux.HandleFunc("GET /api/chat/files/{id}", api.DownloadChatFile)

	// ── Chat History ────────────────────────────────────────────────
	mux.HandleFunc("GET /api/chat/sessions", api.ListChatSessions)
	mux.HandleFunc("POST /api/chat/sessions", api.CreateChatSession)
	mux.HandleFunc("GET /api/chat/sessions/{id}", api.GetChatSession)
	mux.HandleFunc("PUT /api/chat/sessions/{id}", api.UpdateChatSessionTitle)
	mux.HandleFunc("DELETE /api/chat/sessions/{id}", api.DeleteChatSession)
	mux.HandleFunc("POST /api/chat/sessions/{id}/messages", api.SaveChatMessage)
	mux.HandleFunc("GET /api/chat/sessions/{id}/export", api.ExportChatSession)

	// ── Benchmark ───────────────────────────────────────────────────
	mux.HandleFunc("POST /api/benchmark/start", api.StartBenchmarkTask)
	mux.HandleFunc("POST /api/benchmark/stop", api.StopBenchmarkTask)
	mux.HandleFunc("GET /api/benchmark/tasks", api.GetBenchmarkTasks)
	mux.HandleFunc("GET /api/benchmark/results", api.ListBenchmarkResults)
	mux.HandleFunc("GET /api/ws/benchmark", api.StreamBenchmark)

	// ── Static files (embedded SPA) ─────────────────────────────────
	staticContent, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticContent))
	mux.Handle("/", spaHandler(fileServer))

	// Middleware chain: CORS → Auth → Router
	return withCORS(cfg.CORSOrigin, apiKeyAuth(cfg.APIKey, mux))
}

// spaHandler serves index.html for all non-API, non-file routes (SPA routing).
func spaHandler(fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the actual file
		fileServer.ServeHTTP(w, r)
	})
}

// withCORS adds CORS headers. If allowedOrigin is empty, only same-origin is allowed.
func withCORS(allowedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if allowedOrigin == "*" {
			// Development mode: allow all origins
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if allowedOrigin != "" {
			// Specific origin(s) configured
			for _, allowed := range strings.Split(allowedOrigin, ",") {
				if strings.TrimSpace(allowed) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					break
				}
			}
		} else if origin != "" {
			// Default: no CORS header → browser enforces same-origin policy
			// Only allow same-origin requests (no Access-Control-Allow-Origin header set)
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
