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

		CREATE TABLE IF NOT EXISTS chat_sessions (
			id         TEXT PRIMARY KEY,
			title      TEXT NOT NULL DEFAULT '新对话',
			model      TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS chat_messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role       TEXT NOT NULL,
			content    TEXT NOT NULL DEFAULT '',
			files      TEXT DEFAULT '[]',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (session_id) REFERENCES chat_sessions(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_chat_messages_session ON chat_messages(session_id);

		CREATE TABLE IF NOT EXISTS benchmark_results (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			model_name TEXT NOT NULL,
			scores     TEXT NOT NULL DEFAULT '[]',
			total_score REAL DEFAULT 0,
			max_total  INTEGER DEFAULT 0,
			percentage REAL DEFAULT 0,
			avg_tok_sec REAL DEFAULT 0,
			run_at     DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_benchmark_model ON benchmark_results(model_name);
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

// InvalidateShowMeta deletes all model_meta entries with source="show",
// forcing re-detection on next ListModels call.
func (s *MetadataStore) InvalidateShowMeta() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec("DELETE FROM model_meta WHERE source = 'show'")
	slog.Info("invalidated show-source model metadata cache")
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

// ── Chat Sessions ──────────────────────────────────────────────

// ChatSession represents a saved conversation.
type ChatSession struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Model     string        `json:"model"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Messages  []ChatMsgRow  `json:"messages,omitempty"`
}

// ChatMsgRow represents a single message in a session.
type ChatMsgRow struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Files     []string  `json:"files,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ListChatSessions returns all sessions ordered by updated_at desc.
func (s *MetadataStore) ListChatSessions() []ChatSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT id, title, model, created_at, updated_at FROM chat_sessions ORDER BY updated_at DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var sessions []ChatSession
	for rows.Next() {
		var sess ChatSession
		if rows.Scan(&sess.ID, &sess.Title, &sess.Model, &sess.CreatedAt, &sess.UpdatedAt) == nil {
			sessions = append(sessions, sess)
		}
	}
	return sessions
}

// CreateChatSession creates a new session and returns its ID.
func (s *MetadataStore) CreateChatSession(id, title, modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"INSERT INTO chat_sessions (id, title, model) VALUES (?, ?, ?)",
		id, title, modelName,
	)
	if err != nil {
		slog.Warn("failed to create chat session", "error", err)
	}
}

// UpdateChatSession updates session title and model.
func (s *MetadataStore) UpdateChatSession(id, title, modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec(
		"UPDATE chat_sessions SET title = ?, model = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		title, modelName, id,
	)
}

// DeleteChatSession deletes a session and all its messages.
func (s *MetadataStore) DeleteChatSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec("DELETE FROM chat_messages WHERE session_id = ?", id)
	_, _ = s.db.Exec("DELETE FROM chat_sessions WHERE id = ?", id)
}

// GetChatMessages returns all messages for a session.
func (s *MetadataStore) GetChatMessages(sessionID string) []ChatMsgRow {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		"SELECT id, role, content, files, created_at FROM chat_messages WHERE session_id = ? ORDER BY id ASC",
		sessionID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var msgs []ChatMsgRow
	for rows.Next() {
		var msg ChatMsgRow
		var filesJSON string
		if rows.Scan(&msg.ID, &msg.Role, &msg.Content, &filesJSON, &msg.CreatedAt) == nil {
			json.Unmarshal([]byte(filesJSON), &msg.Files)
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// AddChatMessage appends a message to a session.
func (s *MetadataStore) AddChatMessage(sessionID, role, content string, files []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	filesJSON, _ := json.Marshal(files)
	_, err := s.db.Exec(
		"INSERT INTO chat_messages (session_id, role, content, files) VALUES (?, ?, ?, ?)",
		sessionID, role, content, string(filesJSON),
	)
	if err != nil {
		slog.Warn("failed to add chat message", "error", err)
	}
	// Touch session updated_at
	_, _ = s.db.Exec("UPDATE chat_sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?", sessionID)
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

	// Check model_info for architecture clues (weak signal only).
	// vision/clip/projector keys in model_info alone are NOT sufficient to mark as vision —
	// many text-only variants (e.g. gemma3:27b, devstral-small-2:24b) carry these keys
	// in their architecture metadata even though they lack the actual vision projector weights.
	// Only mark vision if families already confirmed it (via clip/mmproj above).
	if modelInfo, ok := info["model_info"].(map[string]any); ok {
		for k := range modelInfo {
			kLower := strings.ToLower(k)
			// Only use model_info to confirm vision if we see a dedicated projector block
			// AND it contains actual tensor count/size (not just architecture definition)
			if strings.Contains(kLower, "mmproj") || strings.Contains(kLower, "projector.block_count") {
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

// ── Benchmark Results ──────────────────────────────────────────

// SaveBenchmarkResult stores a benchmark result.
func (s *MetadataStore) SaveBenchmarkResult(modelName string, scoresJSON string, totalScore float64, maxTotal int, percentage float64, avgTokSec float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO benchmark_results (model_name, scores, total_score, max_total, percentage, avg_tok_sec)
		VALUES (?, ?, ?, ?, ?, ?)
	`, modelName, scoresJSON, totalScore, maxTotal, percentage, avgTokSec)
	if err != nil {
		slog.Warn("failed to save benchmark result", "model", modelName, "error", err)
	}
}

// ListBenchmarkResults returns the latest benchmark result for each model.
func (s *MetadataStore) ListBenchmarkResults() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT model_name, scores, total_score, max_total, percentage, avg_tok_sec, run_at
		FROM benchmark_results
		WHERE id IN (SELECT MAX(id) FROM benchmark_results GROUP BY model_name)
		ORDER BY percentage DESC
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var modelName, scoresJSON, runAt string
		var totalScore, percentage, avgTokSec float64
		var maxTotal int
		if rows.Scan(&modelName, &scoresJSON, &totalScore, &maxTotal, &percentage, &avgTokSec, &runAt) != nil {
			continue
		}
		var scores []any
		json.Unmarshal([]byte(scoresJSON), &scores)
		results = append(results, map[string]any{
			"model_name":  modelName,
			"scores":      scores,
			"total_score":  totalScore,
			"max_total":    maxTotal,
			"percentage":   percentage,
			"avg_tok_sec":  avgTokSec,
			"run_at":       runAt,
		})
	}
	return results
}
