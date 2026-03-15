# Lynx-Ollama

![Version](https://img.shields.io/badge/version-v1.5.0-blue)

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

- **Web 管理界面** — 基于 Go 的轻量级 Web UI，图形化管理 Ollama 服务启停、版本更新、模型管理、配置调优、健康诊断、GPU 监控、实时日志
- **统一内存优化** — 自动识别 GPU/CPU 共享内存架构，最大化可用显存
- **多核 CPU 并行调度** — 通过 `OLLAMA_NUM_PARALLEL` + `OLLAMA_MAX_QUEUE` 实现多路并发推理，每路请求自动利用全部 CPU 核心（llama.cpp OpenMP 线程池），`optimize` 命令综合 VRAM 和 CPU 核心数自动计算最优并行数
- **智能硬件检测** — `optimize` 命令自动检测硬件并生成最佳 docker-compose 配置
- **大模型就绪** — 默认 131K 上下文、8 路并行、4 模型常驻、q8_0 KV 缓存
- **安全加固** — API Key 认证保护所有管理接口、CORS 来源限制、JSON 注入防护、no-new-privileges、最小 capability、只读安全策略
- **全生命周期管理** — 部署 / 监控 / 备份 / 恢复 / 基准测试一站式管理

## 快速开始

```bash
# 1. 克隆项目
git clone <repo-url> && cd lynx-ollama

# 2. 初始化环境（创建目录、拉取镜像）
./ollama.sh init

# 3. 根据硬件自动优化配置（可选）
./ollama.sh optimize

# 4. 启动服务（同时启动 Web 管理界面）
./ollama.sh start

# 5. 访问 Web 管理界面
# http://<server-ip>:9981

# 6. 拉取模型
./ollama.sh pull qwen2.5:72b-instruct-q4_K_M

# 7. 开始对话
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
| `update` | 智能更新：拉取代码和最新镜像，仅在有实际变更时重建对应服务（无变化则跳过编译） |

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

`optimize` 命令会检测宿主机硬件，自动计算最佳配置并写入 `.env` 文件，然后从模板重新生成 `docker-compose.yaml`：

```bash
# 查看优化方案（不修改文件）
./ollama.sh optimize --dry-run

# 自动应用优化（更新 .env + 重新生成 docker-compose.yaml）
./ollama.sh optimize

# 跳过确认直接应用并重启
./ollama.sh optimize --yes
```

**工作流程：** `optimize` → 检测硬件 → 更新 `.env` → 从 `docker-compose.yaml.template` 重新生成 `docker-compose.yaml` → 重启生效

**自动调整的参数（写入 .env）：**

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

## Web 管理界面

### 安全认证

Web 管理界面（端口 9981）的所有 API 端点均受 **API Key 认证** 保护。

```bash
# 方式 1: 自动生成 API Key（首次启动时在终端输出）
./ollama.sh start
# 输出: 🔑 API Key: olw_a1b2c3d4...

# 方式 2: 通过环境变量指定固定 Key
WEB_API_KEY="my-secret-key" ./ollama.sh start

# 方式 3: 命令行参数
ollama-web --api-key="my-secret-key"
```

**认证方式（API 调用时）：**

| 方式 | 示例 |
|------|------|
| Header（推荐） | `X-API-Key: olw_xxx` |
| Bearer Token | `Authorization: Bearer olw_xxx` |
| Query 参数 | `?key=olw_xxx` |

**豁免端点：** `GET /api/health`（供监控探针使用）、`GET /api/version`（版本信息公开）

**CORS 配置：**

```bash
# 默认: 仅同源访问
# 允许所有来源（开发环境）:
WEB_CORS_ORIGIN="*" ./ollama.sh start
# 指定特定来源:
WEB_CORS_ORIGIN="https://admin.example.com" ./ollama.sh start
```

### Web API 端点

| 端点 | 说明 | 认证 |
|------|------|------|
| `POST /api/auth/verify` | 验证 API Key | ❌ 免认证 |
| `GET /api/health` | 健康检查 | ❌ 免认证 |
| `GET /api/version` | 版本信息 | ❌ 免认证 |
| `GET /api/status` | 综合服务状态 | ✅ |
| `POST /api/service/{start,stop,restart,update}` | 服务控制 | ✅ |
| `GET /api/models` | 已下载模型列表 | ✅ |
| `GET /api/models/search` | 搜索 Ollama 官网模型市场（遍历全部分页） | ✅ |
| `POST /api/models/search/translate` | 批量翻译模型描述为中文 | ✅ |
| `POST /api/models/pull` | 拉取模型 | ✅ |
| `DELETE /api/models/{name}` | 删除模型 | ✅ |
| `GET,PUT /api/config` | 读取/更新配置 | ✅ |
| `GET /api/gpu` | GPU 信息 | ✅ |
| `GET /api/logs` | 服务日志 | ✅ |

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
├── docker-compose.yaml.template # Docker Compose 模板（纳入版本管理，source of truth）
├── docker-compose.yaml          # 运行时配置（从模板生成，不纳入 Git）
├── .env                         # 环境变量配置（由 init/optimize 生成，不纳入 Git）
└── README.md                    # 项目文档
```

> **配置架构**：`docker-compose.yaml.template` 是配置的唯一模板源。模板中使用 `${VAR:-default}` 语法引用变量，运行时 Docker Compose 自动从 `.env` 文件读取变量值。`init` 和 `optimize` 都通过复制模板生成 `docker-compose.yaml`，`optimize` 额外将硬件检测结果写入 `.env`。克隆项目后需执行 `./ollama.sh init` 或 `./ollama.sh optimize` 生成运行时配置。

## 更新日志

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.5.0 | 2026-03-15 | 容器健康检查全链路优化：`GetContainerInfo()` 从 6 次 `docker inspect` 子进程合并为**单次调用**（Go template 一次性提取所有字段），新增 5 秒短期缓存避免同一轮询周期内重复 fork；`IsAPIReady()` 增加结果缓存（5 秒 TTL）+ 复用 HTTP Client，消除 GetStatus 中的重复调用（从 2 次降为 1 次）；新增 `correctHealthStatus()` 统一健康状态修正逻辑——不仅修正 `starting→healthy`，还处理 `unhealthy` + API 可达→`healthy`、空健康值 + 容器运行中→`healthy` 等边界情况；`docker inspect` 返回值统一清洗（处理 `<no value>`/`<nil>` 等 Docker 边界输出）；Docker healthcheck `interval` 从 30s 缩短至 15s、Ollama `start_period` 默认值从 120s 降至 30s；容器操作（start/stop/restart/update/clean）后自动清除缓存确保即时刷新；前端 statusMap 扩展覆盖所有 Docker 容器状态（`created`/`paused`/`restarting`/`removing`/`dead`/`unknown` 等） |
| v1.4.9 | 2026-03-15 | 翻译智能选模优化：`findTranslationModel` 重构为三级优先级选模策略——**优先使用当前运行中的模型**（已加载到 VRAM 中，响应最快），其次从已下载模型中选取适合翻译的模型（qwen/deepseek/llama 等），最后回退到任意可用模型；新增 `isTranslationCapableModel()` 和 `isPreferredTranslationModel()` 辅助函数判断模型翻译能力；翻译超时从 30s 提升到 120s（兼容冷启动加载模型场景）；后端翻译容错——连续 3 次翻译失败后自动停止（避免无谓等待），单条翻译失败不影响后续条目；前端翻译进度展示——新增实时翻译进度指示器（已翻译/总数），连续 3 批失败才停止（而非之前的 1 批失败即停），翻译完成后显示摘要提示（成功数/总数） |
| v1.4.8 | 2026-03-15 | 模型市场全量分页加载：`SearchModels` 后端改为自动遍历 ollama.com 所有分页（`page=1` 到 `page=N`），直到页面返回 "No models found" 为止，一次性获取全部模型（安全上限 50 页）；搜索结果跨页自动去重；翻译解耦为独立 API——新增 `POST /api/models/search/translate` 端点，接收模型名+描述批量翻译（每批上限 10 条），翻译不再阻塞搜索请求；前端两阶段展示——搜索完成后立即渲染英文结果卡片，随后异步分批调用翻译 API 逐步替换描述为中文（带淡入动画），翻译失败不影响已展示的英文内容；网络容错——后续分页请求失败时返回已获取的结果而非报错 |
| v1.4.7 | 2026-03-15 | GPU 统一内存架构适配：后端 `GetGPUInfo` 通过 GPU 名称关键词（GB10/GH200/Grace）和 `[N/A]` 内存值检测统一内存，自动从 `/proc/meminfo` 读取系统总内存替代显存显示；前端 Dashboard GPU 卡片和 GPU 详情页适配统一内存（显示「统一内存架构」标签，无进度条）；健康检查显示「统一内存: X GiB」替代「显存: [N/A] MiB」；CUDA 版本提取修复（`sed` 替代 `awk` 避免尾部管道符）；模型管理分类展示——云端模型（`:cloud`）与本地模型分组显示，带数量统计和总大小汇总；新增「模型市场」Tab 页——搜索 Ollama 官网模型（`GET /api/models/search`），解析 HTML `x-test-*` 属性提取模型名称/描述/参数规格/标签/下载量/更新时间，支持按类型（vision/tools/thinking/embedding/code/cloud）和排序（热门/最新）筛选，卡片式展示搜索结果，一键拉取模型；后端自动检测本地翻译模型（优先 qwen3:8b），将英文模型描述翻译为中文 |
| v1.4.6 | 2026-03-15 | Web 管理界面 API 轮询优化：后端新增 `/api/status/lite` 轻量接口（仅查询容器状态、运行模型、版本，3 项替代完整的 7 项并行查询）；前端智能轮询策略——Dashboard 页面 10 秒全量刷新、其他页面 30 秒轻量刷新、浏览器 Tab 不可见时完全暂停轮询；`status` 命令新增 API Key 显示；`update` 命令 Web 版本直接从工程 `VERSION` 变量读取，不再依赖 HTTP API 调用 |
| v1.4.5 | 2026-03-15 | `update` 命令智能变更检测：`git pull` 前后比对 HEAD commit 判断 `web/` 目录是否有代码变更，`docker pull` 前后比对镜像 ID 判断 Ollama 是否有更新；Ollama 和 Web 均无变化时跳过重建直接提示「一切已是最新」；仅 Ollama 镜像更新时只重建 ollama 容器（不重编译 Web）；仅 Web 代码变更时只 `--build` Web 镜像；两者都变化时才 `--build --force-recreate` 全部重建；清理旧镜像也仅在有变更时执行，大幅减少无效构建耗时 |
| v1.4.4 | 2026-03-15 | Web 界面图标全面升级：favicon、登录页 logo、侧边栏 logo 从 🦙 emoji 替换为 Ollama 官方 PNG 图标（`ollama.png`），提升品牌一致性和视觉质量 |
| v1.4.3 | 2026-03-15 | 修复 Web 日志查看功能不显示日志：`GetLogs` 和 `StreamLogs` 从 `docker compose logs` 改为 `docker logs`（直接通过 Docker API 按容器名获取日志，避免在容器内执行 docker compose 时的项目上下文问题）；`StreamLogs` WebSocket 合并 stderr 到 stdout pipe（`docker logs` 将容器 stderr 输出到自身 stderr），确保所有日志行都能被捕获 |
| v1.4.2 | 2026-03-15 | 修复 GPU 状态获取失败（`nvidia-smi` 在 Web 容器内不可用）：`GetGPUInfo` 改为通过 `docker exec ollama` 在 ollama 容器内执行 `nvidia-smi`，因为只有 ollama 容器配置了 NVIDIA GPU 设备映射 |
| v1.4.1 | 2026-03-15 | 修复 Web 版本显示"未知"：docker-compose 构建 Web 镜像时新增 `VERSION` build arg（通过 `WEB_VERSION` 环境变量传递，默认 v1.4.1），确保版本号正确注入二进制文件；`/api/version` 端点改为免认证（与 `/api/health` 同级），修复 `ollama.sh update` 在未配置 API Key 时因鉴权失败获取不到版本的问题；Shell 注入防护（`docker.go` 新增 `shellQuote()` 对所有路径参数转义）；`.env` 写入改为原子操作（先写 `.tmp` 再 `os.Rename`）+ value 净化防注入；`PullModel` 改用无超时专用 HTTP client 防止大模型下载被截断；`UpdateService` 等待循环增加 `ctx.Done()` 检查 |
| v1.4.0 | 2026-03-15 | Web 管理界面安全加固：新增 API Key 认证中间件（所有 /api/* 端点强制校验，支持 Header/Bearer/Query 三种传递方式）；首次启动自动生成随机 Key 并打印到终端，支持 `WEB_API_KEY` 环境变量和 `--api-key` 命令行参数配置固定 Key；前端添加登录页面（API Key 存入 localStorage，支持 Enter 快捷登录、退出登录、Token 过期自动跳转）；收紧 CORS 策略（默认仅同源，支持 `WEB_CORS_ORIGIN` 配置）；移除 WebSocket `CheckOrigin: true` 宽松校验；修复 4 处 JSON 注入漏洞（`PullModel`/`DeleteModel`/`GenerateChat`/`ShowModel` 中 `fmt.Sprintf` 拼接改为 `json.Marshal` 结构体序列化）；新增 `POST /api/auth/verify` 端点；`GET /api/health` 豁免认证供监控探针使用；**ollama.sh 脚本联动**：`start` 命令启动后显示 Web 管理界面地址和 API Key、`status` 命令显示 Web 容器运行状态、`health` 命令新增 Web 管理界面健康检查项、`help` 命令补充 Web 管理界面说明和环境变量文档；docker-compose 模板传递 `WEB_API_KEY`/`WEB_CORS_ORIGIN` 环境变量、healthcheck 改用免认证的 `/api/health` 端点；`.env` 模板新增 Web 管理界面配置段；**项目审查修复**：修复 `web/.gitignore` 误排除 `cmd/server/` 源码目录（`server` → `/server`）、修复 WebSocket 错误消息 2 处 JSON 注入（`fmt.Sprintf` → `json.Marshal`）、修复 CORS 默认模式反射任意 Origin（改为不设置 CORS 头让浏览器同源策略生效）、根目录 `.gitignore` 的 `.env`/`docker-compose.yaml` 加 `/` 前缀防递归误匹配、新增 `web/.dockerignore` 减小构建上下文、`.env` 模板补充 `OLLAMA_PROJECT_DIR` 变量、Web 宿主机映射端口改为 9981 |
| v1.3.0 | 2026-03-13 | 新增 Web 管理界面（`web/` 目录）：基于 Go + 嵌入式 SPA 架构，提供仪表盘（服务状态、资源监控、模型统计）、服务控制（启动/停止/重启/版本更新）、模型管理（列表/拉取/删除，WebSocket 实时进度）、健康检查（6 项诊断）、实时日志流（WebSocket）、配置在线编辑、GPU 状态监控、清理管理；通过 docker-compose 与 Ollama 服务一起部署，端口 9981 |
| v1.2.3 | 2026-03-13 | 修复状态检测：当 Docker healthcheck 处于 `start_period` 报告 `starting` 时，`status`/`health` 命令主动检测 API 可达性，避免服务已就绪但显示 starting 的误报；修复容器时区：移除 `/etc/localtime` symlink 挂载（宿主机 symlink 在容器内可能解析异常导致时区显示为 "Asia"），改为直接挂载 `/usr/share/zoneinfo/${OLLAMA_TZ}` 到容器 `/etc/localtime`；添加 `ZONEINFO=/usr/share/zoneinfo` 环境变量确保 Go runtime 正确加载时区数据；TZ 改为可配置（`OLLAMA_TZ`） |
| v1.2.2 | 2026-03-13 | 统一所有表格输出使用 Python `display_width()` 渲染：`print_banner()` ASCII art 居中对齐、`do_clean()` 清理操作提示框、`do_pull()` 推荐模型表格，全面消除中英文混排宽度不对齐问题 |
| v1.2.1 | 2026-03-13 | 修复容器时区：增加 `/etc/timezone` 挂载确保 Go runtime 正确识别时区（Go `slog` 日志时间由 `time.Now()` Local 时区决定）；`optimize` 方案表格使用 Python `display_width()` 正确处理中英文混排宽度对齐 |
| v1.2.0 | 2026-03-13 | 重构 `optimize` 与 `init` 配置生成架构：`optimize` 不再直接覆盖 `docker-compose.yaml`，改为更新 `.env` 文件后从模板重新生成；抽出 `generate_compose_from_template()` 和 `update_env_var()` 公共函数；删除 `init` 中的 heredoc 内联默认配置，统一走模板路径；所有配置变更都通过 `.env` + 模板联动 |
| v1.1.1 | 2026-03-13 | 修复容器时间与主机不同步：挂载 `/etc/localtime` 和 `/usr/share/zoneinfo` 到容器，配合 `TZ=Asia/Shanghai` 环境变量确保时区生效 |
| v1.1.0 | 2026-03-13 | `status` 输出格式全面优化：表格精确对齐（中英文混排宽度计算）、容器状态/资源使用格式化、GPU 信息表格化、磁盘使用增加进度条、运行中模型汇总 VRAM、模型列表汇总总大小、并行调度配置表减少 docker exec 调用次数 |

## 作者

**lynxlee**

## License

MIT
