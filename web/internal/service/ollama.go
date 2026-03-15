package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lynxlee/lynx-ollama-web/internal/config"
	"github.com/lynxlee/lynx-ollama-web/internal/model"
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

	// Translation cache: English description → Chinese translation.
	// Persists in memory for the lifetime of the process — avoids redundant LLM calls
	// when the user refreshes the model market or navigates back to it.
	translateCache sync.Map // map[string]string
}

// NewOllamaService creates a new OllamaService.
func NewOllamaService(cfg *config.Config) *OllamaService {
	return &OllamaService{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		probeClient: &http.Client{
			Timeout: 3 * time.Second,
		},
		apiReadyCacheTTL: 5 * time.Second,
	}
}

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
// Uses a separate client without timeout since model downloads can take minutes/hours.
func (s *OllamaService) PullModel(name string) (io.ReadCloser, error) {
	payload, err := json.Marshal(map[string]interface{}{
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
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; lynx-ollama-web/1.0)")
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

	return &model.MarketSearchResult{
		Models: allModels,
		Query:  query,
		Total:  len(allModels),
	}, nil
}

// TranslateDescriptions translates the given model descriptions to Chinese using the local Ollama model.
// Optimized strategy:
//  1. Check in-memory cache first — return cached translations immediately.
//  2. Collect uncached items and pack them into a single JSON payload.
//  3. Call the LLM once with a batch translation prompt (JSON in → JSON out).
//  4. Parse the returned JSON and populate results + cache.
//
// This reduces N individual LLM calls to at most 1 batch call per request.
func (s *OllamaService) TranslateDescriptions(items []model.TranslateRequest) []model.TranslateResponse {
	results := make([]model.TranslateResponse, len(items))
	for i, item := range items {
		results[i] = model.TranslateResponse{Name: item.Name, Description: item.Description}
	}

	if len(items) == 0 {
		return results
	}

	// Phase 1: Resolve from cache — collect indices of uncached items
	var uncached []uncachedItem

	for i, item := range items {
		desc := item.Description
		if desc == "" || len(desc) < 10 || containsChinese(desc) {
			continue // Skip empty, too short, or already Chinese
		}

		// Check cache
		if cached, ok := s.translateCache.Load(desc); ok {
			results[i].Description = cached.(string)
			continue
		}
		uncached = append(uncached, uncachedItem{index: i, name: item.Name, desc: desc})
	}

	// All translations found in cache — return immediately
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

	// Phase 4: Populate results and cache
	for _, uc := range uncached {
		if t, ok := translated[uc.name]; ok && t != "" && t != uc.desc {
			results[uc.index].Description = t
			// Store in cache: key = original English description
			s.translateCache.Store(uc.desc, t)
		}
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

	payload, err := json.Marshal(map[string]interface{}{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": string(inputJSON)},
		},
		"stream":  false,
		"think":   false,
		"options": map[string]interface{}{"temperature": 0.1, "num_predict": 4096},
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
	payload, err := json.Marshal(map[string]interface{}{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "system", "content": "你是翻译助手。将用户给出的英文翻译为简洁流畅的中文，只输出翻译结果，不要解释、不要前缀。"},
			{"role": "user", "content": text},
		},
		"stream":  false,
		"think":   false,
		"options": map[string]interface{}{"temperature": 0.1, "num_predict": 256},
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
