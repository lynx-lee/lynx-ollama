package handler

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lynxlee/lynx-ollama-console/internal/config"
	"github.com/lynxlee/lynx-ollama-console/internal/model"
	"github.com/lynxlee/lynx-ollama-console/internal/service"
)

var upgrader = websocket.Upgrader{
	// CheckOrigin defaults to checking the Origin header matches the Host.
	// WebSocket auth is handled by the API key middleware (via ?key= query param).
}

// APIHandler holds all API endpoint handlers.
type APIHandler struct {
	ollama    *service.OllamaService
	docker    *service.DockerService
	system    *service.SystemService
	chatFiles *service.ChatFileStore
	cfg       *config.Config
	version   string
	statusHub *StatusHub

	// Last inference latency tracking (updated by StreamChat on each "done")
	lastInferMs atomic.Int64

	// Inference event tracker (parses Ollama container GIN logs)
	inferTracker *service.InferenceTracker
}

// NewAPIHandler creates a new APIHandler.
func NewAPIHandler(ollama *service.OllamaService, docker *service.DockerService, system *service.SystemService, cfg *config.Config, version string) *APIHandler {
	h := &APIHandler{
		ollama:    ollama,
		docker:    docker,
		system:    system,
		chatFiles: service.NewChatFileStore(cfg.ChatFilesDir),
		cfg:       cfg,
		version:   version,
		inferTracker: service.NewInferenceTracker(500),
	}
	h.statusHub = NewStatusHub(h)
	h.inferTracker.Start(5 * time.Second)
	return h
}

func jsonResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.APIResponse{Success: true, Data: data})
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.APIResponse{Success: false, Error: msg})
}

// ── Authentication ──────────────────────────────────────────────────

// VerifyAPIKey validates the provided API key.
// This endpoint is exempt from auth middleware so the frontend can check the key.
func (h *APIHandler) VerifyAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Key), []byte(h.cfg.APIKey)) != 1 {
		jsonError(w, http.StatusForbidden, "API Key 无效")
		return
	}
	jsonResponse(w, map[string]string{"message": "认证成功"})
}

// ── Project Version ─────────────────────────────────────────────────

// GetVersion returns the project version info.
func (h *APIHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	ollamaVersion, _ := h.ollama.GetVersion()
	jsonResponse(w, map[string]string{
		"project_version": h.version,
		"ollama_version":  ollamaVersion,
	})
}

// ── Service Status ──────────────────────────────────────────────────

// collectResult is used for parallel data collection in status endpoints.
type collectResult struct {
	key string
	val any
	err error
}

// GetStatus returns comprehensive service status (full version, used on Dashboard).
func (h *APIHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	status := model.ServiceStatus{}

	// Parallel data collection — 7 goroutines
	ch := make(chan collectResult, 7)

	go func() {
		info, err := h.docker.GetContainerInfo(ctx)
		ch <- collectResult{"container", info, err}
	}()
	go func() {
		usage, err := h.docker.GetResourceUsage(ctx)
		ch <- collectResult{"resources", usage, err}
	}()
	go func() {
		models, err := h.ollama.ListModels()
		ch <- collectResult{"models", models, err}
	}()
	go func() {
		running, err := h.ollama.ListRunningModels()
		ch <- collectResult{"running", running, err}
	}()
	go func() {
		gpus, err := h.docker.GetGPUInfo(ctx)
		ch <- collectResult{"gpu", gpus, err}
	}()
	go func() {
		disk, err := h.docker.GetDiskUsage(ctx)
		ch <- collectResult{"disk", disk, err}
	}()
	go func() {
		version, err := h.ollama.GetVersion()
		ch <- collectResult{"version", version, err}
	}()

	for i := 0; i < 7; i++ {
		r := <-ch
		switch r.key {
		case "container":
			status.Container = r.val.(model.ContainerInfo)
		case "resources":
			status.Resources = r.val.(model.ResourceUsage)
		case "models":
			if r.val != nil {
				status.Models = r.val.([]model.ModelInfo)
			}
		case "running":
			if r.err == nil && r.val != nil {
				status.RunningModels = r.val.([]model.RunningModel)
				status.APIReachable = true
			}
		case "gpu":
			if r.val != nil {
				status.GPU = r.val.([]model.GPUInfo)
			}
		case "disk":
			status.Disk = r.val.(model.DiskUsage)
		case "version":
			if r.val != nil {
				status.OllamaVersion = r.val.(string)
			}
		}
	}

	// If API was not reachable via running models, do a quick probe as fallback
	if !status.APIReachable {
		status.APIReachable = h.ollama.IsAPIReady()
	}

	// Correct health status based on actual API reachability:
	// - Docker may report "starting" during start_period even if API is already up
	// - Docker may report "unhealthy" due to transient probe failures
	// - Health may be empty if container was started without healthcheck config
	h.correctHealthStatus(&status.Container, status.APIReachable)

	status.ProjectVersion = h.version

	// Read config
	if vars, err := h.system.ReadEnvConfig(); err == nil {
		cfgMap := make(map[string]string)
		for _, v := range vars {
			cfgMap[v.Key] = v.Value
		}
		status.Config = model.ServiceConfig{
			BindAddress:     cfgMap["OLLAMA_BIND_ADDRESS"],
			Port:            cfgMap["OLLAMA_PORT"],
			Version:         cfgMap["OLLAMA_VERSION"],
			DataDir:         cfgMap["OLLAMA_DATA_DIR"],
			NumParallel:     cfgMap["OLLAMA_NUM_PARALLEL"],
			MaxQueue:        cfgMap["OLLAMA_MAX_QUEUE"],
			MaxLoadedModels: cfgMap["OLLAMA_MAX_LOADED_MODELS"],
			KeepAlive:       cfgMap["OLLAMA_KEEP_ALIVE"],
			ContextLength:   cfgMap["OLLAMA_CONTEXT_LENGTH"],
			KVCacheType:     cfgMap["OLLAMA_KV_CACHE_TYPE"],
			FlashAttention:  cfgMap["OLLAMA_FLASH_ATTENTION"],
			Debug:           cfgMap["OLLAMA_DEBUG"],
			CPUReservation:  cfgMap["OLLAMA_CPU_RESERVATION"],
			CPULimit:        cfgMap["OLLAMA_CPU_LIMIT"],
			MemReservation:  cfgMap["OLLAMA_MEM_RESERVATION"],
			MemLimit:        cfgMap["OLLAMA_MEM_LIMIT"],
			Timezone:        cfgMap["OLLAMA_TZ"],
		}
	}

	jsonResponse(w, status)
}

// GetStatusLite returns a lightweight status snapshot.
// Optimized: version from cache, API readiness inferred from ListRunningModels.
func (h *APIHandler) GetStatusLite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	status := model.ServiceStatus{}

	ch := make(chan collectResult, 3)
	go func() {
		info, err := h.docker.GetContainerInfo(ctx)
		ch <- collectResult{"container", info, err}
	}()
	go func() {
		running, err := h.ollama.ListRunningModels()
		ch <- collectResult{"running", running, err}
	}()
	go func() {
		gpus, err := h.docker.GetGPUInfo(ctx)
		ch <- collectResult{"gpu", gpus, err}
	}()

	apiReady := false
	for i := 0; i < 3; i++ {
		r := <-ch
		switch r.key {
		case "container":
			status.Container = r.val.(model.ContainerInfo)
		case "running":
			if r.err == nil {
				apiReady = true
			}
			if r.val != nil {
				status.RunningModels = r.val.([]model.RunningModel)
			}
		case "gpu":
			if r.val != nil {
				status.GPU = r.val.([]model.GPUInfo)
			}
		}
	}

	if ver, err := h.ollama.GetVersion(); err == nil {
		status.OllamaVersion = ver
	}

	status.APIReachable = apiReady
	h.correctHealthStatus(&status.Container, apiReady)
	status.ProjectVersion = h.version

	jsonResponse(w, status)
}

// correctHealthStatus adjusts the container health status based on actual API reachability.
// Docker's health check has inherent delays (interval, start_period, retries), so we use
// a direct API probe to provide more accurate real-time status:
//   - "starting" + API reachable → "healthy" (API is up before Docker finishes start_period)
//   - "unhealthy" + API reachable → "healthy" (Docker probe may fail transiently)
//   - empty health + container running + API reachable → "healthy" (no healthcheck configured)
func (h *APIHandler) correctHealthStatus(info *model.ContainerInfo, apiReady bool) {
	if !apiReady {
		return // If API is truly down, trust Docker's status
	}

	switch info.Health {
	case "starting", "unhealthy":
		// API is actually reachable — override Docker's stale/inaccurate status
		info.Health = "healthy"
	case "":
		// No healthcheck configured (e.g. manual docker run without compose)
		if info.Status == "running" {
			info.Health = "healthy"
		}
	}
}

// ── Service Control ─────────────────────────────────────────────────

// StartService starts the Ollama service.
func (h *APIHandler) StartService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	output, err := h.docker.StartService(ctx)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("启动失败: %s", output))
		return
	}
	jsonResponse(w, map[string]string{"output": output, "message": "服务已启动"})
}

// StopService stops the Ollama service.
func (h *APIHandler) StopService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	output, err := h.docker.StopService(ctx)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("停止失败: %s", output))
		return
	}
	jsonResponse(w, map[string]string{"output": output, "message": "服务已停止"})
}

// RestartService restarts the Ollama service.
func (h *APIHandler) RestartService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	output, err := h.docker.RestartService(ctx)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("重启失败: %s", output))
		return
	}
	jsonResponse(w, map[string]string{"output": output, "message": "服务已重启"})
}

// UpdateService updates Ollama to the latest version.
func (h *APIHandler) UpdateService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 600*time.Second)
	defer cancel()

	oldVersion, _ := h.ollama.GetVersion()

	output, err := h.docker.UpdateService(ctx)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("更新失败: %s", output))
		return
	}

	// Wait for API ready (respect context cancellation)
	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			jsonError(w, http.StatusGatewayTimeout, "更新超时或请求已取消")
			return
		default:
		}
		if h.ollama.IsAPIReady() {
			break
		}
		time.Sleep(2 * time.Second)
	}

	newVersion, _ := h.ollama.GetVersion()

	jsonResponse(w, model.UpdateInfo{
		OldVersion: oldVersion,
		NewVersion: newVersion,
	})
}

// ── Models ──────────────────────────────────────────────────────────

// ListModels returns all downloaded models.
func (h *APIHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.ollama.ListModels()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, models)
}

// ListRunningModels returns currently loaded models.
func (h *APIHandler) ListRunningModels(w http.ResponseWriter, r *http.Request) {
	running, err := h.ollama.ListRunningModels()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, running)
}

// PullModel starts pulling a model (returns immediately, use WebSocket for progress).
func (h *APIHandler) PullModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "model name is required")
		return
	}

	// Start pull and stream response
	reader, err := h.ollama.PullModel(req.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Transfer-Encoding", "chunked")

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()
	}
}

// DeleteModel deletes a model.
func (h *APIHandler) DeleteModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "model name is required")
		return
	}

	if err := h.ollama.DeleteModel(name); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, map[string]string{"message": fmt.Sprintf("模型 %s 已删除", name)})
}

// ShowModel returns detailed model information.
func (h *APIHandler) ShowModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "model name is required")
		return
	}

	info, err := h.ollama.ShowModel(name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, info)
}

// CheckModelsCompatibility checks all downloaded models for Ollama version compatibility.
func (h *APIHandler) CheckModelsCompatibility(w http.ResponseWriter, r *http.Request) {
	incompatible, err := h.ollama.CheckModelsCompatibility()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if incompatible == nil {
		incompatible = []service.IncompatibleModel{}
	}
	jsonResponse(w, incompatible)
}

// SearchMarketModels searches the Ollama website for models.
func (h *APIHandler) SearchMarketModels(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	category := r.URL.Query().Get("c")
	sort := r.URL.Query().Get("sort")

	result, err := h.ollama.SearchModels(query, category, sort)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("搜索失败: %s", err.Error()))
		return
	}
	jsonResponse(w, result)
}

// TranslateModelDescriptions translates model descriptions to Chinese using the local Ollama model.
// Accepts a batch of items; if the batch exceeds maxBatchPerLLM, it is split into multiple
// sequential LLM calls (each ≤ 100 items) so that all descriptions get translated.
func (h *APIHandler) TranslateModelDescriptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []model.TranslateRequest `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Items) == 0 {
		jsonResponse(w, []model.TranslateResponse{})
		return
	}

	// Hard ceiling to prevent abuse (well above the current ~215 models on ollama.com)
	const maxTotal = 500
	if len(req.Items) > maxTotal {
		req.Items = req.Items[:maxTotal]
	}

	// Split into batches of 100 for the LLM (keeps prompt size manageable)
	const batchSize = 100
	allResults := make([]model.TranslateResponse, 0, len(req.Items))

	for start := 0; start < len(req.Items); start += batchSize {
		end := start + batchSize
		if end > len(req.Items) {
			end = len(req.Items)
		}
		batch := req.Items[start:end]
		results := h.ollama.TranslateDescriptions(batch)
		allResults = append(allResults, results...)
	}

	jsonResponse(w, allResults)
}

// ── Health & Diagnostics ────────────────────────────────────────────

// Ping is a lightweight liveness probe that returns 200 without any external calls.
// Used by Docker healthcheck to avoid unnecessary requests to Ollama/Docker.
func (h *APIHandler) Ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// HealthCheck performs comprehensive health checks.
func (h *APIHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	report, err := h.system.RunHealthCheck(ctx, h.ollama, h.docker)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, report)
}

// GetGPUInfo returns GPU information.
func (h *APIHandler) GetGPUInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	gpus, err := h.docker.GetGPUInfo(ctx)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, gpus)
}

// GetLogs returns recent service logs.
func (h *APIHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	lines := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && n > 0 {
		lines = n
	}

	output, err := h.docker.GetLogs(ctx, lines)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Parse log lines
	logLines := strings.Split(output, "\n")
	entries := make([]model.LogEntry, 0, len(logLines))
	for _, line := range logLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entries = append(entries, model.LogEntry{Raw: line})
	}

	jsonResponse(w, entries)
}

// ── Configuration ───────────────────────────────────────────────────

// GetConfig returns the current configuration.
func (h *APIHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	vars, err := h.system.ReadEnvConfig()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, vars)
}

// UpdateConfig updates configuration variables.
func (h *APIHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Variables []model.EnvVariable `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	for _, v := range req.Variables {
		if err := h.system.UpdateEnvConfig(v.Key, v.Value); err != nil {
			jsonError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update %s: %s", v.Key, err.Error()))
			return
		}
	}

	jsonResponse(w, map[string]string{"message": "配置已更新，需要重启服务生效"})
}

// Optimize runs hardware detection and optimization.
func (h *APIHandler) Optimize(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	dryRun := r.URL.Query().Get("dry_run") == "true"

	output, err := h.system.RunScript(ctx, func() string {
		if dryRun {
			return "optimize --dry-run"
		}
		return "optimize --yes"
	}())
	if err != nil {
		slog.Warn("optimize completed with warnings", "error", err)
	}

	jsonResponse(w, map[string]string{"output": output})
}

// Clean performs cleanup operations.
func (h *APIHandler) Clean(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	var req struct {
		Mode string `json:"mode"` // soft, hard
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var output string
	var err error
	switch req.Mode {
	case "soft":
		output, err = h.docker.CleanSoft(ctx)
	case "hard":
		output, err = h.docker.CleanHard(ctx)
	default:
		jsonError(w, http.StatusBadRequest, "invalid clean mode (use: soft, hard)")
		return
	}

	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("清理失败: %s", output))
		return
	}
	jsonResponse(w, map[string]string{"output": output, "message": fmt.Sprintf("清理完成 (模式: %s)", req.Mode)})
}

// ── WebSocket Endpoints ─────────────────────────────────────────────

// StreamLogs streams real-time logs via WebSocket.
func (h *APIHandler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Monitor client disconnect
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	// Stream logs using docker logs -f (direct API, avoids compose context issues)
	cmd := createCommand(ctx, "docker", "logs", "-f", "--tail", "100", "--timestamps", "ollama")

	// docker logs outputs container stderr to its own stderr;
	// merge stderr into stdout so we capture all log lines through one pipe.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("failed to get stdout pipe", "error", err)
		return
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		slog.Error("failed to start log command", "error", err)
		return
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			if err := conn.WriteMessage(websocket.TextMessage, scanner.Bytes()); err != nil {
				return
			}
		}
	}

	cmd.Wait()
}

// StreamPull streams model pull progress via WebSocket.
func (h *APIHandler) StreamPull(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Read model name from first message
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(msg, &req); err != nil || req.Name == "" {
		conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"invalid model name"}`))
		return
	}

	reader, err := h.ollama.PullModel(req.Name)
	if err != nil {
		errMsg, _ := json.Marshal(map[string]string{"error": err.Error()})
		conn.WriteMessage(websocket.TextMessage, errMsg)
		return
	}
	defer reader.Close()

	buf := bufio.NewReader(reader)
	for {
		line, err := buf.ReadBytes('\n')
		if len(line) > 0 {
			// Parse and add percent
			var progress map[string]any
			if json.Unmarshal(line, &progress) == nil {
				if total, ok := progress["total"].(float64); ok && total > 0 {
					if completed, ok := progress["completed"].(float64); ok {
						progress["percent"] = (completed / total) * 100
					}
				}
				enriched, _ := json.Marshal(progress)
				conn.WriteMessage(websocket.TextMessage, enriched)
			} else {
				conn.WriteMessage(websocket.TextMessage, line)
			}
		}
		if err == io.EOF {
			conn.WriteMessage(websocket.TextMessage, []byte(`{"status":"success","message":"模型下载完成"}`))
			break
		}
		if err != nil {
			errMsg, _ := json.Marshal(map[string]string{"error": err.Error()})
			conn.WriteMessage(websocket.TextMessage, errMsg)
			break
		}
	}
}

// StreamUpdate streams Ollama update progress (docker pull + recreate) via WebSocket.
// Flow: checking → up_to_date | update_available → (wait confirm) → pulling → waiting → done
func (h *APIHandler) StreamUpdate(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 600*time.Second)
	defer cancel()

	// Channel to receive client messages
	msgCh := make(chan string, 1)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			select {
			case msgCh <- string(msg):
			default:
			}
		}
	}()

	// Phase 1: Check versions
	conn.WriteJSON(map[string]any{
		"phase":  "checking",
		"status": "正在获取版本信息...",
	})

	currentVersion, _ := h.ollama.GetVersion()
	latestVersion, latestErr := h.ollama.GetLatestVersion()

	if latestErr != nil {
		slog.Warn("failed to get latest version, falling back to digest check", "error", latestErr)
		// 查询失败时回退到 digest 比对
		needsUpdate, _, _, checkErr := h.docker.CheckImageUpdate(ctx)
		if checkErr != nil {
			needsUpdate = true
		}
		if !needsUpdate {
			conn.WriteJSON(map[string]any{
				"phase":           "up_to_date",
				"status":          "success",
				"message":         "当前版本已是最新",
				"current_version": currentVersion,
				"latest_version":  currentVersion,
			})
			return
		}
		latestVersion = "未知（查询失败）"
	} else if currentVersion == latestVersion {
		// 版本号完全一致
		conn.WriteJSON(map[string]any{
			"phase":           "up_to_date",
			"status":          "success",
			"message":         "当前版本已是最新",
			"current_version": currentVersion,
			"latest_version":  latestVersion,
		})
		return
	}

	// Phase 1.5: Notify client that update is available, wait for confirmation
	conn.WriteJSON(map[string]any{
		"phase":           "update_available",
		"status":          "发现新版本",
		"current_version": currentVersion,
		"latest_version":  latestVersion,
	})

	// Wait for client to send "confirm" or "cancel"
	select {
	case <-ctx.Done():
		return
	case msg := <-msgCh:
		if msg != "confirm" {
			conn.WriteJSON(map[string]any{
				"phase":           "cancelled",
				"status":          "用户取消更新",
				"current_version": currentVersion,
				"latest_version":  latestVersion,
			})
			return
		}
	}

	// Phase 2: Stream docker pull progress
	conn.WriteJSON(map[string]any{
		"phase":  "pulling",
		"status": "开始拉取最新镜像...",
	})

	pullErr := h.docker.UpdateServiceStream(ctx, func(line string) {
		conn.WriteJSON(map[string]any{
			"phase":  "pulling",
			"status": line,
		})
	})

	if pullErr != nil {
		conn.WriteJSON(map[string]any{
			"phase":  "error",
			"status": "error",
			"error":  pullErr.Error(),
		})
		return
	}

	// Phase 3: Wait for Ollama API to be ready
	conn.WriteJSON(map[string]any{
		"phase":  "waiting",
		"status": "等待 Ollama 服务就绪...",
	})

	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			conn.WriteJSON(map[string]any{
				"phase": "error",
				"error": "更新超时或请求已取消",
			})
			return
		default:
		}
		if h.ollama.IsAPIReady() {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Phase 4: Done
	h.ollama.InvalidateVersionCache()
	newVersion, _ := h.ollama.GetVersion()
	conn.WriteJSON(map[string]any{
		"phase":       "done",
		"status":      "success",
		"message":     "更新完成",
		"old_version": currentVersion,
		"new_version": newVersion,
	})
}

// StreamServiceControl streams service control (start/stop/restart) progress via WebSocket.
// The action is specified in the "action" query parameter.
func (h *APIHandler) StreamServiceControl(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	if action != "start" && action != "stop" && action != "restart" {
		http.Error(w, "invalid action, must be start/stop/restart", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	timeout := 120 * time.Second
	if action == "stop" {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// Monitor client disconnect
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	actionNames := map[string]string{"start": "启动", "stop": "停止", "restart": "重启"}
	actionName := actionNames[action]

	// Phase 1: Starting operation
	conn.WriteJSON(map[string]any{
		"phase":  "operating",
		"action": action,
		"status": fmt.Sprintf("正在%s Ollama 服务...", actionName),
	})

	// Execute the streaming service method
	lineFn := func(line string) {
		conn.WriteJSON(map[string]any{
			"phase":  "operating",
			"action": action,
			"status": line,
		})
	}

	var opErr error
	switch action {
	case "start":
		opErr = h.docker.StartServiceStream(ctx, lineFn)
	case "stop":
		opErr = h.docker.StopServiceStream(ctx, lineFn)
	case "restart":
		opErr = h.docker.RestartServiceStream(ctx, lineFn)
	}

	if opErr != nil {
		conn.WriteJSON(map[string]any{
			"phase":  "error",
			"action": action,
			"status": "error",
			"error":  opErr.Error(),
		})
		return
	}

	// Phase 2: For start/restart, wait for Ollama API to be ready
	if action == "start" || action == "restart" {
		conn.WriteJSON(map[string]any{
			"phase":  "waiting",
			"action": action,
			"status": "等待 Ollama API 就绪...",
		})

		ready := false
		for i := 0; i < 60; i++ {
			select {
			case <-ctx.Done():
				conn.WriteJSON(map[string]any{
					"phase":  "error",
					"action": action,
					"error":  fmt.Sprintf("%s超时或请求已取消", actionName),
				})
				return
			default:
			}
			if h.ollama.IsAPIReady() {
				ready = true
				break
			}
			time.Sleep(2 * time.Second)
		}

		if !ready {
			conn.WriteJSON(map[string]any{
				"phase":  "error",
				"action": action,
				"error":  "Ollama API 未能在超时时间内就绪",
			})
			return
		}
	}

	// Phase 3: Done
	conn.WriteJSON(map[string]any{
		"phase":   "done",
		"action":  action,
		"status":  "success",
		"message": fmt.Sprintf("Ollama 服务%s成功", actionName),
	})
}

// ── Status WebSocket Hub ────────────────────────────────────────
// StatusHub manages multiple WebSocket connections for status streaming.
// A single data-collection goroutine serves all connected clients, avoiding
// redundant Docker/Ollama queries per client.

// statusClient represents a single WebSocket client connected to the status hub.
type statusClient struct {
	conn   *websocket.Conn
	mode   string // "full" or "lite"
	paused bool
	mu     sync.Mutex
}

// StatusHub manages all status WebSocket clients and a shared ticker.
// The background polling goroutine starts only when there are active
// (non-paused) clients and stops automatically when the last client
// disconnects or pauses, avoiding unnecessary queries to Ollama/Docker.
type StatusHub struct {
	handler *APIHandler
	clients map[*statusClient]struct{}
	mu      sync.RWMutex
	running bool         // whether the polling goroutine is active
	done    chan struct{} // signal to stop the polling goroutine
}

// NewStatusHub creates a new StatusHub.
func NewStatusHub(h *APIHandler) *StatusHub {
	return &StatusHub{
		handler: h,
		clients: make(map[*statusClient]struct{}),
	}
}

// hasActiveClients returns true if any client is connected and not paused.
// Caller must hold at least hub.mu.RLock.
func (hub *StatusHub) hasActiveClients() bool {
	for c := range hub.clients {
		c.mu.Lock()
		paused := c.paused
		c.mu.Unlock()
		if !paused {
			return true
		}
	}
	return false
}

// ensureRunning starts the polling goroutine if not already running and
// there are active clients. Must be called with hub.mu held (write lock).
func (hub *StatusHub) ensureRunning() {
	if hub.running {
		return
	}
	if !hub.hasActiveClients() {
		return
	}
	hub.done = make(chan struct{})
	hub.running = true
	slog.Info("StatusHub: polling started (clients connected)")
	go hub.run()
}

// ensureStopped stops the polling goroutine if there are no active clients.
// Must be called with hub.mu held (write lock).
func (hub *StatusHub) ensureStopped() {
	if !hub.running {
		return
	}
	if hub.hasActiveClients() {
		return
	}
	close(hub.done)
	hub.running = false
	slog.Info("StatusHub: polling stopped (no active clients)")
}

// run is the main loop that collects data and pushes to all clients.
func (hub *StatusHub) run() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-hub.done:
			return
		case <-ticker.C:
			hub.broadcast()
		}
	}
}

// broadcast collects status and sends to each connected client.
func (hub *StatusHub) broadcast() {
	hub.mu.RLock()
	if len(hub.clients) == 0 {
		hub.mu.RUnlock()
		return
	}

	// Determine if any client needs full status
	needsFull := false
	needsLite := false
	for c := range hub.clients {
		c.mu.Lock()
		if c.paused {
			c.mu.Unlock()
			continue
		}
		if c.mode == "full" {
			needsFull = true
		} else {
			needsLite = true
		}
		c.mu.Unlock()
	}
	hub.mu.RUnlock()

	if !needsFull && !needsLite {
		return
	}

	// Collect data once
	var fullData []byte
	var liteData []byte

	if needsFull {
		status := hub.collectFullStatus()
		msg := statusWSMessage{Type: "status", Mode: "full", Data: status}
		fullData, _ = json.Marshal(msg)
	}

	if needsLite && !needsFull {
		status := hub.collectLiteStatus()
		msg := statusWSMessage{Type: "status", Mode: "lite", Data: status}
		liteData, _ = json.Marshal(msg)
	} else if needsLite && needsFull {
		// If we already collected full status, also prepare lite for lite clients
		status := hub.collectLiteStatus()
		msg := statusWSMessage{Type: "status", Mode: "lite", Data: status}
		liteData, _ = json.Marshal(msg)
	}

	// Send to each client
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	for c := range hub.clients {
		c.mu.Lock()
		if c.paused {
			c.mu.Unlock()
			continue
		}
		mode := c.mode

		var payload []byte
		if mode == "full" {
			payload = fullData
		} else {
			payload = liteData
		}
		if payload == nil {
			c.mu.Unlock()
			continue
		}

		// WriteMessage under client lock to prevent concurrent writes
		err := c.conn.WriteMessage(websocket.TextMessage, payload)
		c.mu.Unlock()

		if err != nil {
			slog.Debug("status ws write error, removing client", "error", err)
			go hub.remove(c)
		}
	}
}

// collectFullStatus collects the full status (same as GetStatus handler).
// Version uses 60s cache; API readiness is inferred from ListRunningModels.
func (hub *StatusHub) collectFullStatus() model.ServiceStatus {
	h := hub.handler
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	status := model.ServiceStatus{}

	ch := make(chan collectResult, 6)
	go func() {
		info, err := h.docker.GetContainerInfo(ctx)
		ch <- collectResult{"container", info, err}
	}()
	go func() {
		usage, err := h.docker.GetResourceUsage(ctx)
		ch <- collectResult{"resources", usage, err}
	}()
	go func() {
		models, err := h.ollama.ListModels()
		ch <- collectResult{"models", models, err}
	}()
	go func() {
		running, err := h.ollama.ListRunningModels()
		ch <- collectResult{"running", running, err}
	}()
	go func() {
		gpus, err := h.docker.GetGPUInfo(ctx)
		ch <- collectResult{"gpu", gpus, err}
	}()
	go func() {
		disk, err := h.docker.GetDiskUsage(ctx)
		ch <- collectResult{"disk", disk, err}
	}()

	apiReady := false
	for i := 0; i < 6; i++ {
		r := <-ch
		switch r.key {
		case "container":
			status.Container = r.val.(model.ContainerInfo)
		case "resources":
			status.Resources = r.val.(model.ResourceUsage)
		case "models":
			if r.val != nil {
				status.Models = r.val.([]model.ModelInfo)
			}
		case "running":
			if r.err == nil {
				apiReady = true
			}
			if r.val != nil {
				status.RunningModels = r.val.([]model.RunningModel)
			}
		case "gpu":
			if r.val != nil {
				status.GPU = r.val.([]model.GPUInfo)
			}
		case "disk":
			status.Disk = r.val.(model.DiskUsage)
		}
	}

	// Version from cache (60s TTL)
	if ver, err := h.ollama.GetVersion(); err == nil {
		status.OllamaVersion = ver
	}

	status.APIReachable = apiReady
	h.correctHealthStatus(&status.Container, apiReady)
	status.ProjectVersion = h.version

	if vars, err := h.system.ReadEnvConfig(); err == nil {
		cfgMap := make(map[string]string)
		for _, v := range vars {
			cfgMap[v.Key] = v.Value
		}
		status.Config = model.ServiceConfig{
			BindAddress:     cfgMap["OLLAMA_BIND_ADDRESS"],
			Port:            cfgMap["OLLAMA_PORT"],
			Version:         cfgMap["OLLAMA_VERSION"],
			DataDir:         cfgMap["OLLAMA_DATA_DIR"],
			NumParallel:     cfgMap["OLLAMA_NUM_PARALLEL"],
			MaxQueue:        cfgMap["OLLAMA_MAX_QUEUE"],
			MaxLoadedModels: cfgMap["OLLAMA_MAX_LOADED_MODELS"],
			KeepAlive:       cfgMap["OLLAMA_KEEP_ALIVE"],
			ContextLength:   cfgMap["OLLAMA_CONTEXT_LENGTH"],
			KVCacheType:     cfgMap["OLLAMA_KV_CACHE_TYPE"],
			FlashAttention:  cfgMap["OLLAMA_FLASH_ATTENTION"],
			Debug:           cfgMap["OLLAMA_DEBUG"],
			CPUReservation:  cfgMap["OLLAMA_CPU_RESERVATION"],
			CPULimit:        cfgMap["OLLAMA_CPU_LIMIT"],
			MemReservation:  cfgMap["OLLAMA_MEM_RESERVATION"],
			MemLimit:        cfgMap["OLLAMA_MEM_LIMIT"],
			Timezone:        cfgMap["OLLAMA_TZ"],
		}
	}

	return status
}

// collectLiteStatus collects lightweight status (same as GetStatusLite handler).
// Optimized: version uses 60s cache (no per-poll API call), API readiness is
// inferred from ListRunningModels success (avoids separate GET / probe).
func (hub *StatusHub) collectLiteStatus() model.ServiceStatus {
	h := hub.handler
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	status := model.ServiceStatus{}

	ch := make(chan collectResult, 3)
	go func() {
		info, err := h.docker.GetContainerInfo(ctx)
		ch <- collectResult{"container", info, err}
	}()
	go func() {
		running, err := h.ollama.ListRunningModels()
		ch <- collectResult{"running", running, err}
	}()
	go func() {
		gpus, err := h.docker.GetGPUInfo(ctx)
		ch <- collectResult{"gpu", gpus, err}
	}()

	apiReady := false
	for i := 0; i < 3; i++ {
		r := <-ch
		switch r.key {
		case "container":
			status.Container = r.val.(model.ContainerInfo)
		case "running":
			if r.err == nil {
				apiReady = true // ListRunningModels succeeded → API is reachable
			}
			if r.val != nil {
				status.RunningModels = r.val.([]model.RunningModel)
			}
		case "gpu":
			if r.val != nil {
				status.GPU = r.val.([]model.GPUInfo)
			}
		}
	}

	// Version from cache (60s TTL, no extra API call per poll)
	if ver, err := h.ollama.GetVersion(); err == nil {
		status.OllamaVersion = ver
	}

	status.APIReachable = apiReady
	h.correctHealthStatus(&status.Container, apiReady)
	status.ProjectVersion = h.version

	return status
}

func (hub *StatusHub) add(c *statusClient) {
	hub.mu.Lock()
	hub.clients[c] = struct{}{}
	hub.ensureRunning()
	hub.mu.Unlock()
}

func (hub *StatusHub) remove(c *statusClient) {
	hub.mu.Lock()
	delete(hub.clients, c)
	hub.ensureStopped()
	hub.mu.Unlock()
	c.conn.Close()
}

// onClientStateChange should be called when a client's paused state changes.
// It checks whether the polling goroutine needs to start or stop.
func (hub *StatusHub) onClientStateChange() {
	hub.mu.Lock()
	if hub.hasActiveClients() {
		hub.ensureRunning()
	} else {
		hub.ensureStopped()
	}
	hub.mu.Unlock()
}

// statusWSMessage is the envelope for status WebSocket messages.
type statusWSMessage struct {
	Type string      `json:"type"` // "status"
	Mode string      `json:"mode"` // "full" or "lite"
	Data any `json:"data"`
}

// statusWSCommand is a client→server control message.
type statusWSCommand struct {
	Type string `json:"type"` // "subscribe", "pause", "resume"
	Mode string `json:"mode"` // "full" or "lite" (for subscribe)
}

// StreamStatus handles WebSocket connections for real-time status streaming.
// Protocol:
//   - Client connects, default mode = "lite", not paused
//   - Client sends: {"type":"subscribe","mode":"full"} to switch to full status
//   - Client sends: {"type":"pause"} to pause receiving data
//   - Client sends: {"type":"resume"} to resume
//   - Server pushes: {"type":"status","mode":"full|lite","data":{...}} every 5s
func (h *APIHandler) StreamStatus(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("status websocket upgrade failed", "error", err)
		return
	}

	client := &statusClient{
		conn: conn,
		mode: "lite",
	}

	h.statusHub.add(client)
	defer h.statusHub.remove(client)

	// Send an immediate status snapshot so client doesn't wait 5s
	go func() {
		liteStatus := h.statusHub.collectLiteStatus()
		msg := statusWSMessage{Type: "status", Mode: "lite", Data: liteStatus}
		data, _ := json.Marshal(msg)
		client.mu.Lock()
		client.conn.WriteMessage(websocket.TextMessage, data)
		client.mu.Unlock()
	}()

	// Read client commands until disconnect
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return // Client disconnected
		}

		var cmd statusWSCommand
		if err := json.Unmarshal(msg, &cmd); err != nil {
			continue
		}

		client.mu.Lock()
		switch cmd.Type {
		case "subscribe":
			if cmd.Mode == "full" || cmd.Mode == "lite" {
				client.mode = cmd.Mode
			}
		case "pause":
			client.paused = true
			client.mu.Unlock()
			h.statusHub.onClientStateChange()
			continue
		case "resume":
			client.paused = false
			client.mu.Unlock()
			h.statusHub.onClientStateChange()
			continue
		}
		client.mu.Unlock()
	}
}

// ── Chat History ────────────────────────────────────────────────

// ListChatSessions returns all saved sessions.
func (h *APIHandler) ListChatSessions(w http.ResponseWriter, r *http.Request) {
	sessions := h.metaStore().ListChatSessions()
	if sessions == nil {
		sessions = []service.ChatSession{}
	}
	jsonResponse(w, sessions)
}

// CreateChatSession creates a new session.
func (h *APIHandler) CreateChatSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	id := fmt.Sprintf("sess_%x", idBytes)
	if req.Title == "" {
		req.Title = "新对话"
	}
	h.metaStore().CreateChatSession(id, req.Title, req.Model)
	jsonResponse(w, map[string]string{"id": id, "title": req.Title})
}

// GetChatSession returns a session with all messages.
func (h *APIHandler) GetChatSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	msgs := h.metaStore().GetChatMessages(id)
	if msgs == nil {
		msgs = []service.ChatMsgRow{}
	}
	jsonResponse(w, msgs)
}

// DeleteChatSession deletes a session.
func (h *APIHandler) DeleteChatSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.metaStore().DeleteChatSession(id)
	jsonResponse(w, map[string]string{"status": "ok"})
}

// UpdateChatSessionTitle renames a session.
func (h *APIHandler) UpdateChatSessionTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Title string `json:"title"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	h.metaStore().UpdateChatSession(id, req.Title, req.Model)
	jsonResponse(w, map[string]string{"status": "ok"})
}

// SaveChatMessage saves a message to a session.
func (h *APIHandler) SaveChatMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Role    string   `json:"role"`
		Content string   `json:"content"`
		Files   []string `json:"files,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	h.metaStore().AddChatMessage(id, req.Role, req.Content, req.Files)
	jsonResponse(w, map[string]string{"status": "ok"})
}

// ExportChatSession exports a session as Markdown or JSON.
func (h *APIHandler) ExportChatSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	format := r.URL.Query().Get("format") // "md" or "json"
	if format == "" {
		format = "md"
	}

	msgs := h.metaStore().GetChatMessages(id)

	if format == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"chat_%s.json\"", id))
		json.NewEncoder(w).Encode(msgs)
		return
	}

	// Markdown export
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"chat_%s.md\"", id))

	fmt.Fprintf(w, "# 对话导出\n\n")
	for _, msg := range msgs {
		roleName := msg.Role
		switch msg.Role {
		case "user":
			roleName = "用户"
		case "assistant":
			roleName = "助手"
		case "system":
			roleName = "系统"
		}
		fmt.Fprintf(w, "## %s\n\n%s\n\n---\n\n", roleName, msg.Content)
	}
}

// metaStore is a convenience accessor for the metadata store.
func (h *APIHandler) metaStore() *service.MetadataStore {
	return h.ollama.MetaStore()
}

// ── Chat ────────────────────────────────────────────────────────

// StreamChat handles streaming chat via WebSocket.
// Protocol:
//   - Client sends: {"type":"chat","model":"...","messages":[...],"options":{...}}
//   - Server pushes: {"type":"token","content":"..."} per token
//   - Server sends:  {"type":"done","model":"...","eval_count":N,"total_duration":N} at end
//   - Client sends: {"type":"stop"} to abort generation
func (h *APIHandler) StreamChat(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("chat websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Single reader goroutine — gorilla/websocket does NOT support concurrent reads
	msgCh := make(chan []byte, 4)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				close(msgCh)
				return
			}
			msgCh <- msg
		}
	}()

	for {
		// Wait for next chat request
		rawMsg, ok := <-msgCh
		if !ok {
			return // client disconnected
		}

		var req model.ChatRequest
		if err := json.Unmarshal(rawMsg, &req); err != nil {
			conn.WriteJSON(map[string]any{"type": "error", "error": "invalid request"})
			continue
		}

		if req.Type == "stop" {
			continue
		}

		if req.Type != "chat" || req.Model == "" || len(req.Messages) == 0 {
			conn.WriteJSON(map[string]any{"type": "error", "error": "model and messages are required"})
			continue
		}

		// Build messages with file content and images injected
		ollamaMessages := make([]map[string]any, 0, len(req.Messages))
		for _, m := range req.Messages {
			content := m.Content
			var images []string
			if len(m.Files) > 0 {
				for _, fid := range m.Files {
					if f, ok := h.chatFiles.Get(fid); ok {
						if f.IsImage {
							images = append(images, f.Base64)
						} else {
							content += fmt.Sprintf("\n\n--- 文件: %s ---\n%s", f.Name, f.Content)
						}
					}
				}
			}
			msg := map[string]any{
				"role":    m.Role,
				"content": content,
			}
			if len(images) > 0 {
				msg["images"] = images
			}
			ollamaMessages = append(ollamaMessages, msg)
		}

		// Start streaming with cancellable context
		ctx, cancel := context.WithCancel(r.Context())

		reader, err := h.ollama.ChatStream(req.Model, ollamaMessages, req.Options, req.Format, req.KeepAlive, req.Think)
		if err != nil {
			conn.WriteJSON(map[string]any{"type": "error", "error": err.Error()})
			cancel()
			continue
		}

		// Stream tokens; monitor msgCh for stop command concurrently
		stopped := false
		doneCh := make(chan struct{})

		go func() {
			defer close(doneCh)
			buf := bufio.NewReader(reader)
			for {
				line, err := buf.ReadBytes('\n')
				if len(line) > 0 {
					var chunk map[string]any
					if json.Unmarshal(line, &chunk) == nil {
						if msgObj, ok := chunk["message"].(map[string]any); ok {
							if content, ok := msgObj["content"].(string); ok && content != "" {
								conn.WriteJSON(map[string]any{"type": "token", "content": content})
							}
							// Thinking/reasoning tokens (Ollama think mode)
							if thinking, ok := msgObj["thinking"].(string); ok && thinking != "" {
								conn.WriteJSON(map[string]any{"type": "thinking", "content": thinking})
							}
						}
						if done, ok := chunk["done"].(bool); ok && done {
							doneMsg := map[string]any{"type": "done", "model": req.Model}
							if v, ok := chunk["eval_count"]; ok {
								doneMsg["eval_count"] = v
							}
							if v, ok := chunk["total_duration"]; ok {
								doneMsg["total_duration"] = v
								// Track inference latency for perf monitor
								if td, ok := v.(float64); ok {
									h.lastInferMs.Store(int64(td / 1e6)) // ns → ms
								}
							}
							if v, ok := chunk["eval_duration"]; ok {
								doneMsg["eval_duration"] = v
							}
							conn.WriteJSON(doneMsg)
							return
						}
					}
				}
				if err != nil {
					if err != io.EOF && ctx.Err() == nil {
						conn.WriteJSON(map[string]any{"type": "error", "error": err.Error()})
					}
					return
				}
			}
		}()

		// Wait for either: stream done, client stop command, or client disconnect
	waitLoop:
		for {
			select {
			case <-doneCh:
				// Stream finished naturally
				break waitLoop
			case rawMsg, ok := <-msgCh:
				if !ok {
					// Client disconnected
					cancel()
					reader.Close()
					return
				}
				var stopReq model.ChatRequest
				if json.Unmarshal(rawMsg, &stopReq) == nil && stopReq.Type == "stop" {
					stopped = true
					cancel()
					reader.Close()
					<-doneCh // wait for reader goroutine to exit
					conn.WriteJSON(map[string]any{"type": "stopped"})
					break waitLoop
				}
			}
		}

		cancel()
		if !stopped {
			reader.Close()
		}
		// Ready for next chat request
	}
}

// UploadChatFile handles file upload for chat context (text files + images).
func (h *APIHandler) UploadChatFile(w http.ResponseWriter, r *http.Request) {
	// 10MB max
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		jsonError(w, http.StatusBadRequest, "文件过大或格式错误 (最大 10MB)")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "未找到上传文件")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "读取文件失败")
		return
	}

	// Generate unique ID
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	id := fmt.Sprintf("file_%x", idBytes)

	uf := &model.UploadedFile{
		ID:        id,
		Name:      header.Filename,
		Size:      header.Size,
		CreatedAt: time.Now(),
	}

	if service.IsImageFile(header.Filename) {
		// Image: base64 encode, store for injection into Ollama images field
		uf.IsImage = true
		uf.Base64 = base64.StdEncoding.EncodeToString(data)
		uf.Preview = "image"
	} else {
		// Text file: parse content
		content, err := service.ParseFileContent(header.Filename, data)
		if err != nil {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("文件解析失败: %s", err.Error()))
			return
		}
		uf.Content = content
		preview := content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		uf.Preview = preview
	}

	if err := h.chatFiles.Save(uf); err != nil {
		slog.Error("failed to save chat file", "error", err)
	}

	jsonResponse(w, uf)
}

// DownloadChatFile serves a file for download from persistent storage.
func (h *APIHandler) DownloadChatFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	filePath, ok := h.chatFiles.GetFilePath(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "文件不存在或已删除")
		return
	}

	f, _ := h.chatFiles.Get(id)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", f.Name))
	http.ServeFile(w, r, filePath)
}

// ── Performance Monitor ─────────────────────────────────────────

// StreamPerf handles the /api/ws/perf WebSocket endpoint.
// Protocol:
//   Client sends: {"type":"start","interval":3,"mode":"realtime|persistent"}
//   Client sends: {"type":"stop"}         → pause (stop sending, stop collecting)
//   Client sends: {"type":"interval","value":5}
//   Client sends: {"type":"mode","value":"realtime|paused|persistent"}
//   Server pushes: {"type":"perf","data":{...}}
//   Server pushes: {"type":"perf_batch","data":[{...},...]}  (flush buffer on reconnect)
//   Server pushes: {"type":"status","mode":"...","collecting":bool}
func (h *APIHandler) StreamPerf(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("perf websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	var (
		ticker     *time.Ticker
		stopCh     = make(chan struct{})
		collecting bool   // goroutine is running
		mode       string = "realtime" // realtime | paused | persistent
		interval          = 3 * time.Second
		buffer     []model.PerfMetrics // server-side buffer for persistent mode
		bufferMu   sync.Mutex
		sendable   bool = true // whether we can send to client
		mu         sync.Mutex
	)

	const maxBuffer = 1000 // max frames to buffer

	// Collect one frame, optionally send or buffer
	collectFrame := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		metrics := h.docker.GetPerfMetrics(ctx)
		cancel()

		// Inject latest inference event latency (from log parser, not StreamChat)
		if events := h.inferTracker.GetRecentEvents(1); len(events) > 0 {
			metrics.InferMs = events[0].DurationMs
		}

		mu.Lock()
		curMode := mode
		canSend := sendable
		mu.Unlock()

		if curMode == "persistent" && !canSend {
			// Buffer the frame
			bufferMu.Lock()
			if len(buffer) >= maxBuffer {
				buffer = buffer[1:] // drop oldest
			}
			buffer = append(buffer, metrics)
			bufferMu.Unlock()
			return
		}

		// Send to client
		mu.Lock()
		defer mu.Unlock()
		err := conn.WriteJSON(map[string]any{"type": "perf", "data": metrics})
		if err != nil {
			sendable = false
		}
	}

	// Flush buffer to client
	flushBuffer := func() {
		bufferMu.Lock()
		buf := buffer
		buffer = nil
		bufferMu.Unlock()

		if len(buf) == 0 {
			return
		}

		mu.Lock()
		defer mu.Unlock()
		conn.WriteJSON(map[string]any{"type": "perf_batch", "data": buf})
	}

	// Send status message
	sendStatus := func() {
		mu.Lock()
		defer mu.Unlock()
		conn.WriteJSON(map[string]any{
			"type":       "status",
			"mode":       mode,
			"collecting": collecting,
		})
	}

	startCollecting := func() {
		mu.Lock()
		if collecting {
			mu.Unlock()
			return
		}
		collecting = true
		sendable = true
		stopCh = make(chan struct{})
		ticker = time.NewTicker(interval)
		mu.Unlock()

		go func() {
			collectFrame() // immediate first frame
			for {
				select {
				case <-ticker.C:
					collectFrame()
				case <-stopCh:
					ticker.Stop()
					return
				}
			}
		}()
	}

	stopCollecting := func() {
		mu.Lock()
		defer mu.Unlock()
		if !collecting {
			return
		}
		collecting = false
		close(stopCh)
	}

	defer stopCollecting()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var cmd struct {
			Type     string `json:"type"`
			Interval int    `json:"interval"`
			Value    any    `json:"value"`
			Mode     string `json:"mode"`
		}
		if json.Unmarshal(msg, &cmd) != nil {
			continue
		}

		switch cmd.Type {
		case "start":
			if cmd.Interval >= 1 && cmd.Interval <= 60 {
				interval = time.Duration(cmd.Interval) * time.Second
			}
			if cmd.Mode == "persistent" || cmd.Mode == "realtime" {
				mu.Lock()
				mode = cmd.Mode
				mu.Unlock()
			} else {
				mu.Lock()
				mode = "realtime"
				mu.Unlock()
			}
			stopCollecting()
			// Flush any buffered data
			mu.Lock()
			sendable = true
			mu.Unlock()
			flushBuffer()
			startCollecting()
			sendStatus()

		case "stop":
			mu.Lock()
			mode = "paused"
			mu.Unlock()
			stopCollecting()
			sendStatus()

		case "mode":
			newMode, _ := cmd.Value.(string)
			if newMode == "" {
				newMode = cmd.Mode
			}
			switch newMode {
			case "realtime":
				mu.Lock()
				mode = "realtime"
				sendable = true
				mu.Unlock()
				if !collecting {
					startCollecting()
				}
				flushBuffer()
				sendStatus()
			case "paused":
				mu.Lock()
				mode = "paused"
				mu.Unlock()
				stopCollecting()
				sendStatus()
			case "persistent":
				mu.Lock()
				mode = "persistent"
				sendable = true
				mu.Unlock()
				if !collecting {
					startCollecting()
				}
				sendStatus()
			}

		case "resume":
			// Client reconnected / tab visible again
			mu.Lock()
			sendable = true
			curMode := mode
			mu.Unlock()
			flushBuffer()
			if curMode != "paused" && !collecting {
				startCollecting()
			}
			sendStatus()

		case "interval":
			if v, ok := cmd.Value.(float64); ok && int(v) >= 1 && int(v) <= 60 {
				interval = time.Duration(int(v)) * time.Second
				if collecting {
					stopCollecting()
					startCollecting()
				}
			}
		}
	}
}

// ── Benchmark ───────────────────────────────────────────────────

// benchmarkDimensions defines the evaluation test suite.
var benchmarkDimensions = []struct {
	ID     string
	Name   string
	Prompt string
	// Check function: returns score (0-10) and reasoning
	Check func(response string) (float64, string)
}{
	{
		ID:   "reasoning",
		Name: "逻辑推理",
		Prompt: `请解决以下逻辑推理题，给出完整的推理过程和最终答案：

一个房间里有3个开关，每个开关控制隔壁房间的一盏灯。你只能进入隔壁房间一次。请问如何确定每个开关对应哪盏灯？

要求：
1. 给出详细的推理步骤
2. 给出最终方案`,
		Check: func(resp string) (float64, string) {
			score := 0.0
			lower := strings.ToLower(resp)
			// 核心思路：利用灯泡的温度（开一段时间后关闭）
			if strings.Contains(lower, "温度") || strings.Contains(lower, "热") || strings.Contains(lower, "warm") || strings.Contains(lower, "heat") {
				score += 5
			}
			if strings.Contains(lower, "打开") && strings.Contains(lower, "关") {
				score += 2
			}
			if len(resp) > 100 {
				score += 2
			}
			if strings.Contains(resp, "步骤") || strings.Contains(resp, "方案") || strings.Contains(resp, "1") {
				score += 1
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("推理完整性: %.0f/10", score)
		},
	},
	{
		ID:   "math",
		Name: "数学计算",
		Prompt: `请计算以下数学问题，写出详细的计算过程：

1. 计算 17 × 23 + 45 ÷ 9 - 12² 的值
2. 一个圆的半径为5，求其面积（精确到小数点后两位）

要求给出每一步的计算过程。`,
		Check: func(resp string) (float64, string) {
			score := 0.0
			// 17*23 = 391, 45/9 = 5, 12² = 144, result = 391+5-144 = 252
			if strings.Contains(resp, "252") {
				score += 5
			} else if strings.Contains(resp, "391") || strings.Contains(resp, "144") {
				score += 2
			}
			// π*5² = 78.54
			if strings.Contains(resp, "78.5") {
				score += 5
			} else if strings.Contains(resp, "25π") || strings.Contains(resp, "25\\pi") {
				score += 3
			}
			return score, fmt.Sprintf("计算准确度: %.0f/10", score)
		},
	},
	{
		ID:   "code",
		Name: "代码能力",
		Prompt: `请用 Python 实现一个函数 is_palindrome(s)，判断一个字符串是否是回文。

要求：
1. 忽略大小写和非字母数字字符
2. 给出 3 个测试用例
3. 简要解释算法思路`,
		Check: func(resp string) (float64, string) {
			score := 0.0
			lower := strings.ToLower(resp)
			if strings.Contains(lower, "def ") && strings.Contains(lower, "palindrome") {
				score += 3
			}
			if strings.Contains(lower, "lower()") || strings.Contains(lower, "casefold") {
				score += 2
			}
			if strings.Contains(lower, "isalnum") || strings.Contains(lower, "isalpha") {
				score += 2
			}
			if strings.Contains(resp, "True") || strings.Contains(resp, "False") || strings.Contains(resp, "assert") {
				score += 2
			}
			if strings.Contains(resp, "```") {
				score += 1
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("代码质量: %.0f/10", score)
		},
	},
	{
		ID:   "writing",
		Name: "创意写作",
		Prompt: `请用不超过200字写一个微型故事，主题是"最后一盏路灯"。

要求：
1. 有完整的开头、发展和结尾
2. 包含至少一个比喻或拟人修辞
3. 营造出某种情感氛围`,
		Check: func(resp string) (float64, string) {
			score := 0.0
			runes := []rune(resp)
			if len(runes) > 50 {
				score += 3
			}
			if len(runes) > 100 {
				score += 2
			}
			// 检查修辞（比喻/拟人关键词）
			if strings.Contains(resp, "像") || strings.Contains(resp, "如同") || strings.Contains(resp, "仿佛") || strings.Contains(resp, "似") {
				score += 2
			}
			// 检查是否包含情感词汇
			emotionWords := []string{"孤独", "温暖", "寂寞", "希望", "黑暗", "光", "守候", "等待", "记忆", "沉默"}
			for _, w := range emotionWords {
				if strings.Contains(resp, w) {
					score += 0.5
				}
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("创意与表达: %.0f/10", score)
		},
	},
	{
		ID:   "instruction",
		Name: "指令遵循",
		Prompt: `请严格按照以下格式输出信息：

1. 用 JSON 格式输出一个包含3个中国城市的数组，每个城市包含 name（名称）和 population（人口，单位万）字段
2. JSON 必须是有效的，可以被解析器直接解析
3. 不要添加任何额外的解释文字，只输出 JSON`,
		Check: func(resp string) (float64, string) {
			score := 0.0
			// 检查是否包含有效 JSON
			cleaned := strings.TrimSpace(resp)
			// 去除 markdown code block
			if strings.HasPrefix(cleaned, "```") {
				if idx := strings.Index(cleaned, "\n"); idx >= 0 {
					cleaned = cleaned[idx+1:]
				}
				cleaned = strings.TrimSuffix(cleaned, "```")
				cleaned = strings.TrimSpace(cleaned)
			}
			var arr []map[string]any
			if json.Unmarshal([]byte(cleaned), &arr) == nil {
				score += 4
				if len(arr) == 3 {
					score += 2
				}
				for _, item := range arr {
					if _, ok := item["name"]; ok {
						score += 1
						break
					}
				}
				for _, item := range arr {
					if _, ok := item["population"]; ok {
						score += 1
						break
					}
				}
			} else if strings.Contains(resp, "name") && strings.Contains(resp, "population") {
				score += 2
			}
			// 检查是否有多余解释（应该只有 JSON）
			lines := strings.Split(strings.TrimSpace(resp), "\n")
			nonJsonLines := 0
			for _, l := range lines {
				l = strings.TrimSpace(l)
				if l != "" && !strings.HasPrefix(l, "[") && !strings.HasPrefix(l, "{") && !strings.HasPrefix(l, "]") && !strings.HasPrefix(l, "}") && !strings.HasPrefix(l, "\"") && !strings.HasPrefix(l, "```") {
					nonJsonLines++
				}
			}
			if nonJsonLines <= 1 {
				score += 2
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("指令遵循度: %.0f/10", score)
		},
	},
	{
		ID:   "chinese",
		Name: "中文能力",
		Prompt: `请完成以下中文语言任务：

1. 解释成语"画蛇添足"的含义，并用它造一个句子
2. 将以下句子改写为更加文雅的表达："这个东西很好用，我很喜欢"
3. 用一句话概括《西游记》的主要内容`,
		Check: func(resp string) (float64, string) {
			score := 0.0
			if strings.Contains(resp, "多余") || strings.Contains(resp, "多此一举") || strings.Contains(resp, "不必要") {
				score += 3
			}
			if strings.Contains(resp, "画蛇添足") && len(resp) > 50 {
				score += 1
			}
			// 检查是否有改写部分
			if strings.Contains(resp, "甚") || strings.Contains(resp, "颇") || strings.Contains(resp, "爱不释手") || strings.Contains(resp, "青睐") || strings.Contains(resp, "钟爱") {
				score += 3
			}
			// 检查西游记概括
			if (strings.Contains(resp, "唐僧") || strings.Contains(resp, "师徒")) && (strings.Contains(resp, "取经") || strings.Contains(resp, "西天")) {
				score += 3
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("中文理解与生成: %.0f/10", score)
		},
	},
}

// ── Inference Events ─────────────────────────────────────────────

// GetInferenceEvents returns recent inference events parsed from Ollama logs.
// GET /api/infer/events?window=300  (default 300 seconds)
func (h *APIHandler) GetInferenceEvents(w http.ResponseWriter, r *http.Request) {
	windowStr := r.URL.Query().Get("window")
	window := int64(300)
	if v, err := strconv.ParseInt(windowStr, 10, 64); err == nil && v > 0 {
		window = v
	}
	events := h.inferTracker.GetEvents(window)
	if events == nil {
		events = []model.InferEvent{}
	}
	jsonResponse(w, events)
}

// ── Benchmark: Offline task runner ───────────────────────────────
// Benchmark tasks run as background goroutines, independent of client connection.
// Progress is saved to SQLite after each dimension (断点续跑).

// benchmarkRunners tracks running benchmark goroutines (taskID → cancel func).
var benchmarkRunners = struct {
	sync.Mutex
	m map[int64]context.CancelFunc
}{m: make(map[int64]context.CancelFunc)}

// StartBenchmarkTask starts offline benchmark for given models.
// POST /api/benchmark/start  body: {"models":["model1","model2",...]}
func (h *APIHandler) StartBenchmarkTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Models) == 0 {
		jsonError(w, http.StatusBadRequest, "请选择至少一个模型")
		return
	}

	started := []map[string]any{}
	skipped := []string{}

	// Get currently running tasks from DB to deduplicate
	runningTasks := h.metaStore().GetRunningBenchmarks()
	runningModels := make(map[string]bool)
	for _, t := range runningTasks {
		mn, _ := t["model_name"].(string)
		if mn == "" {
			continue
		}
		// Verify goroutine is actually alive
		tid := int64(0)
		if f, ok := t["id"].(float64); ok {
			tid = int64(f)
		} else if i, ok := t["id"].(int64); ok {
			tid = i
		}
		benchmarkRunners.Lock()
		_, alive := benchmarkRunners.m[tid]
		benchmarkRunners.Unlock()
		if alive {
			runningModels[mn] = true
		} else if tid > 0 {
			// Goroutine dead but DB says running — mark as failed (orphaned task)
			h.metaStore().FailBenchmarkTask(tid, "cancelled")
		}
	}

	for _, modelName := range req.Models {
		// Skip if this model already has a running task with live goroutine
		if runningModels[modelName] {
			skipped = append(skipped, modelName)
			continue
		}

		// Get model digest for version tracking
		digest := ""
		if info, err := h.ollama.ShowModel(modelName); err == nil {
			if d, ok := info["digest"].(string); ok {
				digest = d
			}
		}

		totalDims := len(benchmarkDimensions)
		taskID := h.metaStore().CreateBenchmarkTask(modelName, digest, totalDims)
		if taskID == 0 {
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		benchmarkRunners.Lock()
		benchmarkRunners.m[taskID] = cancel
		benchmarkRunners.Unlock()

		go h.runBenchmarkOffline(ctx, taskID, modelName, nil, 0)

		started = append(started, map[string]any{"id": taskID, "model": modelName})
	}

	jsonResponse(w, map[string]any{"started": started, "skipped": skipped})
}

// runBenchmarkOffline executes benchmark in background goroutine.
func (h *APIHandler) runBenchmarkOffline(ctx context.Context, taskID int64, modelName string, resumeScores []map[string]any, resumeFromDim int) {
	defer func() {
		benchmarkRunners.Lock()
		delete(benchmarkRunners.m, taskID)
		benchmarkRunners.Unlock()
	}()

	scores := resumeScores
	var totalScore, totalTokSec float64
	for _, s := range scores {
		if sc, ok := s["score"].(float64); ok {
			totalScore += sc
		}
		if ts, ok := s["tok_per_sec"].(float64); ok {
			totalTokSec += ts
		}
	}

	for i, dim := range benchmarkDimensions {
		if i < resumeFromDim {
			continue // skip already completed dimensions (断点续跑)
		}

		select {
		case <-ctx.Done():
			h.metaStore().FailBenchmarkTask(taskID, "cancelled")
			return
		default:
		}

		slog.Info("benchmark: testing dimension", "model", modelName, "dim", dim.Name, "progress", i+1)

		dimCtx, dimCancel := context.WithTimeout(ctx, 5*time.Minute)
		startTime := time.Now()
		resp, err := h.ollama.GenerateChatWithContext(dimCtx, modelName, dim.Prompt)
		elapsed := time.Since(startTime).Milliseconds()
		dimCancel()

		var response string
		var tokenCount int
		var tokPerSec float64

		if err != nil {
			if ctx.Err() != nil {
				h.metaStore().FailBenchmarkTask(taskID, "cancelled")
				return
			}
			response = "ERROR: " + err.Error()
		} else {
			if msgObj, ok := resp["message"].(map[string]any); ok {
				response, _ = msgObj["content"].(string)
			}
			if ec, ok := resp["eval_count"].(float64); ok {
				tokenCount = int(ec)
			}
			if ed, ok := resp["eval_duration"].(float64); ok && ed > 0 {
				tokPerSec = float64(tokenCount) / (ed / 1e9)
			}
		}

		score, reasoning := dim.Check(response)

		// Store full response (up to 2000 chars for detail report)
		respFull := response
		if len([]rune(respFull)) > 2000 {
			respFull = string([]rune(respFull)[:2000]) + "..."
		}

		scoreEntry := map[string]any{
			"dimension_id": dim.ID, "name": dim.Name,
			"score": score, "max_score": 10,
			"response": respFull, "reasoning": reasoning,
			"token_count": tokenCount, "duration_ms": elapsed, "tok_per_sec": tokPerSec,
		}
		scores = append(scores, scoreEntry)
		totalScore += score
		totalTokSec += tokPerSec

		// Save progress after each dimension (断点续跑)
		scoresJSON, _ := json.Marshal(scores)
		avgTok := totalTokSec / float64(len(scores))
		h.metaStore().UpdateBenchmarkProgress(taskID, string(scoresJSON), totalScore, len(scores), avgTok)
	}

	// Mark completed
	maxTotal := len(benchmarkDimensions) * 10
	pct := (totalScore / float64(maxTotal)) * 100
	avgTok := totalTokSec / float64(len(scores))
	scoresJSON, _ := json.Marshal(scores)
	h.metaStore().CompleteBenchmarkTask(taskID, string(scoresJSON), totalScore, maxTotal, pct, avgTok)

	slog.Info("benchmark: completed", "model", modelName, "score", totalScore, "pct", pct)
}

// StopBenchmarkTask cancels running benchmark task(s).
// POST /api/benchmark/stop  body: {"model":"model_name"} or {"id":123}
// If model is given, stops ALL running tasks for that model.
// If id is given, stops that specific task.
func (h *APIHandler) StopBenchmarkTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
		ID    int64  `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || (req.Model == "" && req.ID == 0) {
		jsonError(w, http.StatusBadRequest, "model or id required")
		return
	}

	cancelled := 0

	if req.ID > 0 {
		// Cancel specific task by ID
		benchmarkRunners.Lock()
		if cancel, ok := benchmarkRunners.m[req.ID]; ok {
			cancel()
			delete(benchmarkRunners.m, req.ID)
		}
		benchmarkRunners.Unlock()
		h.metaStore().FailBenchmarkTask(req.ID, "cancelled")
		cancelled++
	} else {
		// Cancel all running tasks for this model name
		running := h.metaStore().GetRunningBenchmarks()
		benchmarkRunners.Lock()
		for _, t := range running {
			if t["model_name"] == req.Model {
				tid, _ := t["id"].(int64)
				if tid == 0 {
					if f, ok := t["id"].(float64); ok {
						tid = int64(f)
					}
				}
				if cancel, ok := benchmarkRunners.m[tid]; ok {
					cancel()
					delete(benchmarkRunners.m, tid)
				}
				// Mark as cancelled in DB immediately
				go h.metaStore().FailBenchmarkTask(tid, "cancelled")
				cancelled++
			}
		}
		benchmarkRunners.Unlock()
	}

	jsonResponse(w, map[string]any{"status": "ok", "cancelled": cancelled})
}

// GetBenchmarkTasks returns running + recent completed tasks.
// GET /api/benchmark/tasks
func (h *APIHandler) GetBenchmarkTasks(w http.ResponseWriter, r *http.Request) {
	all := h.metaStore().ListAllBenchmarkResults()
	if all == nil {
		all = []map[string]any{}
	}
	jsonResponse(w, all)
}

// StreamBenchmark pushes benchmark task status via WebSocket every 3s.
// Protocol:
//   Server pushes: {"type":"tasks","data":[...]}  every 3s while running tasks exist
//   Server pushes: {"type":"tasks","data":[...]}  once on connect (immediate snapshot)
//   Client sends: {"type":"stop"} → ignored here (use POST /api/benchmark/stop)
func (h *APIHandler) StreamBenchmark(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("benchmark websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	var mu sync.Mutex
	done := make(chan struct{})

	// Reader goroutine to detect disconnect
	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				close(done)
				return
			}
		}
	}()

	// Immediately send current state
	sendTasks := func() {
		all := h.metaStore().ListAllBenchmarkResults()
		if all == nil {
			all = []map[string]any{}
		}
		mu.Lock()
		conn.WriteJSON(map[string]any{"type": "tasks", "data": all})
		mu.Unlock()
	}

	sendTasks()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			sendTasks()
		}
	}
}

// ListBenchmarkResults returns the latest benchmark result for each model.
func (h *APIHandler) ListBenchmarkResults(w http.ResponseWriter, r *http.Request) {
	results := h.metaStore().ListBenchmarkResults()
	if results == nil {
		results = []map[string]any{}
	}
	jsonResponse(w, results)
}
