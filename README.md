# Lynx-Ollama

针对 **NVIDIA DGX Spark (GB10) 120GB 统一内存架构** 优化的 Ollama AI 服务一站式管理工具。

## 硬件目标

| 组件 | 规格 |
|------|------|
| GPU | NVIDIA GB10 |
| 内存架构 | 统一内存 (Unified Memory) |
| 总内存 | 120 GiB |
| CPU | 10核 ARM (Grace) |
| CUDA | 12.x |

## 核心特性

- **统一内存优化** — 自动识别 GPU/CPU 共享内存架构，最大化可用显存
- **多核 CPU 并行调度** — 通过 `OLLAMA_NUM_PARALLEL` + `OLLAMA_MAX_QUEUE` 实现多路并发推理，每路请求自动利用全部 CPU 核心（llama.cpp OpenMP 线程池），`optimize` 命令综合 VRAM 和 CPU 核心数自动计算最优并行数
- **智能硬件检测** — `optimize` 命令自动检测硬件并生成最佳 docker-compose 配置
- **大模型就绪** — 默认 131K 上下文、8 路并行、4 模型常驻、q8_0 KV 缓存
- **安全加固** — no-new-privileges、最小 capability、只读安全策略
- **全生命周期管理** — 部署 / 监控 / 备份 / 恢复 / 基准测试一站式管理

## 快速开始

```bash
# 1. 克隆项目
git clone <repo-url> && cd lynx-ollama

# 2. 初始化环境（创建目录、拉取镜像）
./ollama.sh init

# 3. 根据硬件自动优化配置（可选）
./ollama.sh optimize

# 4. 启动服务
./ollama.sh start

# 5. 拉取模型
./ollama.sh pull qwen2.5:72b-instruct-q4_K_M

# 6. 开始对话
./ollama.sh run qwen2.5:72b-instruct-q4_K_M
```

## 前置依赖

- Docker (含 Docker Compose V2)
- NVIDIA Container Toolkit
- NVIDIA 驱动 + CUDA 12.x
- curl
- Python 3.6+（搜索、翻译、模型管理功能需要）

## 命令一览

### 服务管理

| 命令 | 说明 |
|------|------|
| `start` | 启动 Ollama 服务 |
| `stop` | 停止 Ollama 服务 |
| `restart` | 重启 Ollama 服务 |
| `status` | 查看服务状态（容器/模型/GPU/磁盘） |
| `logs [lines]` | 查看日志（默认 200 行） |
| `update` | 拉取最新镜像并重启 |

### 模型管理

| 命令 | 说明 |
|------|------|
| `pull <model>` | 拉取/更新模型 |
| `rm <model>` | 删除已下载模型（`-f` 跳过确认） |
| `models` | 列出所有已下载模型 |
| `run <model>` | 交互式运行模型 |
| `search [keyword]` | 搜索 Ollama 官网模型（自动匹配本机硬件，支持按更新时间排序） |

### GPU 与性能

| 命令 | 说明 |
|------|------|
| `gpu` | 查看 GPU 详细信息 |
| `bench <model>` | 运行性能基准测试（冷启动/热启动/并发） |
| `health` | 全面健康检查（10 项） |
| `optimize [选项]` | 检测硬件并优化 docker-compose 配置 |

### 维护操作

| 命令 | 说明 |
|------|------|
| `init` | 初始化部署环境 |
| `backup [name]` | 备份模型与配置 |
| `restore [file]` | 恢复模型与配置 |
| `clean <mode>` | 清理（`--soft` / `--hard` / `--purge`） |
| `exec [cmd]` | 进入容器 Shell |

## 硬件自动优化

`optimize` 命令会检测宿主机硬件并自动计算最佳配置：

```bash
# 查看优化方案（不修改文件）
./ollama.sh optimize --dry-run

# 自动应用优化
./ollama.sh optimize

# 跳过确认直接应用并重启
./ollama.sh optimize --yes
```

**自动调整的参数：**

| 参数 | 说明 | 计算逻辑 |
|------|------|----------|
| CPU limits/reservations | 容器 CPU 配额 | 预留 2 核给系统 |
| Memory limits | 容器内存上限 | 统一内存：总量 - 4~8G；独立显存：80% |
| `OLLAMA_NUM_PARALLEL` | 并行请求数 | 综合 VRAM 和 CPU 核心数取较小值 |
| `OLLAMA_MAX_QUEUE` | 请求队列上限 | NUM_PARALLEL × 64（128~1024） |
| `OLLAMA_MAX_LOADED_MODELS` | 常驻模型数 | ≥96G→4, ≥48G→3, ≥24G→2 |
| `OLLAMA_CONTEXT_LENGTH` | 上下文窗口 | ≥96G→131K, ≥48G→64K, ≥24G→32K |
| `OLLAMA_KV_CACHE_TYPE` | KV 缓存精度 | ≥48G→q8_0, <48G→q4_0 |
| `OLLAMA_KEEP_ALIVE` | 模型驻留时间 | 统一内存≥64G→30m |

支持自动识别统一内存架构（GH200 / Grace / GB10 / GB200 / Jetson）。

## 多核 CPU 并行调度

Ollama 底层使用 `llama.cpp`，推理时通过 **OpenMP 线程池自动利用所有可用 CPU 核心**。多核并行调度通过以下两个层级实现：

### 层级 1：单请求多核（自动）

每个推理请求会自动分配 CPU 线程池进行矩阵运算，无需额外配置。`llama.cpp` 会根据系统可用核心数自动设置线程数。

### 层级 2：多请求并行（需配置）

通过 `OLLAMA_NUM_PARALLEL` 控制每个模型同时处理的并发请求数。每个并行请求独立使用线程池，多个请求可以同时利用 CPU/GPU 资源：

| 环境变量 | 作用 | 推荐值 |
|---|---|---|
| `OLLAMA_NUM_PARALLEL` | 每个模型的并发请求数 | `optimize` 自动计算（综合 VRAM + CPU 核心数） |
| `OLLAMA_MAX_QUEUE` | 请求队列上限（超出返回 503） | NUM_PARALLEL × 64 |
| `OLLAMA_MAX_LOADED_MODELS` | 同时加载的模型数 | 基于可用 VRAM 自动计算 |

### 自动优化

`optimize` 命令会综合 **VRAM** 和 **CPU 核心数** 计算最优 `OLLAMA_NUM_PARALLEL`（取两者推算值的较小值），确保 CPU 和 GPU 都不会过载：

```bash
# 查看当前并行调度配置
./ollama.sh status    # ⚡ 并行调度配置 面板

# 检测硬件自动计算最优值
./ollama.sh optimize --dry-run

# 健康检查中包含并行调度诊断
./ollama.sh health    # 第6项：并行调度
```

## 模型搜索

`search` 命令从 [Ollama 官网](https://ollama.com/search) 检索模型，自动按本机硬件过滤，并翻译模型描述为中文：

```bash
# 浏览热门模型（自动匹配本机 VRAM）
./ollama.sh search

# 按关键词搜索
./ollama.sh search qwen

# 按类型筛选（vision|tools|thinking|embedding|cloud）
./ollama.sh search -c vision

# 按最近更新排序（最新模型优先）
./ollama.sh search --newest
./ollama.sh search -s newest

# 显示所有模型不过滤硬件
./ollama.sh search coder --all

# 显示更多结果（超过 20 条自动拉取多页）
./ollama.sh search -n 50

# 从第 3 页开始浏览 / 组合翻页
./ollama.sh search -p 3
./ollama.sh search -n 100 -p 2

# 组合：最近更新 + 50条结果
./ollama.sh search -s newest -n 50
```

**功能特点：**

- 自动检测本机 GPU / 统一内存 / CPU-only 环境，过滤出可运行的模型
- 绿色标记适合本机的参数规格，灰色标记超出容量的规格
- 支持按热门（默认）或最近更新排序，快速发现新模型
- 翻译优先使用 `qwen3:8b`，若未安装会提示一键下载；未下载时自动选用本地最小通用模型替代
- 显示每个模型的完整描述、下载量、更新时间（中文）和安装命令
- 超过 20 条结果自动拉取多页，底部提示下一页命令

## 推荐模型（120GB VRAM）

| 模型 | 大小 | 适用场景 |
|------|------|----------|
| `qwen2.5:72b-instruct-q4_K_M` | ~42GB | 中文通用 |
| `qwen2.5-coder:32b-instruct-q8_0` | ~34GB | 代码生成 |
| `llama3.1:70b-instruct-q4_K_M` | ~40GB | 英文通用 |
| `deepseek-r1:70b-q4_K_M` | ~43GB | 推理/数学 |
| `deepseek-coder-v2:236b-q2_K` | ~86GB | 代码（极限） |
| `command-r-plus:104b-q4_K_M` | ~60GB | RAG/工具调用 |
| `mixtral:8x22b-instruct-q4_K_M` | ~80GB | MoE 混合专家 |
| `nomic-embed-text` | ~0.3GB | 文本嵌入 |

## API 端点

服务启动后默认监听 `http://localhost:11434`：

| 端点 | 说明 |
|------|------|
| `GET /` | 健康检查 |
| `GET /api/tags` | 已下载模型列表 |
| `POST /api/generate` | 文本生成 |
| `POST /api/chat` | 对话 |
| `GET /api/ps` | 运行中的模型 |
| `GET /api/version` | 版本信息 |

## 数据目录

| 路径 | 说明 |
|------|------|
| `/opt/ai/ollama/ollama_data` | 模型数据（容器挂载卷） |
| `./backups/` | 备份文件 |
| `./logs/` | 日志目录 |

## 清理策略

```bash
./ollama.sh clean --soft     # 仅停止容器（保留镜像和数据）
./ollama.sh clean --hard     # 停止容器 + 删除镜像
./ollama.sh clean --purge    # 删除一切（含所有模型数据）
```

## 项目结构

```
lynx-ollama/
├── ollama.sh                    # 管理脚本（服务/模型/GPU/维护）
├── docker-compose.yaml.template # Docker Compose 模板（纳入版本管理）
├── docker-compose.yaml          # 运行时配置（由 init/optimize 生成，不纳入 Git）
└── README.md                    # 项目文档
```

> **注意**：`docker-compose.yaml` 是由 `ollama.sh init` 从模板生成或由 `ollama.sh optimize` 根据硬件自动生成的，已在 `.gitignore` 中排除。克隆项目后需执行 `./ollama.sh init` 或 `./ollama.sh optimize` 生成该文件。

## 作者

**lynxlee**

## License

MIT
