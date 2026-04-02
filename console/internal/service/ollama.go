package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lynxlee/lynx-ollama-console/internal/config"
	"github.com/lynxlee/lynx-ollama-console/internal/model"
)

// OllamaService interacts with the Ollama API.
type OllamaService struct {
	cfg    *config.Config
	client *http.Client

	// Fast HTTP client for API readiness probes (short timeout, connection reuse).
	probeClient *http.Client

	// Short-lived cache for API readiness to avoid redundant probes within
	// the same polling cycle (multiple handlers call IsAPIReady concurrently).
	apiReadyCache    bool
	apiReadyCacheAt  time.Time
	apiReadyCacheTTL time.Duration

	// Version cache — Ollama version rarely changes, no need to query every 5s.
	versionCache    string
	versionCacheAt  time.Time
	versionCacheTTL time.Duration

	// Persistent metadata store (SQLite) — model capabilities + translation cache.
	metaStore *MetadataStore
}

// NewOllamaService creates a new OllamaService.
func NewOllamaService(cfg *config.Config, metaStore *MetadataStore) *OllamaService {
	return &OllamaService{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		probeClient: &http.Client{
			Timeout: 3 * time.Second,
		},
		apiReadyCacheTTL: 5 * time.Second,
		versionCacheTTL:  1 * time.Hour,
		metaStore:        metaStore,
	}
}

// MetaStore returns the metadata store for external access.
func (s *OllamaService) MetaStore() *MetadataStore { return s.metaStore }

// IsAPIReady checks if the Ollama API is reachable.
// Results are cached for a short TTL to avoid redundant probes when called
// multiple times within the same polling cycle.
func (s *OllamaService) IsAPIReady() bool {
	// Return cached result if still fresh
	if time.Since(s.apiReadyCacheAt) < s.apiReadyCacheTTL {
		return s.apiReadyCache
	}

	resp, err := s.probeClient.Get(s.cfg.OllamaAPIURL)
	if err != nil {
		s.apiReadyCache = false
		s.apiReadyCacheAt = time.Now()
		return false
	}
	defer resp.Body.Close()

	ready := resp.StatusCode == http.StatusOK
	s.apiReadyCache = ready
	s.apiReadyCacheAt = time.Now()
	return ready
}

// GetVersion returns the Ollama version (cached for 60s).
func (s *OllamaService) GetVersion() (string, error) {
	if s.versionCache != "" && time.Since(s.versionCacheAt) < s.versionCacheTTL {
		return s.versionCache, nil
	}

	resp, err := s.client.Get(s.cfg.OllamaAPIURL + "/api/version")
	if err != nil {
		return s.versionCache, fmt.Errorf("failed to get version: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return s.versionCache, fmt.Errorf("failed to decode version: %w", err)
	}
	s.versionCache = result.Version
	s.versionCacheAt = time.Now()
	return result.Version, nil
}

// InvalidateVersionCache forces the next GetVersion call to query the API.
func (s *OllamaService) InvalidateVersionCache() {
	s.versionCacheAt = time.Time{}
}

// GetLatestVersion queries the GitHub API for the latest Ollama release version.
func (s *OllamaService) GetLatestVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/ollama/ollama/releases/latest", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "lynx-ollama-console/1.0")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to query GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to decode GitHub response: %w", err)
	}
	// Strip leading 'v' if present (e.g. "v0.19.0" → "0.19.0")
	ver := strings.TrimPrefix(release.TagName, "v")
	return ver, nil
}
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

// ListModels returns all downloaded models with capabilities enrichment.
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

	// Load all cached metadata in one batch
	allMeta := s.metaStore.GetAllModelMeta()

	models := make([]model.ModelInfo, 0, len(raw.Models))
	var needEnrich []string // model names that need capability detection
	for _, m := range raw.Models {
		info := model.ModelInfo{
			Name:         m.Name,
			Size:         m.Size,
			SizeHuman:    formatBytes(m.Size),
			Digest:       m.Digest,
			ModifiedAt:   m.ModifiedAt,
			Family:       m.Details.Family,
			Parameters:   m.Details.ParameterSize,
			Quantization: m.Details.QuantizationLevel,
		}

		// Try to get capabilities from metadata store
		baseName := m.Name
		if idx := strings.LastIndex(m.Name, ":"); idx > 0 {
			baseName = m.Name[:idx]
		}
		if meta, ok := allMeta[m.Name]; ok {
			info.Capabilities = meta.Capabilities
			info.ModelType = meta.ModelType
		} else if meta, ok := allMeta[baseName]; ok {
			info.Capabilities = meta.Capabilities
			info.ModelType = meta.ModelType
		} else {
			needEnrich = append(needEnrich, m.Name)
			caps, mt := InferCapabilitiesFromName(m.Name)
			info.Capabilities = caps
			info.ModelType = mt
		}

		models = append(models, info)
	}

	// Async: enrich unknown models via /api/show (non-blocking)
	if len(needEnrich) > 0 {
		go s.enrichModelCapabilities(needEnrich)
	}

	return models, nil
}

// enrichModelCapabilities queries /api/show for models without cached capabilities.
// Only operates on model names (not slice indices) to avoid data races with the caller.
func (s *OllamaService) enrichModelCapabilities(names []string) {
	for _, name := range names {
		info, err := s.ShowModel(name)
		if err != nil {
			continue
		}
		caps, modelType := InferCapabilitiesFromShowModel(info)
		if len(caps) == 0 {
			caps, modelType = InferCapabilitiesFromName(name)
		}
		s.metaStore.SetModelMeta(name, caps, modelType, "show")
		if idx := strings.LastIndex(name, ":"); idx > 0 {
			s.metaStore.SetModelMeta(name[:idx], caps, modelType, "show")
		}
	}
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
// Uses a separate client without timeout since model downloads can take minutes/hours.
func (s *OllamaService) PullModel(name string) (io.ReadCloser, error) {
	payload, err := json.Marshal(map[string]any{
		"name":   name,
		"stream": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pull request: %w", err)
	}
	// No timeout for streaming downloads — models can be tens of GB.
	pullClient := &http.Client{}
	resp, err := pullClient.Post(
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
func (s *OllamaService) GenerateChat(modelName, message string) (map[string]any, error) {
	return s.GenerateChatWithContext(context.Background(), modelName, message)
}

// GenerateChatWithContext sends a chat message with context support for cancellation/timeout.
func (s *OllamaService) GenerateChatWithContext(ctx context.Context, modelName, message string) (map[string]any, error) {
	payload, err := json.Marshal(map[string]any{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "user", "content": message},
		},
		"stream": false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.cfg.OllamaAPIURL+"/api/chat", strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("failed to create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("chat request failed: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode chat response: %w", err)
	}
	return result, nil
}

// ShowModel returns detailed info about a model.
func (s *OllamaService) ShowModel(name string) (map[string]any, error) {
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

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode model info: %w", err)
	}
	// Ollama returns 500 with {"error":"..."} for incompatible models
	if resp.StatusCode != http.StatusOK {
		if errMsg, ok := result["error"].(string); ok {
			return nil, fmt.Errorf("%s", errMsg)
		}
		return nil, fmt.Errorf("show model failed (status %d)", resp.StatusCode)
	}
	return result, nil
}

// IncompatibleModel holds info about a model that needs re-downloading.
type IncompatibleModel struct {
	Name    string `json:"name"`
	Error   string `json:"error"`
	SizeHuman string `json:"size_human"`
}

// CheckModelsCompatibility probes each downloaded model via /api/show and returns
// those that are no longer compatible with the current Ollama version.
func (s *OllamaService) CheckModelsCompatibility() ([]IncompatibleModel, error) {
	models, err := s.ListModels()
	if err != nil {
		return nil, err
	}

	var incompatible []IncompatibleModel
	for _, m := range models {
		_, err := s.ShowModel(m.Name)
		if err != nil {
			errStr := err.Error()
			// Only flag compatibility errors, not network/timeout errors
			if strings.Contains(errStr, "no longer compatible") ||
				strings.Contains(errStr, "replaced by") ||
				strings.Contains(errStr, "unsupported model") {
				incompatible = append(incompatible, IncompatibleModel{
					Name:      m.Name,
					Error:     errStr,
					SizeHuman: m.SizeHuman,
				})
			}
		}
	}
	return incompatible, nil
}

// SearchModels searches the Ollama website for models by scraping all pages.
// It fetches pages sequentially until "No models found" or an empty result is encountered.
// Returns raw English results without translation (translation is done asynchronously via a separate API).
// Parameters: query (search term), category (vision/tools/thinking/embedding/code/cloud), sort (popular/newest).
func (s *OllamaService) SearchModels(query, category, sort string) (*model.MarketSearchResult, error) {
	fetchClient := &http.Client{Timeout: 15 * time.Second}

	var allModels []model.MarketModel
	seen := make(map[string]bool)
	maxPages := 50 // safety limit to prevent infinite loops

	for page := 1; page <= maxPages; page++ {
		// Build URL with pagination
		params := []string{}
		if query != "" {
			params = append(params, "q="+query)
		}
		if category != "" {
			params = append(params, "c="+category)
		}
		if sort == "newest" {
			params = append(params, "o=newest")
		}
		params = append(params, fmt.Sprintf("page=%d", page))

		url := "https://ollama.com/search"
		if len(params) > 0 {
			url += "?" + strings.Join(params, "&")
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request for page %d: %w", page, err)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; lynx-ollama-console/1.0)")
		req.Header.Set("Accept", "text/html")

		resp, err := fetchClient.Do(req)
		if err != nil {
			// Network error on subsequent pages — return what we have so far
			if len(allModels) > 0 {
				break
			}
			return nil, fmt.Errorf("failed to fetch ollama.com page %d: %w", page, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if len(allModels) > 0 {
				break
			}
			return nil, fmt.Errorf("failed to read response body for page %d: %w", page, err)
		}

		if resp.StatusCode != http.StatusOK {
			if len(allModels) > 0 {
				break
			}
			return nil, fmt.Errorf("ollama.com returned status %d for page %d", resp.StatusCode, page)
		}

		html := string(body)

		// Check for "No models found" — indicates we've gone past the last page
		if strings.Contains(html, "No models found") {
			break
		}

		models := parseOllamaSearchHTML(html)
		if len(models) == 0 {
			break
		}

		// Deduplicate across pages
		for _, m := range models {
			if !seen[m.Name] {
				seen[m.Name] = true
				allModels = append(allModels, m)
			}
		}
	}

	// Persist market model capabilities to metadata store
	for _, m := range allModels {
		if len(m.Tags) > 0 {
			modelType := "chat"
			for _, t := range m.Tags {
				switch strings.ToLower(t) {
				case "vision":
					modelType = "vision"
				case "embedding":
					modelType = "embedding"
				}
			}
			s.metaStore.SetModelMeta(m.Name, m.Tags, modelType, "market")
		}
	}

	return &model.MarketSearchResult{
		Models: allModels,
		Query:  query,
		Total:  len(allModels),
	}, nil
}

// TranslateDescriptions translates the given model descriptions to Chinese using the local Ollama model.
// Optimized strategy:
//  1. Check SQLite cache first — return cached translations immediately.
//  2. Collect uncached items and pack them into a single JSON payload.
//  3. Call the LLM once with a batch translation prompt (JSON in → JSON out).
//  4. Parse the returned JSON and populate results + persistent cache.
func (s *OllamaService) TranslateDescriptions(items []model.TranslateRequest) []model.TranslateResponse {
	results := make([]model.TranslateResponse, len(items))
	for i, item := range items {
		results[i] = model.TranslateResponse{Name: item.Name, Description: item.Description}
	}

	if len(items) == 0 {
		return results
	}

	// Phase 1: Resolve from persistent cache — collect indices of uncached items
	originals := make([]string, len(items))
	for i, item := range items {
		originals[i] = item.Description
	}
	cachedTranslations := s.metaStore.GetTranslationBatch(originals)

	var uncached []uncachedItem
	for i, item := range items {
		desc := item.Description
		if desc == "" || len(desc) < 10 || containsChinese(desc) {
			continue
		}
		if cached, ok := cachedTranslations[desc]; ok && cached != "" {
			results[i].Description = cached
			continue
		}
		uncached = append(uncached, uncachedItem{index: i, name: item.Name, desc: desc})
	}

	if len(uncached) == 0 {
		slog.Info("translation batch fully served from cache", "total", len(items))
		return results
	}

	slog.Info("translation batch", "total", len(items), "cached", len(items)-len(uncached), "to_translate", len(uncached))

	// Phase 2: Check API readiness and find translation model
	if !s.IsAPIReady() {
		return results
	}

	translationModel := s.findTranslationModel()
	if translationModel == "" {
		slog.Warn("no suitable translation model found")
		return results
	}

	// Phase 3: Batch translate uncached items via single LLM call
	translated := s.batchTranslate(translationModel, uncached)

	// Phase 4: Populate results and persistent cache
	newTranslations := make(map[string]string)
	for _, uc := range uncached {
		if t, ok := translated[uc.name]; ok && t != "" && t != uc.desc {
			results[uc.index].Description = t
			newTranslations[uc.desc] = t
		}
	}
	if len(newTranslations) > 0 {
		s.metaStore.SetTranslationBatch(newTranslations)
	}

	return results
}

// batchTranslate sends all uncached descriptions to the LLM in a single JSON-based request.
// The prompt instructs the model to accept a JSON object (name→description) and return
// a JSON object with the same keys but translated values.
// Falls back to individual translations if batch JSON parsing fails.
func (s *OllamaService) batchTranslate(modelName string, items []uncachedItem) map[string]string {
	result := make(map[string]string)

	// Build input JSON: {"model_name": "English description", ...}
	inputMap := make(map[string]string, len(items))
	for _, item := range items {
		inputMap[item.name] = item.desc
	}

	inputJSON, err := json.Marshal(inputMap)
	if err != nil {
		slog.Error("failed to marshal translation input", "error", err)
		return result
	}

	// Longer timeout: batch translation may take longer than single item
	translateClient := &http.Client{Timeout: 180 * time.Second}

	systemPrompt := `你是翻译助手。用户会给出一个 JSON 对象，其中每个键是模型名称，值是该模型的英文描述。
请将所有值翻译为简洁流畅的中文，保持键不变，只输出翻译后的 JSON 对象。

要求：
1. 只输出 JSON，不要任何解释、前缀或 markdown 代码块标记
2. 保持所有键（模型名称）完全不变
3. 翻译要简洁准确，不超过原文长度
4. 不要添加任何额外的键或内容`

	payload, err := json.Marshal(map[string]any{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": string(inputJSON)},
		},
		"stream":  false,
		"think":   false,
		"options": map[string]any{"temperature": 0.1, "num_predict": 4096},
	})
	if err != nil {
		slog.Error("failed to marshal batch translation payload", "error", err)
		return result
	}

	req, err := http.NewRequest(http.MethodPost, s.cfg.OllamaAPIURL+"/api/chat", strings.NewReader(string(payload)))
	if err != nil {
		return result
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := translateClient.Do(req)
	if err != nil {
		slog.Warn("batch translation request failed", "error", err)
		return s.fallbackIndividualTranslate(translateClient, modelName, items)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("batch translation returned non-200", "status", resp.StatusCode)
		return s.fallbackIndividualTranslate(translateClient, modelName, items)
	}

	var chatResp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		slog.Warn("failed to decode batch translation response", "error", err)
		return s.fallbackIndividualTranslate(translateClient, modelName, items)
	}

	content := strings.TrimSpace(chatResp.Message.Content)

	// Clean up possible think tags
	if idx := strings.Index(content, "</think>"); idx >= 0 {
		content = strings.TrimSpace(content[idx+len("</think>"):])
	}

	// Strip markdown code block wrappers if present (```json ... ```)
	content = stripMarkdownCodeBlock(content)

	// Parse the JSON response
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		slog.Warn("failed to parse batch translation JSON, falling back to individual", "error", err, "content_preview", truncateStr(content, 200))
		return s.fallbackIndividualTranslate(translateClient, modelName, items)
	}

	slog.Info("batch translation completed", "model", modelName, "items", len(items), "translated", len(result))
	return result
}

// uncachedItem represents a translation item that was not found in cache.
type uncachedItem struct {
	index int
	name  string
	desc  string
}

// fallbackIndividualTranslate translates items one by one when batch translation fails.
// This ensures graceful degradation — users still get translations even if the batch
// JSON parsing fails (e.g. model outputs non-standard JSON).
func (s *OllamaService) fallbackIndividualTranslate(client *http.Client, modelName string, items []uncachedItem) map[string]string {
	slog.Info("falling back to individual translation", "items", len(items))
	result := make(map[string]string, len(items))

	consecutiveFailures := 0
	maxConsecutiveFailures := 3

	for _, item := range items {
		if consecutiveFailures >= maxConsecutiveFailures {
			break
		}
		translated := s.ollamaTranslate(client, modelName, item.desc)
		if translated != "" && translated != item.desc {
			result[item.name] = translated
			consecutiveFailures = 0
		} else {
			consecutiveFailures++
		}
	}

	return result
}

// stripMarkdownCodeBlock removes ```json ... ``` or ``` ... ``` wrappers.
func stripMarkdownCodeBlock(s string) string {
	s = strings.TrimSpace(s)
	// Handle ```json\n...\n```
	if strings.HasPrefix(s, "```") {
		// Find the end of the first line (skip ```json or ```)
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		// Remove trailing ```
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

// truncateStr truncates a string to maxLen characters for logging.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// parseOllamaSearchHTML extracts model info from ollama.com search HTML using x-test-* attributes.
func parseOllamaSearchHTML(html string) []model.MarketModel {
	var models []model.MarketModel

	// Split by model cards: <li x-test-model
	cards := strings.Split(html, "<li x-test-model")
	if len(cards) < 2 {
		// Try alternative: <li class containing "model"
		return models
	}
	cards = cards[1:] // skip before first match

	seen := make(map[string]bool)

	for _, card := range cards {
		m := model.MarketModel{}

		// Extract name: <span x-test-search-response-title...>name</span>
		name := extractTagContent(card, "x-test-search-response-title")
		if name == "" {
			continue
		}
		m.Name = strings.TrimSpace(name)
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true

		// Extract capability tags (vision/tools/thinking/embedding/code/cloud)
		m.Tags = extractAllTagContents(card, "x-test-capability")

		// Extract parameter sizes (7b/14b/70b/0.6b/350m etc.)
		m.Sizes = extractAllTagContents(card, "x-test-size")

		// Extract pull count
		m.Pulls = extractTagContent(card, "x-test-pull-count")

		// Extract updated time
		m.Updated = extractTagContent(card, "x-test-updated")

		// Extract description from <p> tags (find reasonably long text)
		m.Description = extractDescription(card)

		models = append(models, m)
	}

	return models
}

// extractTagContent extracts the first text content from a tag with the given attribute.
func extractTagContent(html, attr string) string {
	// Find <span attr...>content</span>
	idx := strings.Index(html, attr)
	if idx < 0 {
		return ""
	}
	// Find closing > of the opening tag
	rest := html[idx:]
	gtIdx := strings.Index(rest, ">")
	if gtIdx < 0 {
		return ""
	}
	rest = rest[gtIdx+1:]
	// Find </span> or </
	endIdx := strings.Index(rest, "</")
	if endIdx < 0 {
		return ""
	}
	content := strings.TrimSpace(rest[:endIdx])
	// Unescape basic HTML entities
	content = htmlUnescape(content)
	return content
}

// extractAllTagContents extracts all text contents from tags with the given attribute.
func extractAllTagContents(html, attr string) []string {
	var results []string
	seen := make(map[string]bool)
	remaining := html
	for {
		idx := strings.Index(remaining, attr)
		if idx < 0 {
			break
		}
		remaining = remaining[idx:]
		gtIdx := strings.Index(remaining, ">")
		if gtIdx < 0 {
			break
		}
		remaining = remaining[gtIdx+1:]
		endIdx := strings.Index(remaining, "</")
		if endIdx < 0 {
			break
		}
		content := strings.TrimSpace(remaining[:endIdx])
		content = htmlUnescape(content)
		lower := strings.ToLower(content)
		if content != "" && !seen[lower] {
			results = append(results, content)
			seen[lower] = true
		}
	}
	return results
}

// extractDescription tries to find a model description from the card HTML.
func extractDescription(card string) string {
	// Look for <p ...>long text content</p>
	remaining := card
	for {
		pIdx := strings.Index(remaining, "<p")
		if pIdx < 0 {
			break
		}
		remaining = remaining[pIdx:]
		gtIdx := strings.Index(remaining, ">")
		if gtIdx < 0 {
			break
		}
		remaining = remaining[gtIdx+1:]
		endIdx := strings.Index(remaining, "</p>")
		if endIdx < 0 {
			endIdx = strings.Index(remaining, "</P>")
		}
		if endIdx < 0 {
			break
		}
		text := strings.TrimSpace(remaining[:endIdx])
		// Strip any inner HTML tags
		text = stripHTMLTags(text)
		text = htmlUnescape(text)
		if len(text) >= 20 {
			return text
		}
	}

	// Fallback: find any long text block between tags
	remaining = card
	for {
		gtIdx := strings.Index(remaining, ">")
		if gtIdx < 0 {
			break
		}
		remaining = remaining[gtIdx+1:]
		ltIdx := strings.Index(remaining, "<")
		if ltIdx < 0 {
			break
		}
		text := strings.TrimSpace(remaining[:ltIdx])
		text = htmlUnescape(text)
		if len(text) >= 25 {
			return text
		}
	}
	return ""
}

// stripHTMLTags removes all HTML tags from a string.
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, ch := range s {
		if ch == '<' {
			inTag = true
			continue
		}
		if ch == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(ch)
		}
	}
	return result.String()
}

// htmlUnescape replaces common HTML entities.
func htmlUnescape(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&#x27;", "'")
	s = strings.ReplaceAll(s, "&#x2F;", "/")
	return s
}

// isTranslationCapableModel checks if a model name suggests it can handle text translation tasks.
// Returns false for embedding models, vision-only models, and other non-text-generation models.
func isTranslationCapableModel(name string) bool {
	lower := strings.ToLower(name)

	// Exclude: embedding models
	excludeKeywords := []string{
		"embed", "nomic-embed", "bge-", "mxbai-embed", "all-minilm",
		"snowflake-arctic-embed",
	}
	for _, kw := range excludeKeywords {
		if strings.Contains(lower, kw) {
			return false
		}
	}

	return true
}

// isPreferredTranslationModel checks if a model is particularly good at translation
// (multilingual LLMs that handle Chinese well).
func isPreferredTranslationModel(name string) bool {
	lower := strings.ToLower(name)
	preferredKeywords := []string{
		"qwen", "glm", "deepseek", "yi-", "internlm", "baichuan",
		"llama", "gemma", "mistral", "phi", "nemotron",
	}
	for _, kw := range preferredKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// findTranslationModel finds the best model for translation tasks.
// Priority order:
//  1. Currently running model that supports translation (already loaded in memory = fastest response)
//  2. Preferred translation model from downloaded list (qwen, deepseek, llama, etc.)
//  3. Any non-embedding downloaded model as fallback
func (s *OllamaService) findTranslationModel() string {
	// === Phase 1: Check currently running models (loaded in VRAM, instant response) ===
	runningModels, err := s.ListRunningModels()
	if err == nil && len(runningModels) > 0 {
		// First: find a preferred running model (qwen, deepseek, llama, etc.)
		for _, m := range runningModels {
			if isTranslationCapableModel(m.Name) && isPreferredTranslationModel(m.Name) {
				return m.Name
			}
		}
		// Second: any running model that can translate
		for _, m := range runningModels {
			if isTranslationCapableModel(m.Name) {
				return m.Name
			}
		}
	}

	// === Phase 2: Fall back to downloaded models ===
	models, err := s.ListModels()
	if err != nil || len(models) == 0 {
		return ""
	}

	// Preferred: qwen3:8b (explicitly known good translation model)
	for _, m := range models {
		lower := strings.ToLower(m.Name)
		if lower == "qwen3:8b" || strings.HasPrefix(lower, "qwen3:8b-") {
			return m.Name
		}
	}

	// Collect translation-capable candidates, sorted by size ascending (prefer smaller = faster)
	type modelEntry struct {
		name string
		size int64
	}
	var candidates []modelEntry
	for _, m := range models {
		if isTranslationCapableModel(m.Name) {
			candidates = append(candidates, modelEntry{name: m.Name, size: m.Size})
		}
	}

	// Sort by size ascending
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].size < candidates[i].size {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	// Preferred text models first
	for _, c := range candidates {
		if isPreferredTranslationModel(c.name) {
			return c.name
		}
	}

	// Any capable model as last resort
	if len(candidates) > 0 {
		return candidates[0].name
	}

	return ""
}

// ollamaTranslate translates a single text from English to Chinese using the local Ollama model.
// Used as fallback when batch translation fails.
func (s *OllamaService) ollamaTranslate(client *http.Client, modelName, text string) string {
	payload, err := json.Marshal(map[string]any{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "system", "content": "你是翻译助手。将用户给出的英文翻译为简洁流畅的中文，只输出翻译结果，不要解释、不要前缀。"},
			{"role": "user", "content": text},
		},
		"stream":  false,
		"think":   false,
		"options": map[string]any{"temperature": 0.1, "num_predict": 256},
	})
	if err != nil {
		return ""
	}

	req, err := http.NewRequest(http.MethodPost, s.cfg.OllamaAPIURL+"/api/chat", strings.NewReader(string(payload)))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}

	translated := strings.TrimSpace(result.Message.Content)
	// Clean up possible think tags
	if idx := strings.Index(translated, "</think>"); idx >= 0 {
		translated = strings.TrimSpace(translated[idx+len("</think>"):])
	}
	// Remove common prefixes
	for _, prefix := range []string{"翻译：", "翻译:", "译文：", "译文:"} {
		if strings.HasPrefix(translated, prefix) {
			translated = strings.TrimSpace(translated[len(prefix):])
		}
	}

	return translated
}

// containsChinese checks if a string contains Chinese characters.
func containsChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
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

// ChatStream sends a streaming chat request to Ollama and returns an io.ReadCloser
// producing NDJSON lines. Caller must close the reader.
func (s *OllamaService) ChatStream(modelName string, messages []map[string]any, options map[string]any, format string, keepAlive string, think bool) (io.ReadCloser, error) {
	payload := map[string]any{
		"model":    modelName,
		"messages": messages,
		"stream":   true,
	}
	if len(options) > 0 {
		payload["options"] = options
	}
	if format != "" {
		payload["format"] = format
	}
	if keepAlive != "" {
		payload["keep_alive"] = keepAlive
	}
	if think {
		payload["think"] = true
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	// No timeout — LLM generation can take minutes for long responses.
	// Cancellation is handled by closing the response body (reader.Close()).
	req, err := http.NewRequest("POST", s.cfg.OllamaAPIURL+"/api/chat", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("chat request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chat failed (status %d): %s", resp.StatusCode, string(errBody))
	}
	return resp.Body, nil
}

// IsImageFile checks if the filename has an image extension.
func IsImageFile(filename string) bool {
	ext := strings.ToLower(filename)
	for _, suffix := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"} {
		if strings.HasSuffix(ext, suffix) {
			return true
		}
	}
	return false
}

// ParseFileContent extracts text content from an uploaded file based on its extension.
// Supports: .txt, .md, .csv, .json, .log, .yaml, .yml, .xml, .html, .go, .py, .js, .sh, .sql, etc.
// For image files, use IsImageFile() check first — images should be base64-encoded, not parsed as text.
func ParseFileContent(filename string, data []byte) (string, error) {
	// For all text-based files, just return as string (with size limit)
	const maxSize = 100 * 1024 // 100KB text limit
	if len(data) > maxSize {
		return string(data[:maxSize]) + "\n\n... (文件内容已截断，超过 100KB 限制)", nil
	}
	return string(data), nil
}
