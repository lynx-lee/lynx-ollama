package handler

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lynxlee/lynx-ollama-web/internal/config"
	"github.com/lynxlee/lynx-ollama-web/internal/model"
	"github.com/lynxlee/lynx-ollama-web/internal/service"
)

var upgrader = websocket.Upgrader{
	// CheckOrigin defaults to checking the Origin header matches the Host.
	// WebSocket auth is handled by the API key middleware (via ?key= query param).
}

// APIHandler holds all API endpoint handlers.
type APIHandler struct {
	ollama  *service.OllamaService
	docker  *service.DockerService
	system  *service.SystemService
	cfg     *config.Config
	version string
}

// NewAPIHandler creates a new APIHandler.
func NewAPIHandler(ollama *service.OllamaService, docker *service.DockerService, system *service.SystemService, cfg *config.Config, version string) *APIHandler {
	return &APIHandler{ollama: ollama, docker: docker, system: system, cfg: cfg, version: version}
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
		if info.Health == "starting" && h.ollama.IsAPIReady() {
			info.Health = "healthy"
		}
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

	status.APIReachable = h.ollama.IsAPIReady()
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

// GetStatusLite returns a lightweight status snapshot (container + API + running models).
// This is used for background polling when the user is NOT on the Dashboard page,
// significantly reducing the number of requests to Ollama and Docker.
func (h *APIHandler) GetStatusLite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	status := model.ServiceStatus{}

	// Only 3 lightweight queries (vs. 7 in full status)
	ch := make(chan collectResult, 3)

	go func() {
		info, err := h.docker.GetContainerInfo(ctx)
		if info.Health == "starting" && h.ollama.IsAPIReady() {
			info.Health = "healthy"
		}
		ch <- collectResult{"container", info, err}
	}()
	go func() {
		running, err := h.ollama.ListRunningModels()
		ch <- collectResult{"running", running, err}
	}()
	go func() {
		version, err := h.ollama.GetVersion()
		ch <- collectResult{"version", version, err}
	}()

	for i := 0; i < 3; i++ {
		r := <-ch
		switch r.key {
		case "container":
			status.Container = r.val.(model.ContainerInfo)
		case "running":
			if r.val != nil {
				status.RunningModels = r.val.([]model.RunningModel)
			}
		case "version":
			if r.val != nil {
				status.OllamaVersion = r.val.(string)
			}
		}
	}

	status.APIReachable = h.ollama.IsAPIReady()
	status.ProjectVersion = h.version

	jsonResponse(w, status)
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

	// Limit batch size to prevent abuse
	maxBatch := 10
	if len(req.Items) > maxBatch {
		req.Items = req.Items[:maxBatch]
	}

	results := h.ollama.TranslateDescriptions(req.Items)
	jsonResponse(w, results)
}

// ── Health & Diagnostics ────────────────────────────────────────────

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
