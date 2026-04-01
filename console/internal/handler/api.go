package handler

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	cfg       *config.Config
	version   string
	statusHub *StatusHub
}

// NewAPIHandler creates a new APIHandler.
func NewAPIHandler(ollama *service.OllamaService, docker *service.DockerService, system *service.SystemService, cfg *config.Config, version string) *APIHandler {
	h := &APIHandler{ollama: ollama, docker: docker, system: system, cfg: cfg, version: version}
	h.statusHub = NewStatusHub(h)
	return h
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
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
	val interface{}
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
			if r.val != nil {
				status.RunningModels = r.val.([]model.RunningModel)
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

	// Single IsAPIReady call (cached internally, avoids redundant probes)
	apiReady := h.ollama.IsAPIReady()
	status.APIReachable = apiReady

	// Correct health status based on actual API reachability:
	// - Docker may report "starting" during start_period even if API is already up
	// - Docker may report "unhealthy" due to transient probe failures
	// - Health may be empty if container was started without healthcheck config
	h.correctHealthStatus(&status.Container, apiReady)

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
			var progress map[string]interface{}
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
	conn.WriteJSON(map[string]interface{}{
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
			conn.WriteJSON(map[string]interface{}{
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
		conn.WriteJSON(map[string]interface{}{
			"phase":           "up_to_date",
			"status":          "success",
			"message":         "当前版本已是最新",
			"current_version": currentVersion,
			"latest_version":  latestVersion,
		})
		return
	}

	// Phase 1.5: Notify client that update is available, wait for confirmation
	conn.WriteJSON(map[string]interface{}{
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
			conn.WriteJSON(map[string]interface{}{
				"phase":           "cancelled",
				"status":          "用户取消更新",
				"current_version": currentVersion,
				"latest_version":  latestVersion,
			})
			return
		}
	}

	// Phase 2: Stream docker pull progress
	conn.WriteJSON(map[string]interface{}{
		"phase":  "pulling",
		"status": "开始拉取最新镜像...",
	})

	pullErr := h.docker.UpdateServiceStream(ctx, func(line string) {
		conn.WriteJSON(map[string]interface{}{
			"phase":  "pulling",
			"status": line,
		})
	})

	if pullErr != nil {
		conn.WriteJSON(map[string]interface{}{
			"phase":  "error",
			"status": "error",
			"error":  pullErr.Error(),
		})
		return
	}

	// Phase 3: Wait for Ollama API to be ready
	conn.WriteJSON(map[string]interface{}{
		"phase":  "waiting",
		"status": "等待 Ollama 服务就绪...",
	})

	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			conn.WriteJSON(map[string]interface{}{
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
	conn.WriteJSON(map[string]interface{}{
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
	conn.WriteJSON(map[string]interface{}{
		"phase":  "operating",
		"action": action,
		"status": fmt.Sprintf("正在%s Ollama 服务...", actionName),
	})

	// Execute the streaming service method
	lineFn := func(line string) {
		conn.WriteJSON(map[string]interface{}{
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
		conn.WriteJSON(map[string]interface{}{
			"phase":  "error",
			"action": action,
			"status": "error",
			"error":  opErr.Error(),
		})
		return
	}

	// Phase 2: For start/restart, wait for Ollama API to be ready
	if action == "start" || action == "restart" {
		conn.WriteJSON(map[string]interface{}{
			"phase":  "waiting",
			"action": action,
			"status": "等待 Ollama API 就绪...",
		})

		ready := false
		for i := 0; i < 60; i++ {
			select {
			case <-ctx.Done():
				conn.WriteJSON(map[string]interface{}{
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
			conn.WriteJSON(map[string]interface{}{
				"phase":  "error",
				"action": action,
				"error":  "Ollama API 未能在超时时间内就绪",
			})
			return
		}
	}

	// Phase 3: Done
	conn.WriteJSON(map[string]interface{}{
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
		c.mu.Unlock()

		var payload []byte
		if mode == "full" {
			payload = fullData
		} else {
			payload = liteData
		}
		if payload == nil {
			continue
		}

		if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
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
	Data interface{} `json:"data"`
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

// ── Chat ────────────────────────────────────────────────────────

// chatFileStore holds uploaded files in memory with TTL-based cleanup.
var chatFileStore = struct {
	mu    sync.RWMutex
	files map[string]*model.UploadedFile
}{files: make(map[string]*model.UploadedFile)}

func init() {
	// Cleanup expired files every 10 minutes
	go func() {
		for range time.Tick(10 * time.Minute) {
			chatFileStore.mu.Lock()
			for id, f := range chatFileStore.files {
				if time.Since(f.CreatedAt) > 1*time.Hour {
					delete(chatFileStore.files, id)
				}
			}
			chatFileStore.mu.Unlock()
		}
	}()
}

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

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return // client disconnected
		}

		var req model.ChatRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			conn.WriteJSON(map[string]any{"type": "error", "error": "invalid request"})
			continue
		}

		if req.Type == "stop" {
			continue // stop is handled by cancelling the context below
		}

		if req.Type != "chat" || req.Model == "" || len(req.Messages) == 0 {
			conn.WriteJSON(map[string]any{"type": "error", "error": "model and messages are required"})
			continue
		}

		// Build messages with file content injected
		ollamaMessages := make([]map[string]string, 0, len(req.Messages))
		for _, m := range req.Messages {
			content := m.Content
			// Inject uploaded file contents
			if len(m.Files) > 0 {
				chatFileStore.mu.RLock()
				for _, fid := range m.Files {
					if f, ok := chatFileStore.files[fid]; ok {
						content += fmt.Sprintf("\n\n--- 文件: %s ---\n%s", f.Name, f.Content)
					}
				}
				chatFileStore.mu.RUnlock()
			}
			ollamaMessages = append(ollamaMessages, map[string]string{
				"role":    m.Role,
				"content": content,
			})
		}

		// Start streaming
		ctx, cancel := context.WithCancel(r.Context())

		// Listen for stop command in background
		stopCh := make(chan struct{}, 1)
		go func() {
			for {
				_, stopMsg, err := conn.ReadMessage()
				if err != nil {
					cancel()
					return
				}
				var stopReq model.ChatRequest
				if json.Unmarshal(stopMsg, &stopReq) == nil && stopReq.Type == "stop" {
					close(stopCh)
					cancel()
					return
				}
			}
		}()

		reader, err := h.ollama.ChatStream(req.Model, ollamaMessages, req.Options)
		if err != nil {
			conn.WriteJSON(map[string]any{"type": "error", "error": err.Error()})
			cancel()
			continue
		}

		// Stream tokens to client
		buf := bufio.NewReader(reader)
		for {
			select {
			case <-ctx.Done():
				reader.Close()
				conn.WriteJSON(map[string]any{"type": "stopped"})
				goto nextMessage
			default:
			}

			line, err := buf.ReadBytes('\n')
			if len(line) > 0 {
				var chunk map[string]any
				if json.Unmarshal(line, &chunk) == nil {
					// Extract token content from Ollama response
					if msgObj, ok := chunk["message"].(map[string]any); ok {
						if content, ok := msgObj["content"].(string); ok && content != "" {
							conn.WriteJSON(map[string]any{"type": "token", "content": content})
						}
					}
					// Check if done
					if done, ok := chunk["done"].(bool); ok && done {
						doneMsg := map[string]any{"type": "done", "model": req.Model}
						if v, ok := chunk["eval_count"]; ok {
							doneMsg["eval_count"] = v
						}
						if v, ok := chunk["total_duration"]; ok {
							doneMsg["total_duration"] = v
						}
						if v, ok := chunk["eval_duration"]; ok {
							doneMsg["eval_duration"] = v
						}
						conn.WriteJSON(doneMsg)
						break
					}
				}
			}
			if err != nil {
				if err != io.EOF {
					conn.WriteJSON(map[string]any{"type": "error", "error": err.Error()})
				}
				break
			}
		}
		reader.Close()
		cancel()

	nextMessage:
		// Ready for next chat request on same connection
	}
}

// UploadChatFile handles file upload for chat context.
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

	content, err := service.ParseFileContent(header.Filename, data)
	if err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("文件解析失败: %s", err.Error()))
		return
	}

	// Generate unique ID
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	id := fmt.Sprintf("file_%x", idBytes)

	preview := content
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}

	uf := &model.UploadedFile{
		ID:        id,
		Name:      header.Filename,
		Size:      header.Size,
		Content:   content,
		Preview:   preview,
		CreatedAt: time.Now(),
	}

	chatFileStore.mu.Lock()
	chatFileStore.files[id] = uf
	chatFileStore.mu.Unlock()

	jsonResponse(w, uf)
}

// DownloadChatFile serves a generated file for download.
func (h *APIHandler) DownloadChatFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	chatFileStore.mu.RLock()
	f, ok := chatFileStore.files[id]
	chatFileStore.mu.RUnlock()

	if !ok {
		jsonError(w, http.StatusNotFound, "文件不存在或已过期")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", f.Name))
	w.Write([]byte(f.Content))
}
