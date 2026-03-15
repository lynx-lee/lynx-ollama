package service

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/lynxlee/lynx-ollama-web/internal/config"
	"github.com/lynxlee/lynx-ollama-web/internal/model"
)

// SystemService handles system-level operations (config, health, etc).
type SystemService struct {
	cfg *config.Config
}

// NewSystemService creates a new SystemService.
func NewSystemService(cfg *config.Config) *SystemService {
	return &SystemService{cfg: cfg}
}

// ReadEnvConfig reads the .env file and returns all variables.
func (s *SystemService) ReadEnvConfig() ([]model.EnvVariable, error) {
	envPath := s.cfg.EnvFilePath()
	f, err := os.Open(envPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open .env: %w", err)
	}
	defer f.Close()

	descriptions := map[string]string{
		"OLLAMA_BIND_ADDRESS":      "绑定地址",
		"OLLAMA_PORT":              "服务端口",
		"OLLAMA_VERSION":           "Docker 镜像版本",
		"OLLAMA_DATA_DIR":          "数据目录",
		"OLLAMA_NUM_PARALLEL":      "每模型并行请求数",
		"OLLAMA_MAX_QUEUE":         "请求队列最大长度",
		"OLLAMA_MAX_LOADED_MODELS": "同时加载模型数",
		"OLLAMA_KEEP_ALIVE":        "模型保持时间",
		"OLLAMA_CONTEXT_LENGTH":    "上下文长度",
		"OLLAMA_KV_CACHE_TYPE":     "KV 缓存类型",
		"OLLAMA_CPU_RESERVATION":   "CPU 预留核心数",
		"OLLAMA_CPU_LIMIT":         "CPU 限制核心数",
		"OLLAMA_MEM_RESERVATION":   "内存预留",
		"OLLAMA_MEM_LIMIT":         "内存限制",
		"OLLAMA_START_PERIOD":      "健康检查启动等待期",
		"OLLAMA_DEBUG":             "日志级别",
		"OLLAMA_TZ":                "容器时区",
		"OLLAMA_FLASH_ATTENTION":   "Flash Attention",
	}

	defaults := map[string]string{
		"OLLAMA_BIND_ADDRESS":      "127.0.0.1",
		"OLLAMA_PORT":              "11434",
		"OLLAMA_VERSION":           "latest",
		"OLLAMA_DATA_DIR":          "/opt/ai/ollama/ollama_data",
		"OLLAMA_NUM_PARALLEL":      "8",
		"OLLAMA_MAX_QUEUE":         "512",
		"OLLAMA_MAX_LOADED_MODELS": "4",
		"OLLAMA_KEEP_ALIVE":        "30m",
		"OLLAMA_CONTEXT_LENGTH":    "131072",
		"OLLAMA_KV_CACHE_TYPE":     "q8_0",
		"OLLAMA_CPU_RESERVATION":   "4.0",
		"OLLAMA_CPU_LIMIT":         "10.0",
		"OLLAMA_MEM_RESERVATION":   "16G",
		"OLLAMA_MEM_LIMIT":         "120G",
		"OLLAMA_START_PERIOD":      "120s",
		"OLLAMA_DEBUG":             "INFO",
		"OLLAMA_TZ":                "Asia/Shanghai",
		"OLLAMA_FLASH_ATTENTION":   "1",
	}

	var vars []model.EnvVariable
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		vars = append(vars, model.EnvVariable{
			Key:         key,
			Value:       value,
			Default:     defaults[key],
			Description: descriptions[key],
		})
	}
	return vars, scanner.Err()
}

// UpdateEnvConfig updates a single variable in the .env file.
func (s *SystemService) UpdateEnvConfig(key, value string) error {
	envPath := s.cfg.EnvFilePath()

	// Validate key format
	if !regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`).MatchString(key) {
		return fmt.Errorf("invalid key format: %s", key)
	}

	// Sanitize value: strip newlines and carriage returns to prevent injection
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, "\r", "")

	data, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("failed to read .env: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}

	if !found {
		lines = append(lines, key+"="+value)
	}

	// Atomic write: write to temp file first, then rename
	tmpPath := envPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		return fmt.Errorf("failed to write temp .env: %w", err)
	}
	if err := os.Rename(tmpPath, envPath); err != nil {
		os.Remove(tmpPath) // cleanup on failure
		return fmt.Errorf("failed to rename .env: %w", err)
	}
	return nil
}

// RunHealthCheck performs comprehensive health checks.
func (s *SystemService) RunHealthCheck(ctx context.Context, ollamaSvc *OllamaService, dockerSvc *DockerService) (*model.HealthReport, error) {
	report := &model.HealthReport{}

	// 1. Docker container check
	containerInfo, _ := dockerSvc.GetContainerInfo(ctx)
	check := model.HealthCheck{Name: "Docker 容器"}
	if containerInfo.Status == "running" {
		check.Status = "pass"
		check.Message = "运行中"
		check.Detail = fmt.Sprintf("运行时间: %s", containerInfo.Uptime)
	} else {
		check.Status = "fail"
		check.Message = fmt.Sprintf("状态: %s", containerInfo.Status)
	}
	report.Checks = append(report.Checks, check)

	// 2. Ollama API check
	check = model.HealthCheck{Name: "Ollama API"}
	if ollamaSvc.IsAPIReady() {
		check.Status = "pass"
		check.Message = "可达"
		if v, err := ollamaSvc.GetVersion(); err == nil {
			check.Detail = fmt.Sprintf("版本: %s", v)
		}
	} else {
		check.Status = "fail"
		check.Message = "不可达"
	}
	report.Checks = append(report.Checks, check)

	// 3. GPU check
	check = model.HealthCheck{Name: "GPU 加速"}
	gpus, err := dockerSvc.GetGPUInfo(ctx)
	if err == nil && len(gpus) > 0 {
		check.Status = "pass"
		check.Message = gpus[0].Name
		check.Detail = fmt.Sprintf("显存: %s, CUDA: %s", gpus[0].MemTotal, gpus[0].CUDA)
	} else {
		check.Status = "warn"
		check.Message = "未检测到 GPU"
	}
	report.Checks = append(report.Checks, check)

	// 4. Container health check
	check = model.HealthCheck{Name: "容器健康检查"}
	health := containerInfo.Health
	if health == "starting" && ollamaSvc.IsAPIReady() {
		health = "healthy"
	}
	switch health {
	case "healthy":
		check.Status = "pass"
		check.Message = "healthy"
	case "starting":
		check.Status = "warn"
		check.Message = "启动中"
	default:
		check.Status = "fail"
		check.Message = health
	}
	report.Checks = append(report.Checks, check)

	// 5. Model storage check
	check = model.HealthCheck{Name: "模型存储"}
	disk, _ := dockerSvc.GetDiskUsage(ctx)
	if disk.ModelDataSize != "" {
		check.Status = "pass"
		check.Message = fmt.Sprintf("数据大小: %s", disk.ModelDataSize)
		check.Detail = fmt.Sprintf("可用空间: %s", disk.AvailSpace)
	} else {
		check.Status = "warn"
		check.Message = "无法获取存储信息"
	}
	report.Checks = append(report.Checks, check)

	// 6. Disk space check
	check = model.HealthCheck{Name: "磁盘空间"}
	if disk.AvailSpace != "" {
		check.Status = "pass"
		check.Message = fmt.Sprintf("可用: %s / 总量: %s", disk.AvailSpace, disk.TotalSpace)
	} else {
		check.Status = "warn"
		check.Message = "无法获取磁盘信息"
	}
	report.Checks = append(report.Checks, check)

	// Calculate score
	passed := 0
	for _, c := range report.Checks {
		if c.Status == "pass" {
			passed++
		}
	}
	report.Passed = passed
	report.Total = len(report.Checks)
	report.Score = fmt.Sprintf("%d/%d", passed, report.Total)

	return report, nil
}

// RunScript executes ollama.sh with the given arguments.
func (s *SystemService) RunScript(ctx context.Context, args string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", s.cfg.ScriptPath, args)
	cmd.Dir = s.cfg.ProjectDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
