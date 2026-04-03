# Lynx-Ollama 多设备适配方案

## 一、现状分析

### 1.1 当前硬件绑定清单

| 绑定点 | 文件 | 具体内容 | 影响范围 |
|--------|------|----------|----------|
| GPU 检测仅支持 `nvidia-smi` | `docker.go` `GetGPUInfo()` | 硬编码 `docker exec ollama nvidia-smi ...` | GPU 信息页、性能监控、健康检查 |
| 统一内存检测硬编码 GPU 名称 | `docker.go` `isUnifiedMemoryGPU()` | 关键词列表：`gb10, gh200, grace, gb200, jetson` | Dashboard 显示、GPU 监控 |
| Docker Compose 模板锁定 NVIDIA | `docker-compose.yaml.template` | `driver: nvidia`、`NVIDIA_VISIBLE_DEVICES=all` | 容器启动 |
| optimize 仅检测 NVIDIA GPU | `ollama.sh` `do_optimize()` | `nvidia-smi` 做硬件检测 | 自动优化配置 |
| 默认参数面向 120G 统一内存 | `docker-compose.yaml.template` | 131K ctx、8 并行、120G 内存限制 | 所有默认配置 |
| 性能监控仅采集 nvidia-smi | `docker.go` `GetPerfMetrics()` | GPU 利用率通过 `nvidia-smi` 获取 | 实时图表 |
| GPU 监控服务 NVIDIA 专用 | `gpu_monitor.go` | 依赖 `GetGPUInfo()` → `nvidia-smi` | 自动重启逻辑 |
| README/脚本标题绑定 GB10 | `README.md`、`ollama.sh` | "DGX Spark Edition" | 用户感知 |

### 1.2 Ollama 官方支持的设备矩阵

| 平台 | 加速后端 | Ollama 镜像 | GPU 检测工具 |
|------|----------|-------------|-------------|
| NVIDIA (CUDA) | CUDA | `ollama/ollama:latest` | `nvidia-smi` |
| NVIDIA 统一内存 | CUDA | `ollama/ollama:latest` | `nvidia-smi` + `/proc/meminfo` |
| AMD (ROCm) | ROCm | `ollama/ollama:rocm` | `rocm-smi` |
| Apple Silicon (Metal) | Metal | 原生安装（非 Docker） | `system_profiler SPDisplaysDataType` |
| Intel Arc (oneAPI) | SYCL | 社区镜像 | `xpu-smi` |
| 纯 CPU | CPU | `ollama/ollama:latest` | 无 |

---

## 二、目标架构

```
                    ┌─────────────────────────────┐
                    │     设备检测层 (DeviceProbe)   │
                    │  统一接口，按平台分发检测逻辑    │
                    └──────────┬──────────────────┘
                               │
         ┌─────────┬───────────┼───────────┬──────────┐
         ▼         ▼           ▼           ▼          ▼
    ┌─────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
    │ NVIDIA  │ │  AMD   │ │ Apple  │ │ Intel  │ │  CPU   │
    │  CUDA   │ │ ROCm   │ │ Metal  │ │ oneAPI │ │  Only  │
    │nvidia-smi│ │rocm-smi│ │sysctl  │ │xpu-smi │ │/proc   │
    └────┬────┘ └───┬────┘ └───┬────┘ └───┬────┘ └───┬────┘
         │          │          │          │          │
         └──────────┴──────────┴──────────┴──────────┘
                               │
                    ┌──────────▼──────────────────┐
                    │   配置优化层 (DeviceOptimizer) │
                    │  根据检测结果生成最优配置       │
                    └──────────┬──────────────────┘
                               │
                    ┌──────────▼──────────────────┐
                    │   Docker Compose 模板渲染     │
                    │  选择镜像 + 设备驱动 + 参数    │
                    └─────────────────────────────┘
```

---

## 三、设备类型定义

```go
// DeviceType 枚举所有支持的设备类型
type DeviceType string

const (
    DeviceNVIDIA       DeviceType = "nvidia"        // NVIDIA 独立显卡 (CUDA)
    DeviceNVIDIAUnified DeviceType = "nvidia_unified" // NVIDIA 统一内存 (GB10/GH200/Jetson)
    DeviceAMD          DeviceType = "amd"            // AMD 显卡 (ROCm)
    DeviceApple        DeviceType = "apple"          // Apple Silicon (Metal)
    DeviceIntel        DeviceType = "intel"          // Intel Arc (oneAPI/SYCL)
    DeviceCPU          DeviceType = "cpu"            // 纯 CPU 推理
)

// DeviceInfo 统一设备信息结构
type DeviceInfo struct {
    Type            DeviceType `json:"type"`
    Name            string     `json:"name"`             // "NVIDIA RTX 4090" / "Apple M3 Max" / "AMD RX 7900"
    Vendor          string     `json:"vendor"`           // "nvidia" / "amd" / "apple" / "intel" / "cpu"
    MemTotal        int64      `json:"mem_total_mb"`     // 显存/统一内存 (MiB)
    MemUsed         int64      `json:"mem_used_mb"`
    MemFree         int64      `json:"mem_free_mb"`
    IsUnifiedMem    bool       `json:"is_unified_mem"`   // 统一内存架构
    Utilization     int        `json:"utilization"`      // GPU 利用率 (%)
    Temperature     int        `json:"temperature"`      // 温度 (°C)
    DriverVersion   string     `json:"driver_version"`
    ComputeRuntime  string     `json:"compute_runtime"`  // "CUDA 12.x" / "ROCm 6.x" / "Metal 3" / "N/A"
    Count           int        `json:"count"`            // GPU 数量
    SystemMemTotal  int64      `json:"sys_mem_total_mb"` // 系统总内存
    SystemMemFree   int64      `json:"sys_mem_free_mb"`
    CPUCores        int        `json:"cpu_cores"`
    CPUModel        string     `json:"cpu_model"`
    Arch            string     `json:"arch"`             // "x86_64" / "arm64"
}
```

---

## 四、分模块改造方案

### 4.1 设备检测层 — `internal/service/device_probe.go`（新增）

**核心思路**：统一接口，按优先级逐一尝试各检测器。

```
检测顺序：NVIDIA → AMD → Apple → Intel → CPU
```

| 检测器 | 检测方式 | 适用场景 |
|--------|----------|----------|
| `probeNVIDIA()` | 容器内 `nvidia-smi --query-gpu=...` | NVIDIA GPU（含统一内存） |
| `probeAMD()` | 容器内 `rocm-smi --showmeminfo vram --json` | AMD ROCm GPU |
| `probeApple()` | 宿主机 `system_profiler SPDisplaysDataType` + `sysctl hw.memsize` | macOS Apple Silicon |
| `probeIntel()` | 容器内 `xpu-smi discovery` | Intel Arc |
| `probeCPU()` | `/proc/cpuinfo` + `/proc/meminfo` 或 `sysctl` | 纯 CPU / 兜底 |

**关键实现细节：**

```go
// ProbeDevice 自动检测当前设备，返回统一的 DeviceInfo
func (s *DockerService) ProbeDevice(ctx context.Context) DeviceInfo {
    // 1. 尝试 NVIDIA (含统一内存自动判定)
    if info, ok := s.probeNVIDIA(ctx); ok {
        return info
    }
    // 2. 尝试 AMD ROCm
    if info, ok := s.probeAMD(ctx); ok {
        return info
    }
    // 3. 尝试 Apple Metal (仅非 Docker 模式)
    if info, ok := s.probeApple(ctx); ok {
        return info
    }
    // 4. 尝试 Intel oneAPI
    if info, ok := s.probeIntel(ctx); ok {
        return info
    }
    // 5. 兜底：CPU only
    return s.probeCPU(ctx)
}
```

**NVIDIA 统一内存判定**（沿用现有双重策略，扩展关键词）：

```go
func isUnifiedMemoryGPU(gpuName string, gpuVRAM, sysRAM int64) bool {
    lower := strings.ToLower(gpuName)
    // 名称匹配
    keywords := []string{"gb10", "gh200", "grace", "gb200", "jetson", "orin"}
    for _, kw := range keywords {
        if strings.Contains(lower, kw) { return true }
    }
    // 数值匹配：VRAM 与系统内存差值 < 20%
    if gpuVRAM > 0 && sysRAM > 0 {
        diff := abs(sysRAM - gpuVRAM)
        threshold := sysRAM * 20 / 100
        if diff <= threshold { return true }
    }
    return false
}
```

### 4.2 Docker Compose 模板系统 — 多模板方案

**当前**：单一 `docker-compose.yaml.template`（NVIDIA 硬编码）

**改造后**：按设备类型选择模板片段

```
docker-compose.yaml.template          # 基础模板（通用部分）
docker-compose.nvidia.yaml.template   # NVIDIA GPU 设备段
docker-compose.amd.yaml.template      # AMD ROCm 设备段
docker-compose.cpu.yaml.template      # CPU-only（无设备段）
docker-compose.apple.yaml.template    # Apple（原生模式，不用 Docker）
```

**基础模板**中设备段改为条件渲染：

```yaml
# docker-compose.yaml.template (修改后)
services:
  ollama:
    image: ${OLLAMA_IMAGE:-ollama/ollama:latest}
    # ... 通用环境变量 ...

    # GPU 设备配置（由 optimize 根据检测结果写入 .env）
    # NVIDIA: driver=nvidia, count=all
    # AMD:    /dev/kfd + /dev/dri
    # CPU:    无 deploy.resources.reservations.devices
```

**各设备类型的关键差异：**

| 配置项 | NVIDIA (CUDA) | NVIDIA (统一内存) | AMD (ROCm) | CPU-only | Apple (Metal) |
|--------|---------------|-------------------|------------|----------|---------------|
| 镜像 | `ollama/ollama:latest` | 同左 | `ollama/ollama:rocm` | `ollama/ollama:latest` | 原生安装 |
| 设备 | `driver: nvidia` | 同左 | `/dev/kfd`, `/dev/dri` | 无 | 无 Docker |
| 环境变量 | `NVIDIA_VISIBLE_DEVICES=all` | 同左 | `HSA_OVERRIDE_GFX_VERSION=x.x.x` | 无 | 无 |
| 内存限制 | 系统内存 80% | 系统内存 - 4~8G | 系统内存 80% | 系统内存 80% | 全部 |
| Flash Attention | 支持 | 支持 | 视版本 | 不支持 | 支持 |

### 4.3 配置优化引擎改造 — `do_optimize()` / `Optimize API`

**当前**：只检测 `nvidia-smi`，只处理 NVIDIA/统一内存/CPU-only 三种情况。

**改造后**：基于 `ProbeDevice()` 的统一检测结果，分设备类型生成配置。

```bash
# ollama.sh do_optimize() 改造后的检测流程
detect_device() {
    # 1. NVIDIA
    if command -v nvidia-smi &>/dev/null; then
        # ... 现有 NVIDIA 检测逻辑（含统一内存判定）
        DEVICE_TYPE="nvidia" # 或 "nvidia_unified"
        return
    fi

    # 2. AMD ROCm
    if command -v rocm-smi &>/dev/null || [ -e /dev/kfd ]; then
        gpu_name=$(rocm-smi --showproductname 2>/dev/null | grep "GPU" | head -1)
        gpu_vram_mb=$(rocm-smi --showmeminfo vram --json 2>/dev/null | python3 -c "...")
        DEVICE_TYPE="amd"
        return
    fi

    # 3. Apple Metal (macOS)
    if [[ "$(uname)" == "Darwin" ]] && system_profiler SPDisplaysDataType &>/dev/null; then
        gpu_name=$(system_profiler SPDisplaysDataType | grep "Chipset Model" | head -1 | cut -d: -f2 | xargs)
        # Apple 统一内存 = 系统内存
        DEVICE_TYPE="apple"
        return
    fi

    # 4. Intel (xpu-smi)
    if command -v xpu-smi &>/dev/null; then
        DEVICE_TYPE="intel"
        return
    fi

    # 5. CPU-only
    DEVICE_TYPE="cpu"
}
```

**各设备类型的优化参数计算规则：**

| 参数 | NVIDIA 独立显存 | 统一内存 | AMD | Apple | CPU-only |
|------|----------------|----------|-----|-------|----------|
| `effective_vram` | GPU VRAM | 系统内存 - 预留 | GPU VRAM | 统一内存 | 系统内存 × 70% |
| `NUM_PARALLEL` | min(VRAM 档位, CPU/2) | 同左 | 同 NVIDIA | 同左 | min(CPU/2, 4) |
| `CONTEXT_LENGTH` | ≥96G→131K, ≥48G→64K | 同左 | 同左 | 同左 | ≥32G→32K, 否则 8K |
| `KV_CACHE_TYPE` | ≥48G→q8_0, 否则 q4_0 | 同左 | 同左 | 同左 | q4_0 |
| `FLASH_ATTENTION` | 1 | 1 | 视 ROCm 版本 | 1 | 0 |
| `KEEP_ALIVE` | 15m | 30m | 15m | 30m | 5m |
| `OLLAMA_IMAGE` | latest | latest | rocm | N/A | latest |

### 4.4 Go 后端改造

#### 4.4.1 `docker.go` — GetGPUInfo 泛化

```go
// GetAcceleratorInfo 替代原 GetGPUInfo，支持多种加速器
func (s *DockerService) GetAcceleratorInfo(ctx context.Context) ([]model.DeviceInfo, error) {
    // 优先尝试 NVIDIA
    if info, err := s.getNVIDIAInfo(ctx); err == nil && len(info) > 0 {
        return info, nil
    }
    // 尝试 AMD ROCm
    if info, err := s.getAMDInfo(ctx); err == nil && len(info) > 0 {
        return info, nil
    }
    // 尝试获取系统信息作为 CPU-only 回退
    return s.getCPUOnlyInfo(ctx)
}

// getAMDInfo 通过 rocm-smi 获取 AMD GPU 信息
func (s *DockerService) getAMDInfo(ctx context.Context) ([]model.DeviceInfo, error) {
    out, err := s.runCommand(ctx, "docker", "exec", "ollama",
        "rocm-smi", "--showmeminfo", "vram", "--showuse", "--showtemp", "--json")
    // ... 解析 JSON 输出 ...
}
```

#### 4.4.2 `gpu_monitor.go` — 泛化为 DeviceMonitor

```go
// DeviceMonitorService 替代 GPUMonitorService
type DeviceMonitorService struct {
    // ... 同原有字段 ...
    deviceType DeviceType // 启动时检测一次，后续复用
}

func (s *DeviceMonitorService) checkDevice() {
    switch s.deviceType {
    case DeviceNVIDIA, DeviceNVIDIAUnified:
        s.checkNVIDIA()   // 现有逻辑
    case DeviceAMD:
        s.checkAMD()       // rocm-smi 检测
    case DeviceCPU:
        s.checkCPU()       // 只检查容器运行状态
    case DeviceApple:
        // Apple 不用 Docker，跳过容器检查
    }
}
```

#### 4.4.3 `GetPerfMetrics()` — 多后端采集

```go
func (s *DockerService) GetPerfMetrics(ctx context.Context) model.PerfMetrics {
    m := model.PerfMetrics{Timestamp: time.Now().Unix()}

    // Docker stats 是通用的（CPU/内存/网络/磁盘）
    s.collectDockerStats(ctx, &m)

    // GPU 指标按检测到的设备类型分发
    switch s.detectedDevice {
    case DeviceNVIDIA, DeviceNVIDIAUnified:
        s.collectNVIDIAMetrics(ctx, &m)  // nvidia-smi
    case DeviceAMD:
        s.collectAMDMetrics(ctx, &m)     // rocm-smi
    default:
        // CPU-only: 无 GPU 指标
    }

    return m
}
```

### 4.5 前端适配

#### Dashboard GPU 卡片

```javascript
// 根据 device_type 动态渲染
function renderDeviceCard(device) {
    switch (device.type) {
        case 'nvidia':
        case 'nvidia_unified':
            return renderNVIDIACard(device);   // 现有逻辑
        case 'amd':
            return renderAMDCard(device);       // ROCm 信息
        case 'apple':
            return renderAppleCard(device);     // Metal 信息
        case 'cpu':
            return renderCPUOnlyCard(device);   // 仅 CPU/内存
    }
}
```

#### 前端性能监控

GPU 面板标题根据设备类型动态显示：

| 设备类型 | 面板标题 | 数据来源 |
|----------|----------|----------|
| NVIDIA | GPU 使用率 | `nvidia-smi` |
| AMD | GPU 使用率 | `rocm-smi` |
| Apple | GPU 使用率 | `powermetrics` |
| CPU | （隐藏此面板） | N/A |

### 4.6 Apple Silicon 特殊处理

Apple Silicon (M1/M2/M3/M4) 不支持 Docker GPU 透传，需要 **原生安装模式**：

```
Ollama 安装方式对比：
┌────────────────┬───────────────────────────────────────────┐
│                │  Docker 模式              │  原生模式       │
├────────────────┼───────────────────────────┼────────────────┤
│ NVIDIA/AMD     │ ✅ Docker + GPU runtime   │ ✅ 也支持       │
│ Apple Silicon  │ ❌ 无 GPU 加速            │ ✅ Metal 加速   │
│ CPU-only       │ ✅ Docker                 │ ✅ 也支持       │
└────────────────┴───────────────────────────┴────────────────┘
```

**方案**：新增 `DEPLOY_MODE` 环境变量：

```bash
DEPLOY_MODE=docker   # 默认：Docker 容器模式（NVIDIA/AMD/CPU）
DEPLOY_MODE=native   # 原生模式（Apple Silicon / 裸机 Linux）
```

原生模式下：
- 不使用 `docker-compose`
- 直接用 `brew install ollama` 或 `curl -fsSL https://ollama.com/install.sh | sh`
- Web 管理界面以独立二进制运行
- `docker exec ollama nvidia-smi` 改为直接调用宿主机命令

---

## 五、实施步骤（按优先级排序）

### Phase 1：架构解耦（不改变功能，消除硬编码）

| 步骤 | 改动 | 涉及文件 | 工作量 |
|------|------|----------|--------|
| 1.1 | 抽象 `DeviceInfo` 统一数据模型 | `model/types.go` | 小 |
| 1.2 | 新增 `device_probe.go` 设备检测层 | `service/device_probe.go`（新增） | 中 |
| 1.3 | `GetGPUInfo()` → `GetAcceleratorInfo()` 适配器模式 | `service/docker.go` | 中 |
| 1.4 | `gpu_monitor.go` → `device_monitor.go` 泛化 | `service/gpu_monitor.go` | 小 |
| 1.5 | `GetPerfMetrics()` GPU 采集分发 | `service/docker.go` | 小 |
| 1.6 | `isUnifiedMemoryGPU()` 提取到 `device_probe.go` | `service/docker.go` | 小 |
| 1.7 | 前端 GPU 信息展示适配 `device.type` | `app.js` | 中 |
| 1.8 | README / 脚本标题去除 GB10 专属标记 | `README.md`、`ollama.sh` | 小 |

### Phase 2：AMD ROCm 支持

| 步骤 | 改动 | 涉及文件 | 工作量 |
|------|------|----------|--------|
| 2.1 | `probeAMD()` 实现 `rocm-smi` 解析 | `service/device_probe.go` | 中 |
| 2.2 | `docker-compose.amd.yaml.template` 模板 | 根目录（新增） | 小 |
| 2.3 | `do_optimize()` 增加 AMD 分支 | `ollama.sh` | 中 |
| 2.4 | `collectAMDMetrics()` 性能采集 | `service/docker.go` | 中 |
| 2.5 | AMD GPU 卡片渲染 | `app.js` | 小 |

### Phase 3：CPU-only 完善

| 步骤 | 改动 | 工作量 |
|------|------|--------|
| 3.1 | `docker-compose.cpu.yaml.template`（无 GPU 设备段） | 小 |
| 3.2 | `do_optimize()` CPU-only 分支优化参数（关闭 Flash Attention 等） | 小 |
| 3.3 | 前端隐藏 GPU 面板，突出 CPU/内存信息 | 小 |

### Phase 4：Apple Silicon 原生模式

| 步骤 | 改动 | 工作量 |
|------|------|--------|
| 4.1 | `DEPLOY_MODE=native` 模式支持 | 中 |
| 4.2 | `probeApple()` macOS 硬件检测 | 中 |
| 4.3 | Web 管理界面独立运行（非 Docker） | 大 |
| 4.4 | 服务管理从 `docker start/stop` → `launchctl` / `systemctl` | 大 |

### Phase 5：Intel Arc + 多 GPU

| 步骤 | 改动 | 工作量 |
|------|------|--------|
| 5.1 | `probeIntel()` + `xpu-smi` 解析 | 中 |
| 5.2 | 多 GPU 选择和分配策略 | 大 |
| 5.3 | Web UI 多 GPU 监控面板 | 中 |

---

## 六、向后兼容策略

| 策略 | 说明 |
|------|------|
| **检测优先级兜底** | 检测失败时总是降级到 CPU-only，不会报错退出 |
| **现有 .env 兼容** | 已有的 NVIDIA 配置（`NVIDIA_VISIBLE_DEVICES` 等）继续生效 |
| **optimize 保持幂等** | 重新运行 `optimize` 会重新检测硬件并覆盖配置 |
| **API 兼容** | `/api/gpu` 返回格式增加 `type` 字段，旧字段保留 |
| **docker-compose 模板** | 现有模板改名为 `docker-compose.nvidia.yaml.template`，新增通用模板 |

---

## 七、各设备类型的 NVIDIA 消费级显卡优化建议

针对游戏卡（如 RTX 4090/3090/4080 等），需要特殊优化：

| 参数 | RTX 4090 (24G) | RTX 3090 (24G) | RTX 4080 (16G) | RTX 3060 (12G) |
|------|----------------|----------------|----------------|----------------|
| `NUM_PARALLEL` | 2 | 2 | 1 | 1 |
| `MAX_LOADED_MODELS` | 2 | 2 | 1 | 1 |
| `CONTEXT_LENGTH` | 32768 | 32768 | 16384 | 8192 |
| `KV_CACHE_TYPE` | q8_0 | q4_0 | q4_0 | q4_0 |
| `FLASH_ATTENTION` | 1 | 1 | 1 | 1 |
| `KEEP_ALIVE` | 15m | 15m | 10m | 5m |
| 推荐模型 | 70B-Q4, 32B-Q8 | 70B-Q4, 32B-Q8 | 14B-Q8, 32B-Q4 | 7B-Q8, 14B-Q4 |

**检测消费级显卡的特殊逻辑**：

```go
// 消费级显卡通常 VRAM < 48G 且 GPU 名称包含 "GeForce" / "RTX" / "GTX"
func isConsumerGPU(name string) bool {
    lower := strings.ToLower(name)
    return strings.Contains(lower, "geforce") ||
           strings.Contains(lower, "rtx") ||
           strings.Contains(lower, "gtx")
}
```

消费级显卡额外优化：
- 默认关闭 `OLLAMA_DEBUG`（减少 IO 开销）
- `MEM_LIMIT` 设为系统内存 50%（留更多给 OS 和其他应用）
- GPU 监控检查间隔延长到 60s（避免 nvidia-smi 调用影响游戏性能）

---

## 八、AMD 显卡适配细节

### 8.1 支持的 AMD GPU 型号

| GPU 系列 | ROCm 支持 | VRAM | 备注 |
|----------|-----------|------|------|
| RX 7900 XTX | ✅ ROCm 6.x | 24G | 消费级旗舰 |
| RX 7900 XT | ✅ ROCm 6.x | 20G | |
| RX 7800 XT | ✅ ROCm 6.x | 16G | |
| RX 7600 | ⚠️ 需 `HSA_OVERRIDE` | 8G | 需要设置 GFX 版本覆盖 |
| MI300X | ✅ ROCm 6.x | 192G | 数据中心 |
| MI250X | ✅ ROCm 5.x+ | 128G | |

### 8.2 Docker Compose AMD 模板

```yaml
# docker-compose.amd.yaml.template
services:
  ollama:
    image: ollama/ollama:rocm
    devices:
      - /dev/kfd
      - /dev/dri
    environment:
      - HSA_OVERRIDE_GFX_VERSION=${AMD_GFX_VERSION:-}
    group_add:
      - video
      - render
```

### 8.3 `rocm-smi` 指标采集

```bash
# GPU 信息
rocm-smi --showproductname --showmeminfo vram --showuse --showtemp --json

# 输出示例
{
  "card0": {
    "GPU Memory Allocated (VRAM)": "8192 MB",
    "GPU Memory Usage (%)": "33",
    "GPU use (%)": "45",
    "Temperature (Sensor edge)": "65.0"
  }
}
```

---

## 九、测试矩阵

| 测试场景 | 验证点 |
|----------|--------|
| NVIDIA RTX 4090 | 独立显存检测、24G 优化参数、消费级标记 |
| NVIDIA DGX Spark GB10 | 统一内存检测、120G 优化参数（回归测试） |
| NVIDIA Jetson Orin | 统一内存检测、小内存优化 |
| AMD RX 7900 XTX | ROCm 检测、`rocm` 镜像选择、GFX 版本 |
| Apple M3 Max | 原生模式、Metal 检测、统一内存 |
| 纯 CPU (Linux) | 无 GPU 回退、CPU-only 参数 |
| 纯 CPU (macOS) | macOS sysctl 内存检测 |
| 多 NVIDIA GPU | 多卡识别、VRAM 累加 |

---

## 十、总结

改造核心思路：**检测泛化 → 配置分发 → 模板选择**。

- **Phase 1**（解耦）是基础，工作量中等，完成后项目不再硬绑 GB10
- **Phase 2**（AMD）是最高优先级的新设备支持，ROCm 生态成熟
- **Phase 3**（CPU-only）工作量最小，也有实际用户需求
- **Phase 4**（Apple）工作量最大（需要原生模式），但用户基数大
- **Phase 5**（Intel）优先级最低，等 Intel Arc + Ollama 生态成熟再做

预计总工作量：Phase 1-3 约 **3-5 人天**，Phase 4 约 **5-8 人天**，Phase 5 约 **3 人天**。
