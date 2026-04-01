package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lynxlee/lynx-ollama-console/internal/config"
)

// GPUMonitorService monitors GPU availability and auto-restarts container if needed.
type GPUMonitorService struct {
	cfg        *config.Config
	dockerSvc  *DockerService
	ollamaSvc  *OllamaService

	// Configuration
	checkInterval   time.Duration
	restartCooldown time.Duration
	maxRestarts     int

	// State
	mu              sync.Mutex
	lastRestart     time.Time
	restartCount    int
	lastGPUCheck    time.Time
	gpuAvailable    bool
	stopChan        chan struct{}
	running         bool
}

// GPUMonitorConfig holds configuration for GPU monitoring.
type GPUMonitorConfig struct {
	CheckInterval   time.Duration // How often to check GPU (default: 30s)
	RestartCooldown time.Duration // Minimum time between restarts (default: 5min)
	MaxRestarts     int           // Max restarts per hour (default: 3)
}

// NewGPUMonitorService creates a new GPU monitor service.
func NewGPUMonitorService(cfg *config.Config, dockerSvc *DockerService, ollamaSvc *OllamaService, monitorCfg *GPUMonitorConfig) *GPUMonitorService {
	if monitorCfg == nil {
		monitorCfg = &GPUMonitorConfig{
			CheckInterval:   30 * time.Second,
			RestartCooldown: 5 * time.Minute,
			MaxRestarts:     3,
		}
	}

	return &GPUMonitorService{
		cfg:             cfg,
		dockerSvc:       dockerSvc,
		ollamaSvc:       ollamaSvc,
		checkInterval:   monitorCfg.CheckInterval,
		restartCooldown: monitorCfg.RestartCooldown,
		maxRestarts:     monitorCfg.MaxRestarts,
		stopChan:        make(chan struct{}),
	}
}

// Start begins the GPU monitoring loop.
func (s *GPUMonitorService) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	slog.Info("GPU monitor started", "interval", s.checkInterval, "cooldown", s.restartCooldown, "max_restarts", s.maxRestarts)

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.checkGPU()
		case <-s.stopChan:
			slog.Info("GPU monitor stopped")
			return
		}
	}
}

// Stop halts the GPU monitoring loop.
func (s *GPUMonitorService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.running = false
	close(s.stopChan)
}

// checkGPU performs a single GPU check and restarts if needed.
func (s *GPUMonitorService) checkGPU() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.lastGPUCheck = time.Now()

	// Get container info first
	containerInfo, err := s.dockerSvc.GetContainerInfo(ctx)
	if err != nil {
		slog.Warn("GPU monitor: failed to get container info", "error", err)
		return
	}

	// Skip if container is not running
	if containerInfo.Status != "running" {
		slog.Debug("GPU monitor: container not running, skipping check")
		s.gpuAvailable = false
		return
	}

	// Check GPU availability
	gpus, err := s.dockerSvc.GetGPUInfo(ctx)
	if err != nil {
		slog.Warn("GPU monitor: failed to get GPU info", "error", err)
		s.gpuAvailable = false
		return
	}

	// Check if GPU info is valid
	if len(gpus) == 0 {
		slog.Warn("GPU monitor: no GPU detected")
		s.handleGPUNotAvailable(ctx, "no GPU detected")
		return
	}

	// Check for unified memory GPU with valid info
	gpu := gpus[0]
	if gpu.IsUnifiedMem {
		// For unified memory GPUs (GB10, GH200, etc.), check if we have valid memory info
		if gpu.UnifiedMemTotal == "" || gpu.UnifiedMemTotal == "0 GiB" {
			slog.Warn("GPU monitor: unified memory GPU but no memory info available", "gpu", gpu.Name)
			s.handleGPUNotAvailable(ctx, "unified memory GPU not properly initialized")
			return
		}
		s.gpuAvailable = true
		slog.Debug("GPU monitor: unified memory GPU available", "gpu", gpu.Name, "memory", gpu.UnifiedMemTotal)
	} else {
		// For regular GPUs, check if we have valid memory info
		if gpu.MemTotal == "" || gpu.MemTotal == "[N/A] MiB" {
			slog.Warn("GPU monitor: GPU detected but no memory info", "gpu", gpu.Name)
			s.handleGPUNotAvailable(ctx, "GPU memory info not available")
			return
		}
		s.gpuAvailable = true
		slog.Debug("GPU monitor: GPU available", "gpu", gpu.Name, "memory", gpu.MemTotal)
	}

	// Reset restart count on successful check
	s.mu.Lock()
	if time.Since(s.lastRestart) > time.Hour {
		s.restartCount = 0
	}
	s.mu.Unlock()
}

// handleGPUNotAvailable handles the case when GPU is not available.
func (s *GPUMonitorService) handleGPUNotAvailable(ctx context.Context, reason string) {
	s.gpuAvailable = false

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if we're in cooldown period
	if time.Since(s.lastRestart) < s.restartCooldown {
		slog.Debug("GPU monitor: in cooldown period, skipping restart",
			"time_since_last", time.Since(s.lastRestart),
			"cooldown", s.restartCooldown)
		return
	}

	// Check if we've exceeded max restarts
	if s.restartCount >= s.maxRestarts {
		slog.Warn("GPU monitor: max restarts exceeded, skipping restart",
			"count", s.restartCount,
			"max", s.maxRestarts)
		return
	}

	// Perform restart
	slog.Warn("GPU monitor: restarting container due to GPU unavailability",
		"reason", reason,
		"restart_count", s.restartCount+1)

	s.lastRestart = time.Now()
	s.restartCount++

	// Restart in a goroutine to avoid blocking
	go func() {
		restartCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		_, err := s.dockerSvc.RestartService(restartCtx)
		if err != nil {
			slog.Error("GPU monitor: failed to restart container", "error", err)
		} else {
			slog.Info("GPU monitor: container restarted successfully")
		}
	}()
}

// GetStatus returns the current GPU monitor status.
func (s *GPUMonitorService) GetStatus() GPUMonitorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	return GPUMonitorStatus{
		Running:       s.running,
		GPUAvailable:  s.gpuAvailable,
		LastCheck:     s.lastGPUCheck,
		LastRestart:   s.lastRestart,
		RestartCount:  s.restartCount,
		CheckInterval: s.checkInterval,
	}
}

// GPUMonitorStatus represents the current status of the GPU monitor.
type GPUMonitorStatus struct {
	Running       bool          `json:"running"`
	GPUAvailable  bool          `json:"gpu_available"`
	LastCheck     time.Time     `json:"last_check"`
	LastRestart   time.Time     `json:"last_restart"`
	RestartCount  int           `json:"restart_count"`
	CheckInterval time.Duration `json:"check_interval"`
}
