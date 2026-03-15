package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lynxlee/lynx-ollama-web/internal/config"
	"github.com/lynxlee/lynx-ollama-web/internal/handler"
	"github.com/lynxlee/lynx-ollama-web/internal/service"
)

// Version is set at build time via -ldflags.
var Version = "v1.4.8"

func main() {
	showVersion := flag.Bool("version", false, "Show version and exit")

	cfg := config.New()

	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "Web server listen address")
	flag.StringVar(&cfg.OllamaAPIURL, "ollama-url", cfg.OllamaAPIURL, "Ollama API base URL")
	flag.StringVar(&cfg.ProjectDir, "project-dir", cfg.ProjectDir, "Ollama project directory (contains ollama.sh)")
	flag.StringVar(&cfg.ScriptPath, "script", cfg.ScriptPath, "Path to ollama.sh")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug/info/warn/error)")
	flag.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "API key for authentication (auto-generated if empty)")
	flag.StringVar(&cfg.CORSOrigin, "cors-origin", cfg.CORSOrigin, "Allowed CORS origin (empty = same-origin only)")
	flag.Parse()

	if *showVersion {
		fmt.Println("Lynx-Ollama Web " + Version)
		os.Exit(0)
	}

	// Ensure API key exists (auto-generate if not provided)
	cfg.EnsureAPIKey()

	// Setup structured logging
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	// Validate config
	if err := cfg.Validate(); err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	// Initialize services
	ollamaSvc := service.NewOllamaService(cfg)
	dockerSvc := service.NewDockerService(cfg)
	systemSvc := service.NewSystemService(cfg)

	// Setup HTTP router
	mux := handler.NewRouter(ollamaSvc, dockerSvc, systemSvc, cfg, Version)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("starting web server", "addr", cfg.ListenAddr, "ollama_url", cfg.OllamaAPIURL, "version", Version)
		fmt.Printf("\n  🌐 Ollama Web 管理界面: http://%s  (%s)\n", cfg.ListenAddr, Version)
		fmt.Printf("  🔑 API Key: %s\n\n", cfg.APIKey)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("server stopped")
}
