package service

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// MetadataStore provides persistent storage for model metadata and translation cache.
type MetadataStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewMetadataStore opens (or creates) the SQLite database at the given directory.
func NewMetadataStore(dataDir string) (*MetadataStore, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "console.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}

	store := &MetadataStore{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	slog.Info("metadata store initialized", "path", dbPath)
	return store, nil
}

// migrate creates tables if they don't exist.
func (s *MetadataStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS model_meta (
			name         TEXT PRIMARY KEY,
			capabilities TEXT DEFAULT '[]',
			model_type   TEXT DEFAULT '',
			source       TEXT DEFAULT '',
			updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS translations (
			original_text TEXT PRIMARY KEY,
			translated    TEXT NOT NULL,
			updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}

// Close closes the database.
func (s *MetadataStore) Close() error {
	return s.db.Close()
}

// ── Model Metadata ──────────────────────────────────────────────

// ModelMeta holds capability and type info for a model.
type ModelMeta struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"` // vision, tools, thinking, code, embedding, cloud
	ModelType    string   `json:"model_type"`    // chat, embedding, vision, code, etc.
	Source       string   `json:"source"`        // "show", "market", "infer"
	UpdatedAt    time.Time `json:"updated_at"`
}

// GetModelMeta retrieves metadata for a model. Returns nil if not found.
func (s *MetadataStore) GetModelMeta(name string) *ModelMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Normalize: try both exact name and base name (without tag)
	names := []string{name}
	if idx := strings.LastIndex(name, ":"); idx > 0 {
		names = append(names, name[:idx])
	}

	for _, n := range names {
		var capsJSON, modelType, source string
		var updatedAt time.Time
		err := s.db.QueryRow(
			"SELECT capabilities, model_type, source, updated_at FROM model_meta WHERE name = ?", n,
		).Scan(&capsJSON, &modelType, &source, &updatedAt)
		if err != nil {
			continue
		}

		var caps []string
		json.Unmarshal([]byte(capsJSON), &caps)
		return &ModelMeta{
			Name:         n,
			Capabilities: caps,
			ModelType:    modelType,
			Source:       source,
			UpdatedAt:    updatedAt,
		}
	}
	return nil
}

// SetModelMeta upserts metadata for a model.
func (s *MetadataStore) SetModelMeta(name string, capabilities []string, modelType, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	capsJSON, _ := json.Marshal(capabilities)
	_, err := s.db.Exec(`
		INSERT INTO model_meta (name, capabilities, model_type, source, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET
			capabilities = excluded.capabilities,
			model_type = excluded.model_type,
			source = excluded.source,
			updated_at = CURRENT_TIMESTAMP
	`, name, string(capsJSON), modelType, source)
	if err != nil {
		slog.Warn("failed to save model meta", "name", name, "error", err)
	}
}

// GetAllModelMeta returns all stored model metadata as a map[name]→ModelMeta.
func (s *MetadataStore) GetAllModelMeta() map[string]*ModelMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*ModelMeta)
	rows, err := s.db.Query("SELECT name, capabilities, model_type, source, updated_at FROM model_meta")
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var name, capsJSON, modelType, source string
		var updatedAt time.Time
		if rows.Scan(&name, &capsJSON, &modelType, &source, &updatedAt) != nil {
			continue
		}
		var caps []string
		json.Unmarshal([]byte(capsJSON), &caps)
		result[name] = &ModelMeta{
			Name:         name,
			Capabilities: caps,
			ModelType:    modelType,
			Source:       source,
			UpdatedAt:    updatedAt,
		}
	}
	return result
}

// ── Translation Cache ───────────────────────────────────────────

// GetTranslation retrieves a cached translation. Returns "" if not found.
func (s *MetadataStore) GetTranslation(original string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var translated string
	err := s.db.QueryRow("SELECT translated FROM translations WHERE original_text = ?", original).Scan(&translated)
	if err != nil {
		return ""
	}
	return translated
}

// SetTranslation stores a translation.
func (s *MetadataStore) SetTranslation(original, translated string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO translations (original_text, translated, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(original_text) DO UPDATE SET
			translated = excluded.translated,
			updated_at = CURRENT_TIMESTAMP
	`, original, translated)
	if err != nil {
		slog.Warn("failed to save translation", "error", err)
	}
}

// GetTranslationBatch retrieves cached translations for multiple originals.
// Returns a map[original]→translated (only found entries).
func (s *MetadataStore) GetTranslationBatch(originals []string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string, len(originals))
	// SQLite has a limit on bind params, batch in groups of 500
	for i := 0; i < len(originals); i += 500 {
		end := i + 500
		if end > len(originals) {
			end = len(originals)
		}
		batch := originals[i:end]
		placeholders := strings.Repeat("?,", len(batch))
		placeholders = placeholders[:len(placeholders)-1]

		args := make([]any, len(batch))
		for j, o := range batch {
			args[j] = o
		}

		rows, err := s.db.Query(
			"SELECT original_text, translated FROM translations WHERE original_text IN ("+placeholders+")", args...,
		)
		if err != nil {
			continue
		}
		for rows.Next() {
			var orig, trans string
			if rows.Scan(&orig, &trans) == nil {
				result[orig] = trans
			}
		}
		rows.Close()
	}
	return result
}

// SetTranslationBatch stores multiple translations at once.
func (s *MetadataStore) SetTranslationBatch(translations map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`
		INSERT INTO translations (original_text, translated, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(original_text) DO UPDATE SET
			translated = excluded.translated,
			updated_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()

	for orig, trans := range translations {
		stmt.Exec(orig, trans)
	}
	tx.Commit()
}

// ── Capability Detection Helpers ────────────────────────────────

// InferCapabilitiesFromShowModel extracts capabilities from /api/show response.
func InferCapabilitiesFromShowModel(info map[string]any) (caps []string, modelType string) {
	seen := make(map[string]bool)

	// Check details.families for projector/clip → vision
	if details, ok := info["details"].(map[string]any); ok {
		if families, ok := details["families"].([]any); ok {
			for _, f := range families {
				fStr, _ := f.(string)
				fLower := strings.ToLower(fStr)
				if strings.Contains(fLower, "clip") || strings.Contains(fLower, "mmproj") {
					if !seen["vision"] {
						caps = append(caps, "vision")
						seen["vision"] = true
					}
				}
			}
		}
		if family, ok := details["family"].(string); ok {
			fLower := strings.ToLower(family)
			if strings.Contains(fLower, "bert") || strings.Contains(fLower, "nomic") {
				modelType = "embedding"
				if !seen["embedding"] {
					caps = append(caps, "embedding")
					seen["embedding"] = true
				}
			}
		}
	}

	// Check template for tool support
	if tmpl, ok := info["template"].(string); ok {
		tmplLower := strings.ToLower(tmpl)
		if strings.Contains(tmplLower, "tool") || strings.Contains(tmplLower, "function") {
			if !seen["tools"] {
				caps = append(caps, "tools")
				seen["tools"] = true
			}
		}
	}

	// Check model_info for architecture clues
	if modelInfo, ok := info["model_info"].(map[string]any); ok {
		for k := range modelInfo {
			kLower := strings.ToLower(k)
			if strings.Contains(kLower, "vision") || strings.Contains(kLower, "clip") || strings.Contains(kLower, "projector") {
				if !seen["vision"] {
					caps = append(caps, "vision")
					seen["vision"] = true
				}
			}
		}
	}

	// Determine model type
	if modelType == "" {
		if seen["vision"] {
			modelType = "vision"
		} else {
			modelType = "chat"
		}
	}

	return caps, modelType
}

// InferCapabilitiesFromName guesses capabilities from model name (fallback).
func InferCapabilitiesFromName(name string) (caps []string, modelType string) {
	lower := strings.ToLower(name)

	if containsAny(lower, "llava", "vision", "minicpm-v", "moondream", "bakllava", "cogvlm", "internvl") {
		caps = append(caps, "vision")
		modelType = "vision"
	}
	if containsAny(lower, "embed", "nomic-embed", "bge-", "mxbai-embed", "all-minilm") {
		caps = append(caps, "embedding")
		modelType = "embedding"
	}
	if containsAny(lower, "coder", "codellama", "starcoder", "deepseek-coder", "code") {
		caps = append(caps, "code")
	}
	if containsAny(lower, ":cloud") {
		caps = append(caps, "cloud")
	}

	if modelType == "" {
		modelType = "chat"
	}
	return caps, modelType
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
