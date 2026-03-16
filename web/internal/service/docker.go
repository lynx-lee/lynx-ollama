package service

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/lynxlee/lynx-ollama-web/internal/config"
	"github.com/lynxlee/lynx-ollama-web/internal/model"
)

// DockerService interacts with Docker for container management.
type DockerService struct {
	cfg        *config.Config
	composeCmd string

	// Short-lived cache for container info to avoid repeated docker inspect forks.
	// Multiple API handlers may request container info within the same polling cycle.
	containerCache    *model.ContainerInfo
	containerCacheAt  time.Time
	containerCacheTTL time.Duration
}

// NewDockerService creates a new DockerService.
func NewDockerService(cfg *config.Config) *DockerService {
	composeCmd := "docker compose"
	if _, err := exec.LookPath("docker"); err == nil {
		out, err := exec.Command("docker", "compose", "version").Output()
		if err != nil || len(out) == 0 {
			composeCmd = "docker-compose"
		}
	}
	return &DockerService{
		cfg:               cfg,
		composeCmd:        composeCmd,
		containerCacheTTL: 5 * time.Second,
	}
}

// runCommand executes a shell command and returns output.
func (s *DockerService) runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.cfg.ProjectDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runShell executes a shell command string with the working directory set to ProjectDir.
func (s *DockerService) runShell(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = s.cfg.ProjectDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// shellQuote quotes a string for safe use in shell commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// normalizeDockerValue cleans up docker inspect output values.
// Handles "<no value>" and empty strings by returning "".
func normalizeDockerValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "<no value>" || s == "<nil>" {
		return ""
	}
	return s
}

// InvalidateContainerCache clears the container info cache.
// Call this after operations that change container state (start/stop/restart).
func (s *DockerService) InvalidateContainerCache() {
	s.containerCache = nil
}

// GetContainerInfo returns the Ollama container status.
// Uses a single `docker inspect` call with a combined Go template to extract all
// fields at once (instead of 6 separate subprocess calls). Results are cached for
// a short TTL to avoid repeated forks within the same polling cycle.
func (s *DockerService) GetContainerInfo(ctx context.Context) (model.ContainerInfo, error) {
	// Return cached result if still fresh
	if s.containerCache != nil && time.Since(s.containerCacheAt) < s.containerCacheTTL {
		return *s.containerCache, nil
	}

	info := model.ContainerInfo{}

	// Single docker inspect call with all fields in one Go template.
	// Fields are separated by "|" for easy parsing.
	// Using "index" function to safely access Health.Status even if Health is nil.
	const inspectTemplate = `{{.Id}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}{{end}}|{{.Config.Image}}|{{.State.StartedAt}}|{{range $p, $conf := .NetworkSettings.Ports}}{{$p}} -> {{range $conf}}{{.HostIp}}:{{.HostPort}}{{end}} {{end}}`

	out, err := s.runCommand(ctx, "docker", "inspect", "--format", inspectTemplate, "ollama")
	if err != nil {
		info.Status = "not_found"
		// Cache the not_found result too (but with shorter TTL)
		s.containerCache = &info
		s.containerCacheAt = time.Now()
		return info, nil
	}

	parts := strings.SplitN(out, "|", 6)
	if len(parts) < 6 {
		// Fallback: if parsing fails, mark as unknown
		info.Status = "unknown"
		slog.Warn("unexpected docker inspect output", "output", out, "parts", len(parts))
		return info, nil
	}

	// Container ID (truncate to 12 chars)
	id := normalizeDockerValue(parts[0])
	if len(id) > 12 {
		info.ID = id[:12]
	} else {
		info.ID = id
	}

	// Status
	info.Status = normalizeDockerValue(parts[1])

	// Health (may be empty if container has no healthcheck configured)
	info.Health = normalizeDockerValue(parts[2])

	// Image
	info.Image = normalizeDockerValue(parts[3])

	// Started at + Uptime
	startedAt := normalizeDockerValue(parts[4])
	info.StartedAt = startedAt
	if t, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
		info.Uptime = formatDuration(time.Since(t))
	}

	// Ports
	info.Ports = normalizeDockerValue(parts[5])

	// Cache the result
	s.containerCache = &info
	s.containerCacheAt = time.Now()

	return info, nil
}

// GetResourceUsage returns container resource metrics.
func (s *DockerService) GetResourceUsage(ctx context.Context) (model.ResourceUsage, error) {
	usage := model.ResourceUsage{}
	out, err := s.runCommand(ctx, "docker", "stats", "--no-stream", "--format",
		"{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}|{{.NetIO}}|{{.BlockIO}}", "ollama")
	if err != nil {
		return usage, nil
	}
	parts := strings.Split(out, "|")
	if len(parts) >= 5 {
		usage.CPUPercent = strings.TrimSpace(parts[0])
		usage.MemUsage = strings.TrimSpace(parts[1])
		usage.MemPercent = strings.TrimSpace(parts[2])
		usage.NetIO = strings.TrimSpace(parts[3])
		usage.BlockIO = strings.TrimSpace(parts[4])
	}
	return usage, nil
}

// StartService starts the Ollama service.
func (s *DockerService) StartService(ctx context.Context) (string, error) {
	s.InvalidateContainerCache()
	dir := shellQuote(s.cfg.ProjectDir)
	out, err := s.runShell(ctx, fmt.Sprintf("cd %s && %s up -d 2>&1", dir, s.composeCmd))
	s.InvalidateContainerCache()
	return out, err
}

// StopService stops the Ollama service.
func (s *DockerService) StopService(ctx context.Context) (string, error) {
	s.InvalidateContainerCache()
	dir := shellQuote(s.cfg.ProjectDir)
	out, err := s.runShell(ctx, fmt.Sprintf("cd %s && %s down 2>&1", dir, s.composeCmd))
	s.InvalidateContainerCache()
	return out, err
}

// RestartService restarts the Ollama service with full recreation.
func (s *DockerService) RestartService(ctx context.Context) (string, error) {
	s.InvalidateContainerCache()
	dir := shellQuote(s.cfg.ProjectDir)
	out, err := s.runShell(ctx, fmt.Sprintf("cd %s && %s down && %s up -d 2>&1", dir, s.composeCmd, s.composeCmd))
	s.InvalidateContainerCache()
	return out, err
}

// CheckImageUpdate checks whether the remote ollama image has a newer version
// by comparing the local digest with the remote manifest digest.
// Returns (needsUpdate bool, localDigest string, remoteDigest string, err error).
func (s *DockerService) CheckImageUpdate(ctx context.Context) (bool, string, string, error) {
	// Get local image digest
	localDigest, err := s.runCommand(ctx, "docker", "image", "inspect",
		"--format", "{{index .RepoDigests 0}}", "ollama/ollama:latest")
	if err != nil {
		// Image not pulled yet → definitely needs update
		return true, "", "", nil
	}
	// Extract digest part after @
	if idx := strings.Index(localDigest, "@"); idx >= 0 {
		localDigest = localDigest[idx+1:]
	}

	// Use docker manifest inspect to get remote digest (docker ≥ 20.10)
	remoteOut, err := s.runCommand(ctx, "docker", "manifest", "inspect",
		"--verbose", "ollama/ollama:latest")
	if err != nil {
		// manifest inspect not supported or network error → cannot determine, proceed with update
		slog.Warn("cannot check remote manifest, proceeding with pull", "error", err)
		return true, localDigest, "", nil
	}

	// The remote manifest output contains the digest; do a simple comparison
	// by checking if our local digest appears in the remote manifest output
	if localDigest != "" && strings.Contains(remoteOut, localDigest) {
		return false, localDigest, localDigest, nil
	}

	return true, localDigest, "new", nil
}

// UpdateServiceStream pulls the latest ollama image with streaming output,
// sending each line of docker pull progress to the provided lineFn callback.
// After pull completes, it recreates the ollama container.
func (s *DockerService) UpdateServiceStream(ctx context.Context, lineFn func(line string)) error {
	s.InvalidateContainerCache()
	defer s.InvalidateContainerCache()

	// Phase 1: docker pull with streaming output
	cmd := exec.CommandContext(ctx, "docker", "pull", "ollama/ollama:latest")
	cmd.Dir = s.cfg.ProjectDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建管道失败: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 docker pull 失败: %w", err)
	}

	// Stream each line
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		lineFn(scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker pull 失败: %w", err)
	}

	// Phase 2: recreate ollama container
	lineFn("正在重建 Ollama 容器...")
	out, err := s.recreateOllama(ctx)
	if err != nil {
		return fmt.Errorf("重建容器失败: %s - %w", out, err)
	}
	lineFn("容器重建完成")

	return nil
}

// recreateOllama force-recreates only the ollama service container,
// avoiding recreation of the web container (which serves this request).
func (s *DockerService) recreateOllama(ctx context.Context) (string, error) {
	dir := shellQuote(s.cfg.ProjectDir)
	return s.runShell(ctx, fmt.Sprintf("cd %s && %s up -d --force-recreate ollama 2>&1", dir, s.composeCmd))
}

// UpdateService is a non-streaming version kept for backward compatibility.
func (s *DockerService) UpdateService(ctx context.Context) (string, error) {
	s.InvalidateContainerCache()
	dir := shellQuote(s.cfg.ProjectDir)
	out, err := s.runShell(ctx, fmt.Sprintf("cd %s && docker pull ollama/ollama:latest && %s up -d --force-recreate ollama 2>&1", dir, s.composeCmd))
	s.InvalidateContainerCache()
	return out, err
}


// GetLogs returns recent container logs.
// Uses "docker logs" directly instead of "docker compose logs" to avoid
// compose project context issues when running inside a container.
func (s *DockerService) GetLogs(ctx context.Context, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	tail := fmt.Sprintf("%d", lines)
	return s.runCommand(ctx, "docker", "logs", "--tail", tail, "--timestamps", "ollama")
}

// GetGPUInfo returns GPU information via nvidia-smi inside the ollama container.
// The web container does not have GPU device access, so we exec into the ollama
// container which has the NVIDIA runtime configured.
func (s *DockerService) GetGPUInfo(ctx context.Context) ([]model.GPUInfo, error) {
	out, err := s.runCommand(ctx, "docker", "exec", "ollama",
		"nvidia-smi",
		"--query-gpu=index,name,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu,power.draw,power.limit,driver_version",
		"--format=csv,noheader,nounits")
	if err != nil {
		slog.Warn("nvidia-smi failed", "error", err)
		return nil, nil
	}

	var gpus []model.GPUInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ", ")
		if len(parts) < 10 {
			continue
		}

		memTotal := strings.TrimSpace(parts[2])
		memUsed := strings.TrimSpace(parts[3])
		memFree := strings.TrimSpace(parts[4])
		gpuName := strings.TrimSpace(parts[1])

		// Detect unified memory architecture (GB10, GH200, Grace Hopper, GB200, Jetson)
		// nvidia-smi returns "[N/A]" for memory on these platforms
		isUnified := isUnifiedMemoryGPU(gpuName) || memTotal == "[N/A]"

		gpu := model.GPUInfo{
			Index:        strings.TrimSpace(parts[0]),
			Name:         gpuName,
			MemTotal:     memTotal + " MiB",
			MemUsed:      memUsed + " MiB",
			MemFree:      memFree + " MiB",
			Utilization:  strings.TrimSpace(parts[5]) + "%",
			Temperature:  strings.TrimSpace(parts[6]) + "°C",
			Power:        strings.TrimSpace(parts[7]) + "W",
			PowerLimit:   strings.TrimSpace(parts[8]) + "W",
			Driver:       strings.TrimSpace(parts[9]),
			IsUnifiedMem: isUnified,
		}

		if isUnified {
			// Get system total memory from the container
			sysMem := s.getSystemMemory(ctx)
			if sysMem != "" {
				gpu.UnifiedMemTotal = sysMem
				gpu.MemTotal = sysMem + " (统一内存)"
				// For unified memory, try to get used memory from /proc/meminfo
				usedMem := s.getUsedMemory(ctx)
				if usedMem != "" {
					gpu.MemUsed = usedMem
				} else {
					gpu.MemUsed = "N/A"
				}
				gpu.MemFree = "N/A"
			}
		} else {
			// Normal GPU: append MiB suffix for non-[N/A] values
			if memTotal != "[N/A]" {
				gpu.MemTotal = memTotal + " MiB"
			}
			if memUsed != "[N/A]" {
				gpu.MemUsed = memUsed + " MiB"
			}
			if memFree != "[N/A]" {
				gpu.MemFree = memFree + " MiB"
			}
		}

		gpus = append(gpus, gpu)
	}

	// Also get CUDA version from the ollama container
	cudaOut, _ := s.runCommand(ctx, "docker", "exec", "ollama",
		"bash", "-c", "nvidia-smi | grep 'CUDA Version' | sed 's/.*CUDA Version: *//;s/ .*//'")
	for i := range gpus {
		gpus[i].CUDA = strings.TrimSpace(cudaOut)
	}

	return gpus, nil
}

// isUnifiedMemoryGPU checks if the GPU name indicates a unified memory architecture.
func isUnifiedMemoryGPU(name string) bool {
	lower := strings.ToLower(name)
	keywords := []string{"gb10", "gh200", "grace", "gb200", "jetson"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// getSystemMemory reads total system memory from the ollama container.
func (s *DockerService) getSystemMemory(ctx context.Context) string {
	out, err := s.runCommand(ctx, "docker", "exec", "ollama",
		"bash", "-c", "awk '/MemTotal/ {printf \"%.0f\", $2/1024/1024}' /proc/meminfo")
	if err != nil || strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "0" {
		return ""
	}
	return strings.TrimSpace(out) + " GiB"
}

// getUsedMemory reads used system memory from the ollama container.
func (s *DockerService) getUsedMemory(ctx context.Context) string {
	out, err := s.runCommand(ctx, "docker", "exec", "ollama",
		"bash", "-c", "awk '/MemTotal/{t=$2} /MemAvailable/{a=$2} END{printf \"%.1f GiB\", (t-a)/1024/1024}' /proc/meminfo")
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	return strings.TrimSpace(out)
}

// GetDiskUsage returns disk usage information for the data directory.
func (s *DockerService) GetDiskUsage(ctx context.Context) (model.DiskUsage, error) {
	disk := model.DiskUsage{}

	// Model data size
	dataDir := shellQuote(s.cfg.ProjectDir + "/ollama_data")
	out, _ := s.runShell(ctx, fmt.Sprintf("du -sh %s 2>/dev/null | awk '{print $1}'", dataDir))
	disk.ModelDataSize = out

	// Disk space
	dir := shellQuote(s.cfg.ProjectDir)
	out, _ = s.runShell(ctx, fmt.Sprintf("df -h %s | tail -1 | awk '{print $2\"|\"$3\"|\"$4\"|\"$5}'", dir))
	parts := strings.Split(out, "|")
	if len(parts) >= 4 {
		disk.TotalSpace = parts[0]
		disk.UsedSpace = parts[1]
		disk.AvailSpace = parts[2]
		disk.UsePercent = parts[3]
	}

	return disk, nil
}

// CleanSoft stops containers only.
func (s *DockerService) CleanSoft(ctx context.Context) (string, error) {
	s.InvalidateContainerCache()
	dir := shellQuote(s.cfg.ProjectDir)
	out, err := s.runShell(ctx, fmt.Sprintf("cd %s && %s down --remove-orphans 2>&1", dir, s.composeCmd))
	s.InvalidateContainerCache()
	return out, err
}

// CleanHard stops containers and removes images.
func (s *DockerService) CleanHard(ctx context.Context) (string, error) {
	s.InvalidateContainerCache()
	dir := shellQuote(s.cfg.ProjectDir)
	out, err := s.runShell(ctx, fmt.Sprintf("cd %s && %s down --remove-orphans --rmi all 2>&1 && docker image prune -f 2>&1", dir, s.composeCmd))
	s.InvalidateContainerCache()
	return out, err
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%d天%d小时%d分钟", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时%d分钟", hours, minutes)
	}
	return fmt.Sprintf("%d分钟", minutes)
}
