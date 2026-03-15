package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lynxlee/lynx-ollama-web/internal/config"
	"github.com/lynxlee/lynx-ollama-web/internal/model"
)

// OllamaService interacts with the Ollama API.
type OllamaService struct {
	cfg    *config.Config
	client *http.Client
}

// NewOllamaService creates a new OllamaService.
func NewOllamaService(cfg *config.Config) *OllamaService {
	return &OllamaService{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// IsAPIReady checks if the Ollama API is reachable.
func (s *OllamaService) IsAPIReady() bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(s.cfg.OllamaAPIURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// GetVersion returns the Ollama version.
func (s *OllamaService) GetVersion() (string, error) {
	resp, err := s.client.Get(s.cfg.OllamaAPIURL + "/api/version")
	if err != nil {
		return "", fmt.Errorf("failed to get version: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode version: %w", err)
	}
	return result.Version, nil
}

// ollamaTagsResponse is the raw response from /api/tags.
type ollamaTagsResponse struct {
	Models []struct {
		Name       string    `json:"name"`
		Size       int64     `json:"size"`
		Digest     string    `json:"digest"`
		ModifiedAt time.Time `json:"modified_at"`
		Details    struct {
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

// ListModels returns all downloaded models.
func (s *OllamaService) ListModels() ([]model.ModelInfo, error) {
	resp, err := s.client.Get(s.cfg.OllamaAPIURL + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}
	defer resp.Body.Close()

	var raw ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode models: %w", err)
	}

	models := make([]model.ModelInfo, 0, len(raw.Models))
	for _, m := range raw.Models {
		models = append(models, model.ModelInfo{
			Name:         m.Name,
			Size:         m.Size,
			SizeHuman:    formatBytes(m.Size),
			Digest:       m.Digest,
			ModifiedAt:   m.ModifiedAt,
			Family:       m.Details.Family,
			Parameters:   m.Details.ParameterSize,
			Quantization: m.Details.QuantizationLevel,
		})
	}
	return models, nil
}

// ollamaPsResponse is the raw response from /api/ps.
type ollamaPsResponse struct {
	Models []struct {
		Name      string    `json:"name"`
		Size      int64     `json:"size"`
		Digest    string    `json:"digest"`
		ExpiresAt time.Time `json:"expires_at"`
		SizeVRAM  int64     `json:"size_vram"`
	} `json:"models"`
}

// ListRunningModels returns currently loaded/running models.
func (s *OllamaService) ListRunningModels() ([]model.RunningModel, error) {
	resp, err := s.client.Get(s.cfg.OllamaAPIURL + "/api/ps")
	if err != nil {
		return nil, fmt.Errorf("failed to list running models: %w", err)
	}
	defer resp.Body.Close()

	var raw ollamaPsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode running models: %w", err)
	}

	running := make([]model.RunningModel, 0, len(raw.Models))
	for _, m := range raw.Models {
		running = append(running, model.RunningModel{
			Name:      m.Name,
			Size:      m.Size,
			SizeHuman: formatBytes(m.Size),
			Digest:    m.Digest,
			ExpiresAt: m.ExpiresAt,
			SizeVRAM:  m.SizeVRAM,
			VRAMHuman: formatBytes(m.SizeVRAM),
		})
	}
	return running, nil
}

// PullModel sends a pull request and returns a reader for streaming progress.
func (s *OllamaService) PullModel(name string) (io.ReadCloser, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"name":   name,
		"stream": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pull request: %w", err)
	}
	resp, err := s.client.Post(
		s.cfg.OllamaAPIURL+"/api/pull",
		"application/json",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to pull model: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pull failed (status %d): %s", resp.StatusCode, string(body))
	}
	return resp.Body, nil
}

// DeleteModel removes a model.
func (s *OllamaService) DeleteModel(name string) error {
	payload, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return fmt.Errorf("failed to marshal delete request: %w", err)
	}
	req, err := http.NewRequest(http.MethodDelete, s.cfg.OllamaAPIURL+"/api/delete", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("failed to create delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// GenerateChat sends a chat message for testing/benchmarking.
func (s *OllamaService) GenerateChat(modelName, message string) (map[string]interface{}, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "user", "content": message},
		},
		"stream": false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Post(
		s.cfg.OllamaAPIURL+"/api/chat",
		"application/json",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return nil, fmt.Errorf("chat request failed: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode chat response: %w", err)
	}
	return result, nil
}

// ShowModel returns detailed info about a model.
func (s *OllamaService) ShowModel(name string) (map[string]interface{}, error) {
	payload, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal show request: %w", err)
	}
	resp, err := s.client.Post(
		s.cfg.OllamaAPIURL+"/api/show",
		"application/json",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return nil, fmt.Errorf("show model failed: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode model info: %w", err)
	}
	return result, nil
}

func formatBytes(b int64) string {
	const (
		GiB = 1024 * 1024 * 1024
		MiB = 1024 * 1024
	)
	switch {
	case b >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(GiB))
	case b >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(MiB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func init() {
	// Suppress unused import warning
	_ = slog.Default
}
