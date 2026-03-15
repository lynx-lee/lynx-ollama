package service

import (
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
	return &DockerService{cfg: cfg, composeCmd: composeCmd}
}

// runCommand executes a shell command and returns output.
func (s *DockerService) runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.cfg.ProjectDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runShell executes a shell command string.
func (s *DockerService) runShell(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = s.cfg.ProjectDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// GetContainerInfo returns the Ollama container status.
func (s *DockerService) GetContainerInfo(ctx context.Context) (model.ContainerInfo, error) {
	info := model.ContainerInfo{}

	// Container ID
	out, err := s.runCommand(ctx, "docker", "inspect", "--format", "{{.Id}}", "ollama")
	if err != nil {
		info.Status = "not_found"
		return info, nil
	}
	if len(out) > 12 {
		info.ID = out[:12]
	} else {
		info.ID = out
	}

	// Status
	out, _ = s.runCommand(ctx, "docker", "inspect", "--format", "{{.State.Status}}", "ollama")
	info.Status = out

	// Health
	out, _ = s.runCommand(ctx, "docker", "inspect", "--format", "{{.State.Health.Status}}", "ollama")
	info.Health = out

	// Image
	out, _ = s.runCommand(ctx, "docker", "inspect", "--format", "{{.Config.Image}}", "ollama")
	info.Image = out

	// Started at
	out, _ = s.runCommand(ctx, "docker", "inspect", "--format", "{{.State.StartedAt}}", "ollama")
	info.StartedAt = out
	if t, err := time.Parse(time.RFC3339Nano, out); err == nil {
		info.Uptime = formatDuration(time.Since(t))
	}

	// Ports
	out, _ = s.runCommand(ctx, "docker", "inspect", "--format", `{{range $p, $conf := .NetworkSettings.Ports}}{{$p}} -> {{range $conf}}{{.HostIp}}:{{.HostPort}}{{end}} {{end}}`, "ollama")
	info.Ports = out

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
	return s.runShell(ctx, fmt.Sprintf("cd %s && %s up -d 2>&1", s.cfg.ProjectDir, s.composeCmd))
}

// StopService stops the Ollama service.
func (s *DockerService) StopService(ctx context.Context) (string, error) {
	return s.runShell(ctx, fmt.Sprintf("cd %s && %s down 2>&1", s.cfg.ProjectDir, s.composeCmd))
}

// RestartService restarts the Ollama service with full recreation.
func (s *DockerService) RestartService(ctx context.Context) (string, error) {
	return s.runShell(ctx, fmt.Sprintf("cd %s && %s down && %s up -d 2>&1", s.cfg.ProjectDir, s.composeCmd, s.composeCmd))
}

// UpdateService pulls latest image and recreates container.
func (s *DockerService) UpdateService(ctx context.Context) (string, error) {
	return s.runShell(ctx, fmt.Sprintf("cd %s && docker pull ollama/ollama:latest && %s up -d --force-recreate 2>&1", s.cfg.ProjectDir, s.composeCmd))
}

// GetLogs returns recent container logs.
func (s *DockerService) GetLogs(ctx context.Context, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	return s.runShell(ctx, fmt.Sprintf("%s logs --tail=%d ollama 2>&1", s.composeCmd, lines))
}

// GetGPUInfo returns GPU information via nvidia-smi.
func (s *DockerService) GetGPUInfo(ctx context.Context) ([]model.GPUInfo, error) {
	out, err := s.runCommand(ctx, "nvidia-smi",
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
		gpus = append(gpus, model.GPUInfo{
			Index:       strings.TrimSpace(parts[0]),
			Name:        strings.TrimSpace(parts[1]),
			MemTotal:    strings.TrimSpace(parts[2]) + " MiB",
			MemUsed:     strings.TrimSpace(parts[3]) + " MiB",
			MemFree:     strings.TrimSpace(parts[4]) + " MiB",
			Utilization: strings.TrimSpace(parts[5]) + "%",
			Temperature: strings.TrimSpace(parts[6]) + "°C",
			Power:       strings.TrimSpace(parts[7]) + "W",
			PowerLimit:  strings.TrimSpace(parts[8]) + "W",
			Driver:      strings.TrimSpace(parts[9]),
		})
	}

	// Also get CUDA version
	cudaOut, _ := s.runShell(ctx, "nvidia-smi | grep 'CUDA Version' | awk '{print $NF}'")
	for i := range gpus {
		gpus[i].CUDA = strings.TrimSpace(cudaOut)
	}

	return gpus, nil
}

// GetDiskUsage returns disk usage information for the data directory.
func (s *DockerService) GetDiskUsage(ctx context.Context) (model.DiskUsage, error) {
	disk := model.DiskUsage{}

	// Model data size
	out, _ := s.runShell(ctx, fmt.Sprintf("du -sh %s 2>/dev/null | awk '{print $1}'", s.cfg.ProjectDir+"/ollama_data"))
	disk.ModelDataSize = out

	// Disk space
	out, _ = s.runShell(ctx, fmt.Sprintf("df -h %s | tail -1 | awk '{print $2\"|\"$3\"|\"$4\"|\"$5}'", s.cfg.ProjectDir))
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
	return s.runShell(ctx, fmt.Sprintf("cd %s && %s down --remove-orphans 2>&1", s.cfg.ProjectDir, s.composeCmd))
}

// CleanHard stops containers and removes images.
func (s *DockerService) CleanHard(ctx context.Context) (string, error) {
	return s.runShell(ctx, fmt.Sprintf("cd %s && %s down --remove-orphans --rmi all 2>&1 && docker image prune -f 2>&1", s.cfg.ProjectDir, s.composeCmd))
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
