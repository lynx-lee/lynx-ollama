package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all configuration for the web management server.
type Config struct {
	ListenAddr   string // Web server listen address (e.g. "0.0.0.0:8080")
	OllamaAPIURL string // Ollama API base URL (e.g. "http://localhost:11434")
	ProjectDir   string // Path to the ollama project directory
	ScriptPath   string // Path to ollama.sh
	LogLevel     string // Log level: debug, info, warn, error
	APIKey       string // API key for authentication (empty = auto-generate)
	CORSOrigin   string // Allowed CORS origin ("*" for all, empty = same-origin only)
}

// New creates a Config with defaults, reading from environment variables.
func New() *Config {
	projectDir := envOrDefault("OLLAMA_PROJECT_DIR", "/opt/ai/ollama")
	return &Config{
		ListenAddr:   envOrDefault("WEB_LISTEN_ADDR", "0.0.0.0:8080"),
		OllamaAPIURL: envOrDefault("OLLAMA_API_URL", "http://localhost:11434"),
		ProjectDir:   projectDir,
		ScriptPath:   envOrDefault("OLLAMA_SCRIPT_PATH", filepath.Join(projectDir, "ollama.sh")),
		LogLevel:     envOrDefault("WEB_LOG_LEVEL", "info"),
		APIKey:       os.Getenv("WEB_API_KEY"),
		CORSOrigin:   os.Getenv("WEB_CORS_ORIGIN"),
	}
}

// EnsureAPIKey generates a random API key if none is configured.
func (c *Config) EnsureAPIKey() {
	if c.APIKey != "" {
		return
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use a simple default (should never happen)
		c.APIKey = "ollama-web-default-key"
		return
	}
	c.APIKey = "olw_" + hex.EncodeToString(b)
}

// Validate checks that the configuration is valid.
func (c *Config) Validate() error {
	if c.ProjectDir == "" {
		return fmt.Errorf("project directory is required")
	}
	if _, err := os.Stat(c.ProjectDir); os.IsNotExist(err) {
		return fmt.Errorf("project directory does not exist: %s", c.ProjectDir)
	}
	if _, err := os.Stat(c.ScriptPath); os.IsNotExist(err) {
		return fmt.Errorf("ollama.sh not found: %s", c.ScriptPath)
	}
	return nil
}

// EnvFilePath returns the path to .env file.
func (c *Config) EnvFilePath() string {
	return filepath.Join(c.ProjectDir, ".env")
}

// ComposeFilePath returns the path to docker-compose.yaml.
func (c *Config) ComposeFilePath() string {
	return filepath.Join(c.ProjectDir, "docker-compose.yaml")
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
