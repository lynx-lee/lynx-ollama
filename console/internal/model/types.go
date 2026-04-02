package model

import "time"

// APIResponse is the standard API response wrapper.
type APIResponse struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ServiceStatus represents the overall Ollama service status.
type ServiceStatus struct {
	Container      ContainerInfo  `json:"container"`
	Resources      ResourceUsage  `json:"resources"`
	Models         []ModelInfo    `json:"models"`
	RunningModels  []RunningModel `json:"running_models"`
	Config         ServiceConfig  `json:"config"`
	GPU            []GPUInfo      `json:"gpu"`
	Disk           DiskUsage      `json:"disk"`
	OllamaVersion  string         `json:"ollama_version"`
	ProjectVersion string         `json:"project_version"`
	APIReachable   bool           `json:"api_reachable"`
}

// ContainerInfo holds Docker container state.
type ContainerInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`    // running, exited, etc.
	Health    string `json:"health"`    // healthy, starting, unhealthy
	Image     string `json:"image"`
	StartedAt string `json:"started_at"`
	Uptime    string `json:"uptime"`
	Ports     string `json:"ports"`
}

// ResourceUsage holds container resource metrics.
type ResourceUsage struct {
	CPUPercent    string `json:"cpu_percent"`
	MemUsage      string `json:"mem_usage"`
	MemPercent    string `json:"mem_percent"`
	NetIO         string `json:"net_io"`
	BlockIO       string `json:"block_io"`
}

// ModelInfo represents a downloaded model.
type ModelInfo struct {
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	SizeHuman    string    `json:"size_human"`
	Digest       string    `json:"digest"`
	ModifiedAt   time.Time `json:"modified_at"`
	Family       string    `json:"family,omitempty"`
	Parameters   string    `json:"parameters,omitempty"`
	Quantization string    `json:"quantization,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"` // vision, tools, thinking, code, embedding, cloud
	ModelType    string    `json:"model_type,omitempty"`    // chat, vision, embedding, code
}

// RunningModel represents a currently loaded model.
type RunningModel struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	SizeHuman  string    `json:"size_human"`
	Digest     string    `json:"digest"`
	ExpiresAt  time.Time `json:"expires_at"`
	SizeVRAM   int64     `json:"size_vram"`
	VRAMHuman  string    `json:"vram_human"`
}

// GPUInfo holds GPU hardware information.
type GPUInfo struct {
	Index           string `json:"index"`
	Name            string `json:"name"`
	MemTotal        string `json:"mem_total"`
	MemUsed         string `json:"mem_used"`
	MemFree         string `json:"mem_free"`
	Utilization     string `json:"utilization"`
	Temperature     string `json:"temperature"`
	Power           string `json:"power"`
	PowerLimit      string `json:"power_limit"`
	Driver          string `json:"driver"`
	CUDA            string `json:"cuda"`
	IsUnifiedMem    bool   `json:"is_unified_mem"`
	UnifiedMemTotal string `json:"unified_mem_total,omitempty"`
	
	// 新增字段
	PersistenceMode string         `json:"persistence_mode"`
	BusID           string         `json:"bus_id"`
	DispActive      string         `json:"disp_active"`
	VolatileECC     string         `json:"volatile_ecc"`
	FanSpeed        string         `json:"fan_speed"`
	PerfState       string         `json:"perf_state"`
	ComputeMode     string         `json:"compute_mode"`
	MIGMode         string         `json:"mig_mode"`
	Processes       []GPUProcess   `json:"processes,omitempty"`
}

// GPUProcess represents a process using GPU.
type GPUProcess struct {
	PID       int    `json:"pid"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	MemUsage  string `json:"mem_usage"`
	GI        string `json:"gi,omitempty"`
	CI        string `json:"ci,omitempty"`
}

// DiskUsage holds disk space information.
type DiskUsage struct {
	ModelDataSize string `json:"model_data_size"`
	TotalSpace    string `json:"total_space"`
	UsedSpace     string `json:"used_space"`
	AvailSpace    string `json:"avail_space"`
	UsePercent    string `json:"use_percent"`
}

// ServiceConfig holds the current Ollama configuration.
type ServiceConfig struct {
	BindAddress     string `json:"bind_address"`
	Port            string `json:"port"`
	Version         string `json:"version"`
	DataDir         string `json:"data_dir"`
	NumParallel     string `json:"num_parallel"`
	MaxQueue        string `json:"max_queue"`
	MaxLoadedModels string `json:"max_loaded_models"`
	KeepAlive       string `json:"keep_alive"`
	ContextLength   string `json:"context_length"`
	KVCacheType     string `json:"kv_cache_type"`
	FlashAttention  string `json:"flash_attention"`
	Debug           string `json:"debug"`
	CPUReservation  string `json:"cpu_reservation"`
	CPULimit        string `json:"cpu_limit"`
	MemReservation  string `json:"mem_reservation"`
	MemLimit        string `json:"mem_limit"`
	Timezone        string `json:"timezone"`
}

// HealthCheck represents a single health check item.
type HealthCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass, fail, warn, skip
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// HealthReport is the overall health check result.
type HealthReport struct {
	Checks []HealthCheck `json:"checks"`
	Passed int           `json:"passed"`
	Total  int           `json:"total"`
	Score  string        `json:"score"`
}

// PullProgress represents model pull/download progress.
type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Percent   float64 `json:"percent,omitempty"`
}

// LogEntry represents a single log line.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Raw       string `json:"raw"`
}

// UpdateInfo holds version update information.
type UpdateInfo struct {
	OldVersion string `json:"old_version"`
	NewVersion string `json:"new_version"`
	ImageID    string `json:"image_id"`
}

// MarketModel represents a model found on the Ollama website.
type MarketModel struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Sizes       []string `json:"sizes,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Pulls       string   `json:"pulls,omitempty"`
	Updated     string   `json:"updated,omitempty"`
}

// MarketSearchResult holds the search results from the Ollama website.
type MarketSearchResult struct {
	Models []MarketModel `json:"models"`
	Query  string        `json:"query,omitempty"`
	Total  int           `json:"total"`
}

// TranslateRequest is used to request translation of a model description.
type TranslateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// TranslateResponse is the result of translating a model description.
type TranslateResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// BenchmarkResult holds performance benchmark results.
type BenchmarkResult struct {
	TestName       string  `json:"test_name"`
	Duration       string  `json:"duration"`
	PromptSpeed    float64 `json:"prompt_speed,omitempty"`
	GenerateSpeed  float64 `json:"generate_speed,omitempty"`
	TotalTokens    int     `json:"total_tokens,omitempty"`
	Throughput     float64 `json:"throughput,omitempty"`
}

// EnvVariable represents a single .env configuration variable.
type EnvVariable struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	Role    string   `json:"role"`              // system, user, assistant
	Content string   `json:"content"`
	Files   []string `json:"files,omitempty"`   // uploaded file IDs (for user messages)
}

// ChatRequest is the client→server message to start a chat completion.
type ChatRequest struct {
	Type      string            `json:"type"`               // "chat" or "stop"
	Model     string            `json:"model,omitempty"`
	Messages  []ChatMessage     `json:"messages,omitempty"`
	Options   map[string]any    `json:"options,omitempty"`
	Format    string            `json:"format,omitempty"`    // "json" for JSON mode
	KeepAlive string            `json:"keep_alive,omitempty"` // e.g. "5m", "1h", "-1"
	Think     bool              `json:"think,omitempty"`      // enable thinking/reasoning mode
}

// UploadedFile holds a parsed uploaded file in memory.
type UploadedFile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	IsImage   bool      `json:"is_image"`
	Content   string    `json:"-"`           // parsed text content (not sent to client)
	Base64    string    `json:"-"`           // base64-encoded image data (not sent to client)
	Preview   string    `json:"preview"`     // first 200 chars preview, or "image" for images
	CreatedAt time.Time `json:"-"`
}

// BenchmarkDimension defines a single evaluation dimension.
type BenchmarkDimension struct {
	ID          string `json:"id"`          // e.g. "reasoning"
	Name        string `json:"name"`        // 显示名称
	Prompt      string `json:"prompt"`      // 发送给模型的 prompt
	CheckPrompt string `json:"check_prompt"` // 评分 prompt（由评判模型执行）
	MaxScore    int    `json:"max_score"`
}

// BenchmarkScore holds the score for a single dimension.
type BenchmarkScore struct {
	DimensionID string  `json:"dimension_id"`
	Name        string  `json:"name"`
	Score       float64 `json:"score"`
	MaxScore    int     `json:"max_score"`
	Response    string  `json:"response"`    // 模型原始回答（截断）
	Reasoning   string  `json:"reasoning"`   // 评分理由
	TokenCount  int     `json:"token_count"`
	DurationMs  int64   `json:"duration_ms"` // 耗时毫秒
	TokPerSec   float64 `json:"tok_per_sec"`
}

// BenchmarkResult holds the full evaluation result for a model.
type BenchmarkModelResult struct {
	ModelName   string           `json:"model_name"`
	Scores      []BenchmarkScore `json:"scores"`
	TotalScore  float64          `json:"total_score"`
	MaxTotal    int              `json:"max_total"`
	Percentage  float64          `json:"percentage"`  // 百分制
	AvgTokSec   float64          `json:"avg_tok_sec"` // 平均 tok/s
	RunAt       string           `json:"run_at"`
}
