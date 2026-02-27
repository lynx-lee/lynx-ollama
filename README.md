# Lynx-Ollama

针对 **NVIDIA DGX Spark (GB10) 120GB 统一内存架构** 优化的 Ollama AI 服务一键部署工具。

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
- **智能硬件检测** — `optimize` 命令自动检测硬件并生成最佳 docker-compose 配置
- **大模型就绪** — 默认 131K 上下文、8 路并行、4 模型常驻、q8_0 KV 缓存
- **安全加固** — no-new-privileges、最小 capability、只读安全策略
- **全生命周期管理** — 部署 / 监控 / 备份 / 恢复 / 基准测试一站式管理

## 快速开始

```bash
# 1. 克隆项目
git clone <repo-url> && cd lynx-ollama

# 2. 初始化环境（创建目录、拉取镜像）
./deploy.sh init

# 3. 根据硬件自动优化配置（可选）
./deploy.sh optimize

# 4. 启动服务
./deploy.sh start

# 5. 拉取模型
./deploy.sh pull qwen2.5:72b-instruct-q4_K_M

# 6. 开始对话
./deploy.sh run qwen2.5:72b-instruct-q4_K_M
```

## 前置依赖

- Docker (含 Docker Compose V2)
- NVIDIA Container Toolkit
- NVIDIA 驱动 + CUDA 12.x
- curl

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
| `models` | 列出所有已下载模型 |
| `run <model>` | 交互式运行模型 |
| `search [keyword]` | 搜索 Ollama 官网模型（自动匹配本机硬件，本地模型翻译描述） |

### GPU 与性能

| 命令 | 说明 |
|------|------|
| `gpu` | 查看 GPU 详细信息 |
| `bench <model>` | 运行性能基准测试（冷启动/热启动/并发） |
| `health` | 全面健康检查（9 项） |
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
./deploy.sh optimize --dry-run

# 自动应用优化
./deploy.sh optimize

# 跳过确认直接应用并重启
./deploy.sh optimize --yes
```

**自动调整的参数：**

| 参数 | 说明 | 计算逻辑 |
|------|------|----------|
| CPU limits/reservations | 容器 CPU 配额 | 预留 2 核给系统 |
| Memory limits | 容器内存上限 | 统一内存：总量 - 4~8G；独立显存：80% |
| `OLLAMA_NUM_PARALLEL` | 并行请求数 | ≥96G→8, ≥48G→4, ≥24G→2 |
| `OLLAMA_MAX_LOADED_MODELS` | 常驻模型数 | ≥96G→4, ≥48G→3, ≥24G→2 |
| `OLLAMA_CONTEXT_LENGTH` | 上下文窗口 | ≥96G→131K, ≥48G→64K, ≥24G→32K |
| `OLLAMA_KV_CACHE_TYPE` | KV 缓存精度 | ≥48G→q8_0, <48G→q4_0 |
| `OLLAMA_KEEP_ALIVE` | 模型驻留时间 | 统一内存≥64G→30m |

支持自动识别统一内存架构（GH200 / Grace / GB10 / GB200 / Jetson）。

## 模型搜索

`search` 命令从 [Ollama 官网](https://ollama.com/search) 检索模型，自动按本机硬件过滤，并翻译模型描述为中文：

```bash
# 浏览热门模型（自动匹配本机 VRAM）
./deploy.sh search

# 按关键词搜索
./deploy.sh search qwen

# 按类型筛选（vision|tools|thinking|embedding|cloud）
./deploy.sh search -c vision

# 显示所有模型不过滤硬件
./deploy.sh search coder --all

# 显示更多结果（超过 20 条自动拉取多页）
./deploy.sh search -n 50

# 从第 3 页开始浏览 / 组合翻页
./deploy.sh search -p 3
./deploy.sh search -n 100 -p 2
```

**功能特点：**

- 自动检测本机 GPU / 统一内存 / CPU-only 环境，过滤出可运行的模型
- 绿色标记适合本机的参数规格，灰色标记超出容量的规格
- 若本地 Ollama 服务在运行，自动选用小模型翻译英文描述为中文
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
./deploy.sh clean --soft     # 仅停止容器（保留镜像和数据）
./deploy.sh clean --hard     # 停止容器 + 删除镜像
./deploy.sh clean --purge    # 删除一切（含所有模型数据）
```

## 项目结构

```
lynx-ollama/
├── deploy.sh              # 部署管理脚本
├── docker-compose.yaml    # Docker Compose 配置
└── README.md              # 项目文档
```

## License

MIT
