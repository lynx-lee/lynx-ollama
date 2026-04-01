package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lynxlee/lynx-ollama-console/internal/model"
)

// ChatFileStore manages persistent chat file storage.
// Files are stored on disk under baseDir/<date>/<id>/ with metadata JSON + raw content.
type ChatFileStore struct {
	baseDir string
	mu      sync.RWMutex
	cache   map[string]*model.UploadedFile // in-memory index for fast lookup
}

// NewChatFileStore creates a new persistent file store.
func NewChatFileStore(baseDir string) *ChatFileStore {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		slog.Error("failed to create chat-files directory", "path", baseDir, "error", err)
	}
	store := &ChatFileStore{
		baseDir: baseDir,
		cache:   make(map[string]*model.UploadedFile),
	}
	// Load today's files into cache on startup
	store.loadTodayCache()
	return store
}

// dateDir returns the date-based subdirectory path (e.g. chat-files/2026-04-01/).
func (s *ChatFileStore) dateDir(t time.Time) string {
	return filepath.Join(s.baseDir, t.Format("2006-01-02"))
}

// fileDir returns the directory for a specific file (e.g. chat-files/2026-04-01/<id>/).
func (s *ChatFileStore) fileDir(id string, t time.Time) string {
	return filepath.Join(s.dateDir(t), id)
}

// Save persists an uploaded file to disk and adds it to the in-memory cache.
func (s *ChatFileStore) Save(uf *model.UploadedFile) error {
	dir := s.fileDir(uf.ID, uf.CreatedAt)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create file directory: %w", err)
	}

	// Write raw content
	rawPath := filepath.Join(dir, uf.Name)
	if uf.IsImage {
		data, err := base64.StdEncoding.DecodeString(uf.Base64)
		if err != nil {
			return fmt.Errorf("failed to decode base64: %w", err)
		}
		if err := os.WriteFile(rawPath, data, 0o644); err != nil {
			return fmt.Errorf("failed to write image file: %w", err)
		}
	} else {
		if err := os.WriteFile(rawPath, []byte(uf.Content), 0o644); err != nil {
			return fmt.Errorf("failed to write text file: %w", err)
		}
	}

	// Write metadata JSON
	meta := map[string]any{
		"id":         uf.ID,
		"name":       uf.Name,
		"size":       uf.Size,
		"is_image":   uf.IsImage,
		"preview":    uf.Preview,
		"created_at": uf.CreatedAt.Format(time.RFC3339),
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	metaPath := filepath.Join(dir, "metadata.json")
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	// Add to cache
	s.mu.Lock()
	s.cache[uf.ID] = uf
	s.mu.Unlock()

	slog.Debug("chat file saved", "id", uf.ID, "name", uf.Name, "path", dir)
	return nil
}

// Get retrieves an uploaded file by ID (from cache, or loads from disk).
func (s *ChatFileStore) Get(id string) (*model.UploadedFile, bool) {
	s.mu.RLock()
	f, ok := s.cache[id]
	s.mu.RUnlock()
	if ok {
		return f, true
	}

	// Try to load from disk — scan recent date dirs
	uf := s.loadFromDisk(id)
	if uf != nil {
		s.mu.Lock()
		s.cache[id] = uf
		s.mu.Unlock()
		return uf, true
	}
	return nil, false
}

// loadFromDisk scans date directories to find a file by ID.
func (s *ChatFileStore) loadFromDisk(id string) *model.UploadedFile {
	// Check today and last 7 days
	now := time.Now()
	for i := 0; i < 7; i++ {
		t := now.AddDate(0, 0, -i)
		dir := s.fileDir(id, t)
		metaPath := filepath.Join(dir, "metadata.json")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		return s.loadFileFromDir(dir)
	}
	return nil
}

// loadFileFromDir reads a file entry from a directory.
func (s *ChatFileStore) loadFileFromDir(dir string) *model.UploadedFile {
	metaBytes, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		IsImage   bool   `json:"is_image"`
		Preview   string `json:"preview"`
		CreatedAt string `json:"created_at"`
	}
	if json.Unmarshal(metaBytes, &meta) != nil {
		return nil
	}

	createdAt, _ := time.Parse(time.RFC3339, meta.CreatedAt)
	uf := &model.UploadedFile{
		ID:        meta.ID,
		Name:      meta.Name,
		Size:      meta.Size,
		IsImage:   meta.IsImage,
		Preview:   meta.Preview,
		CreatedAt: createdAt,
	}

	rawPath := filepath.Join(dir, meta.Name)
	if meta.IsImage {
		data, err := os.ReadFile(rawPath)
		if err == nil {
			uf.Base64 = base64.StdEncoding.EncodeToString(data)
		}
	} else {
		data, err := os.ReadFile(rawPath)
		if err == nil {
			uf.Content = string(data)
		}
	}
	return uf
}

// loadTodayCache loads all files from today's directory into cache.
func (s *ChatFileStore) loadTodayCache() {
	todayDir := s.dateDir(time.Now())
	entries, err := os.ReadDir(todayDir)
	if err != nil {
		return // directory doesn't exist yet, that's fine
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(todayDir, entry.Name())
		uf := s.loadFileFromDir(dir)
		if uf != nil {
			s.mu.Lock()
			s.cache[uf.ID] = uf
			s.mu.Unlock()
		}
	}
	if len(s.cache) > 0 {
		slog.Info("loaded chat files from today's cache", "count", len(s.cache))
	}
}

// GetFilePath returns the raw file path on disk for a given file ID.
// Useful for serving file downloads directly.
func (s *ChatFileStore) GetFilePath(id string) (string, bool) {
	s.mu.RLock()
	f, ok := s.cache[id]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	rawPath := filepath.Join(s.fileDir(id, f.CreatedAt), f.Name)
	if _, err := os.Stat(rawPath); err != nil {
		return "", false
	}
	return rawPath, true
}

// SaveGeneratedFile saves a file generated by LLM to disk.
func (s *ChatFileStore) SaveGeneratedFile(name string, content []byte) (*model.UploadedFile, error) {
	now := time.Now()
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("gen_%x", idBytes)

	isImage := IsImageFile(name)
	uf := &model.UploadedFile{
		ID:        id,
		Name:      name,
		Size:      int64(len(content)),
		IsImage:   isImage,
		CreatedAt: now,
	}

	if isImage {
		uf.Base64 = base64.StdEncoding.EncodeToString(content)
		uf.Preview = "image"
	} else {
		text := string(content)
		uf.Content = text
		preview := text
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		uf.Preview = preview
	}

	if err := s.Save(uf); err != nil {
		return nil, err
	}
	return uf, nil
}
