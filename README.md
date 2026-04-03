# Lynx-Ollama

![Version](https://img.shields.io/badge/version-v2.5.3-blue)

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
ollama-console --api-key="my-secret-key"
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
| `GET /api/benchmark/results` | 查询已完成评测结果 | ✅ |
| `POST /api/benchmark/start` | 提交离线评测任务 | ✅ |
| `POST /api/benchmark/stop` | 取消运行中评测 | ✅ |
| `GET /api/benchmark/tasks` | 查询所有评测任务 | ✅ |
| `GET /api/ws/perf` | WebSocket 实时性能监控 | ✅ |
| `GET /api/infer/events` | 推理事件列表（含客户端 IP） | ✅ |

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
├── console/                     # Web 管理界面（Go + 嵌入式 SPA）
├── chat-files/                  # 对话文件持久化目录（按日期子目录，运行时生成）
├── console-data/                # 持久化数据目录（SQLite 数据库，运行时生成）
├── docker-compose.yaml.template # Docker Compose 模板（纳入版本管理，source of truth）
├── docker-compose.yaml          # 运行时配置（从模板生成，不纳入 Git）
├── .env                         # 环境变量配置（由 init/optimize 生成，不纳入 Git）
└── README.md                    # 项目文档
```

> **配置架构**：`docker-compose.yaml.template` 是配置的唯一模板源。模板中使用 `${VAR:-default}` 语法引用变量，运行时 Docker Compose 自动从 `.env` 文件读取变量值。`init` 和 `optimize` 都通过复制模板生成 `docker-compose.yaml`，`optimize` 额外将硬件检测结果写入 `.env`。克隆项目后需执行 `./ollama.sh init` 或 `./ollama.sh optimize` 生成运行时配置。

## 更新日志

| 版本 | 日期 | 变更 |
|------|------|------|
| v2.5.2 | 2026-04-03 | **评测任务状态推送改为 WebSocket**。前端从 `setInterval` 每 5 秒轮询 `GET /api/benchmark/tasks` 改为 `GET /api/ws/benchmark` WebSocket 长连接，后端每 3 秒推送 `{"type":"tasks","data":[...]}` 任务状态，连接时立即推送快照。离开评测页面自动断开 WS，进入时自动连接。消除了大量 GET 请求堆积 |
| v2.5.1 | 2026-04-03 | **修复 StatusHub 并发写 WebSocket 导致 panic**。`broadcast()` 遍历客户端发送消息时，`conn.WriteMessage` 未持有 `client.mu` 锁，与即时快照 goroutine 或其他写操作产生并发写冲突，触发 `panic: concurrent write to websocket connection`。修复：将 `WriteMessage` 调用移入 `c.mu.Lock()` 保护范围内 |
| v2.5.0 | 2026-04-03 | **推理耗时追踪 + 客户端识别**。🔸 **InferenceTracker 服务**：每 5 秒解析 `docker logs ollama` 的 GIN 日志行，正则提取所有 `/api/chat`、`/v1/chat/completions`、`/api/generate` 推理请求的时间戳、耗时、客户端 IP、HTTP 状态码，环形缓冲区保留最近 500 条；🔸 **客户端识别**：自动检测 Console 容器 IP（`hostname -i`），Docker 网关 IP（x.x.x.1）标记为 `external`，其余显示原始 IP，前端用紫色🖥/橙色🌐/青色图标区分；🔸 **推理耗时面板改造**：从无效的折线图改为 SVG 散点图 + 事件表格，每个散点代表一次推理（颜色区分客户端来源），表格显示最近 10 次推理的时间/耗时/客户端/路径/状态码；🔸 **数据来源修正**：原方案依赖 `StreamChat` WebSocket 的 `done` 消息写入 `lastInferMs`，但评测任务（`GenerateChatWithContext`）和外部客户端（`/v1/chat/completions`）不经过该路径，导致始终为 0。改为从容器日志解析，捕获所有来源的推理请求；🔸 **新增 API**：`GET /api/infer/events?window=300` 返回指定时间窗口内的推理事件列表 |
| v2.4.0 | 2026-04-03 | **能力评测系统全面重构**。🔸 **离线评测**：评测任务通过 `POST /api/benchmark/start` 提交后在后端 goroutine 离线执行，不依赖前端在线状态，关闭浏览器后评测继续进行；🔸 **断点续跑**：每完成一个维度立即持久化进度到 SQLite（`UpdateBenchmarkProgress`），服务重启后可从上次中断的维度继续评测；🔸 **模型版本记录**：`benchmark_results` 表新增 `model_digest`/`status`/`completed_dims`/`total_dims` 字段，记录评测时的模型版本 digest，支持 running/completed/cancelled/superseded 状态；🔸 **模型管理评分列**：本地模型列表新增「评测」列，显示百分比总分 + 6 项维度小图标评分（颜色标识：≥8 绿/≥5 黄/<5 红）；🔸 **评分规则说明**：评测页面顶部新增📖评分规则按钮，点击展开详细规则表格（6 个维度的测试内容和评分标准）；🔸 **表格选择模型**：选择评测模型从 checkbox 卡片改为结构化表格，显示模型名称/大小/历史评分/评测时间，支持全选；🔸 **评测结果表格增强**：结果表新增「详情」列，点击📄按钮弹出模态框显示完整评测报告（每个维度的分数/评分依据/token 统计/模型原始回答可折叠展开）；🔸 **运行中任务面板**：评测页显示正在运行的任务进度条，支持单个取消；🔸 **新增 API**：`POST /api/benchmark/start`（提交评测）、`POST /api/benchmark/stop`（取消评测）、`GET /api/benchmark/tasks`（查询所有任务） |
| v2.3.0 | 2026-04-03 | **性能监控三态模式 + 推理耗时采集**。🔸 **三态监控模式**：开关从二态 checkbox 改为三态选择器 —— 🟢 **实时**（默认，页面可见时推送，离开页面暂停）、⏸ **暂停**（停止采集和推送）、📌 **常驻**（始终采集，前端不可达时服务端缓冲最多 1000 帧，前端恢复后批量 flush）；🔸 **状态指示灯**：绿色闪亮 = 采集中，灰色 = 空闲/暂停，红色 = 断开；旁边文字实时显示"采集中/已暂停/常驻采集中/已断开"；🔸 **WS 断线自动重连**：`onclose` 后 3 秒自动重连（非暂停模式），重连后 `resume` 命令触发服务端 flush 缓冲区并恢复推送；🔸 **推理耗时数据采集**：`StreamChat` 的 `done` 消息中提取 `total_duration`（ns→ms）存入 `APIHandler.lastInferMs`（`atomic.Int64`），`GetPerfMetrics` → `StreamPerf` 每帧注入该值，前端推理耗时面板不再显示 `-- ms`；🔸 **CPU 动态上限**：多核 CPU 使用率可超 100%（如 205%），图表 Y 轴上限改为 `max(100, 实际最大值) × 1.1` 自适应；🔸 **服务端缓冲协议**：新增 `perf_batch` 消息类型（数组），`resume` 命令，`mode` 命令（前端切换模式无需重连） |
| v2.2.2 | 2026-04-03 | **模型对比界面自适应满屏 + 可拖拽分隔线**。🔸 对比面板从 CSS Grid 改为 Flex 布局，两个输出面板中间新增 5px 可拖拽分隔线（`compare-resizer`），鼠标拖拽或触摸滑动可左右调整两侧比例（15%-85% 范围），拖拽时分隔线高亮蓝色；🔸 `#page-compare.active` 改为 `display:flex; height:calc(100vh-48px)` 满屏，`.compare-page` 用 `flex:1; height:100%` 填满；🔸 移动端自动隐藏 resizer 并切换为上下堆叠 |
| v2.2.1 | 2026-04-03 | **关键 Bug 修复 + 交互优化**。🔸 **DOMContentLoaded 闭合修复**（根因）：`app.js` 第 2678 行的 `DOMContentLoaded` 回调缺少 `});` 闭合，导致性能监控、模型对比、能力评测、对比复制等约 600 行代码被意外嵌套在异步回调作用域内，`switchPage` 调用这些函数时在严格模式浏览器中报 undefined，修复后所有模块恢复到全局作用域正常工作；🔸 **防误发送机制**：聊天输入框从 Enter 发送改为 Ctrl+Enter / Cmd+Enter 发送，Enter 键仅换行；对比页同步更新；🔸 **全选支持**：聊天输入框 Ctrl+A / Cmd+A 全选文本内容；🔸 **不兼容模型行内标注**：从顶部独立告警卡片改为直接在模型列表行内添加红色「⚠ 需重新下载」标签 + 🔄 重新拉取按钮，hover 显示错误详情，不兼容行背景高亮；🔸 **复制功能全面兜底**：所有 `navigator.clipboard.writeText` 调用（聊天导出文本/Markdown、对比页单侧复制/全局复制）统一添加 `fallbackCopy()` textarea 降级方案，兼容非 HTTPS 环境 |
| v2.2.0 | 2026-04-03 | **仪表盘实时性能监控**。🔸 **6 面板实时图表**：新增「📈 性能监控」区域，纯 SVG 零依赖折线图展示 CPU 使用率、GPU 使用率、内存使用、网络 IO（入/出）、磁盘 IO（读/写）、推理耗时，80% 阈值红色虚线预警；🔸 **WebSocket 独立通道**：`GET /api/ws/perf` 端点，客户端可发送 `start/stop/interval` 控制采集，后端 `GetPerfMetrics` 单次调用采集 docker stats + nvidia-smi 数据；🔸 **工具栏控制**：实时刷新开关（绿色圆点）、采集间隔选择（3s/5s/10s）、时间窗口选择（1min/5min/15min）；🔸 **智能生命周期**：离开 Dashboard 自动暂停采集，切换回来恢复，Tab 不可见时暂停，可见时恢复；🔸 **网络/磁盘速率计算**：后端返回累计字节数，前端差值计算实时速率并自动选择 B/KB/MB/GB 单位；🔸 **响应式布局**：3 列 → 2 列 → 1 列自适应，移动端友好；🔸 **Lint 修复**：app.js 多余闭括号、gpu_monitor.go 未使用参数、ollama.go 应使用 tagged switch、docker.go KiB 转换数学错误 |
| v2.1.1 | 2026-04-03 | **Bug 修复与体验优化**。🔸 **复制功能修复**：代码块复制按钮改为独立 `copyCodeBlock()` 函数解决 HTML 实体转义问题，气泡复制按钮改用 `innerText` 兜底 + `fallbackCopy()` textarea 方案兼容非 HTTPS 环境；🔸 **代码语法高亮**：内置轻量级语法高亮引擎（`highlightCode`），支持 20+ 语言关键词/字符串/注释/数字着色，暗色和亮色主题各有独立配色；🔸 **云端模型增强**：云端模型表格新增类型、能力列和💬测试按钮，Chat/对比模型选择器不再过滤云端模型；🔸 **模型对比数据加载**：`initCompare()` 增加 fallback API fetch，解决首次进入对比页面下拉框为空的问题，工具栏 select 改为 CSS flex 响应式宽度；🔸 **能力评测数据加载**：`initBenchmark()` 增加 fallback API fetch，解决模型列表显示「加载中」不消失的问题；🔸 **拉取完成按钮状态**：模型下载完成后「开始拉取」按钮变为绿色「✅ 完成」，点击关闭弹窗，下次打开自动恢复；🔸 **模型兼容性检测**：新增 `GET /api/models/check` 端点 + `ShowModel` HTTP 500 处理，模型管理页自动检测不兼容模型并显示红色告警卡片，支持一键重新拉取；🔸 **Vision 误判修复**：`model_info` 键名扫描收紧（仅 `mmproj`/`projector.block_count` 才标记 vision），启动时清除旧 show 缓存 |
| v2.1.0 | 2026-04-02 | **模型管理增强 + 对话拷贝 + 模型对比完善 + 能力评测系统**。🔸 **模型列表快捷测试**：已下载模型操作列新增「💬 测试」按钮，点击跳转到模型测试页并自动选中该模型；🔸 **对话拷贝功能**：每条消息气泡 hover 显示复制按钮（📋 文本 / 📝 Markdown），工具栏「导出」按钮改为多格式菜单（复制文本/Markdown、下载 MD/JSON、截图为图片）；🔸 **模型对比界面完善**：新增页面标题和设置面板（System Prompt、Temperature、Thinking 模式），支持 Enter 快捷发送，对比生成中显示停止按钮，每个面板支持单独复制输出，新增「复制对比结果」按钮（Markdown 格式），统计指标改为结构化标签显示，支持 Thinking 推理链折叠展示；🔸 **模型能力评测系统**：新增「📊 能力评测」页面，支持多模型并发评测 6 个维度（逻辑推理/数学计算/代码能力/创意写作/指令遵循/中文能力），每项 10 分共 60 分，基于规则引擎自动评分（关键词匹配 + JSON 格式验证 + 长度检查），WebSocket 流式进度推送，评测结果排行榜含百分比进度条/维度雷达分/详细评分卡片，结果持久化到 SQLite（`benchmark_results` 表），支持历史结果查看；🔸 **Go 版本升级**：go.mod + Dockerfile 统一升级到 Go 1.25（匹配本地 go1.25.5） |
| v2.0.0 | 2026-04-02 | **全面功能升级**。🔸 **对话历史持久化**：SQLite 新增 `chat_sessions`/`chat_messages` 表，支持多会话切换、历史浏览、删除、重命名，对话完成后自动保存（标题从首条用户消息生成）；🔸 **对话导出**：支持 Markdown 和 JSON 格式导出，通过 `GET /api/chat/sessions/{id}/export` API 下载；🔸 **模型详情弹窗美化**：`showModelInfo` 从 `alert(JSON)` 改为模态框，结构化展示参数规模/量化/格式/模型族/上下文长度，折叠展示 Modelfile 参数、System Prompt、模板、许可证；🔸 **Thinking 模式**：设置面板新增思维链开关，后端透传 `think: true` 到 Ollama，前端区分 `thinking` token 和 `content` token，思维链以紫色折叠块展示；🔸 **模型对比页**：新增导航页面「⚖️ 模型对比」，选择两个模型输入同一 prompt 并排对比输出和性能指标（tokens/耗时/速率），各自独立 WebSocket 流式传输；🔸 **代码质量优化**：Dockerfile runtime 改 `alpine:3`；`enrichModelCapabilities` 消除 slice 数据竞争；`GetStatus` 从 `ListRunningModels` 推断 API 可达性减少冗余 probe；全量 `interface{}` → `any`；清理 unused 变量；🔸 **Go 版本升级**：go.mod + Dockerfile 统一升级到 Go 1.25（匹配本地 go1.25.5） |
| v1.9.0 | 2026-04-02 | **SQLite 持久化存储 + 模型能力标签**。1️⃣ **新增 SQLite 数据库**（`console-data/console.db`）：`model_meta` 表持久化模型能力标签和类型，`translations` 表持久化模型描述翻译缓存（替代内存 `sync.Map`，重启不丢失）；2️⃣ **模型能力检测**：三级来源——从 Ollama `/api/show` 解析 `details.families`（clip→vision）和 `template`（tool→tools）、从模型市场 `tags` 同步、从模型名关键词推断；首次查询后写入 SQLite，后续直接读取；3️⃣ **模型列表增强**：已下载模型表格新增「类型」列（💬对话/👁视觉/📐嵌入/💻代码）和「能力」列（vision/tools/thinking/code/embedding 标签）；4️⃣ **翻译缓存持久化**：模型市场描述翻译结果存入 SQLite，相同英文描述直接返回缓存译文，不再重复调用 LLM；5️⃣ 依赖新增 `modernc.org/sqlite`（纯 Go，无需 CGO） |
| v1.8.4 | 2026-04-02 | **对话停止修复 + 错误提示优化**。1️⃣ 修复停止按钮无法中止生成的 bug：`StreamChat` 重写为单一读 goroutine + channel 调度，解决 gorilla/websocket 并发读竞争和 `ReadBytes` 阻塞导致 cancel 无法生效的问题，`reader.Close()` 立即断开 Ollama 连接中止生成；2️⃣ 前端发送校验：上传图片时自动检测模型是否支持视觉能力（`isVisionModel` 匹配 llava/vision/minicpm-v 等关键词），不支持则阻止发送并提示选择视觉模型；3️⃣ 错误中文友好化：`friendlyChatError()` 识别 6 种常见错误（图片不支持/模型不存在/上下文过长/显存不足/连接失败/超时）返回中文提示 |
| v1.8.3 | 2026-04-01 | **智能参数预设系统**。切换模型时自动填充最优参数，三级优先级：1️⃣ **用户自定义预设**（localStorage 持久化）— 设置面板底部新增「💾 保存预设」/「🗑 删除预设」按钮，用户调好参数后保存为该模型专属预设，下次选择自动加载；2️⃣ **Ollama 模型默认参数**（`/api/show`）— 从模型 Modelfile 的 `parameters` 字段解析 temperature/top_p/num_ctx/num_predict；3️⃣ **内置推荐预设**（11 个模型族）— Qwen(0.7/0.8/32K)、DeepSeek(0.6/0.9/64K)、CodeLlama/Coder(0.2/高ctx)、Llama(0.7/0.9/8K)、Mistral/Mixtral(32K)、Gemma、Phi、LLaVA、Command-R(0.3/128K) 等。设置面板底部显示当前参数来源标签（「用户预设」/「模型默认」/「Qwen 推荐」等） |
| v1.8.2 | 2026-04-01 | **对话设置面板**。1️⃣ **System Prompt**：工具栏新增「⚙️ 设置」按钮，打开右侧滑入式设置面板，支持配置系统角色提示词（自动注入为 messages 首条 system 消息）；2️⃣ **参数调节**：Temperature（0-2）、Top P（0-1）滑块实时显示数值，上下文长度（num_ctx 2K-128K）、最大生成长度（num_predict 256-8K/无限制）下拉选择；3️⃣ **JSON 模式**：开关切换，开启后通过 Ollama `format: "json"` 强制模型输出有效 JSON；4️⃣ **keep_alive**：模型选择旁新增驻留时间下拉（5m/30m/1h/4h/24h/永久），控制模型在显存中的保持时间；后端 `ChatStream` 新增 `format`/`keep_alive` 参数透传 |
| v1.8.1 | 2026-04-01 | **文件持久化 + 图片多模态 + 自适应优化**。1️⃣ **文件持久化存储**：上传文件和 LLM 生成文件从内存改为磁盘持久化，存储在 `chat-files/<日期>/<fileID>/` 目录下（含 metadata.json + 原始文件），服务重启不丢失，宿主机可直接管理和清理；新增 `ChatFileStore` 服务层，支持按日期分目录、启动时加载当日缓存、7 天内文件自动检索；2️⃣ **图片多模态支持**：文件上传新增图片格式（jpg/png/gif/webp/bmp），图片 base64 编码后通过 Ollama `/api/chat` 的 `images` 字段传入，需配合视觉模型（llava/llama3.2-vision）使用；上传标签和气泡中显示图片缩略图预览；3️⃣ **Chat 页面自适应**：三档响应式断点（>768px/≤768px/≤480px），工具栏/气泡/输入区弹性布局，代码块和表格横向滑动，消息区独立滚动；4️⃣ `GetVersion` 缓存 TTL 调整为 1 小时；docker-compose ollama 容器挂载 `chat-files` 共享目录 |
| v1.8.0 | 2026-04-01 | **新增模型测试（对话）功能 + 状态轮询优化**。1️⃣ **流式对话**：侧边栏新增「💬 模型测试」页面，通过 WebSocket (`/api/ws/chat`) 与本地大模型多轮流式对话，支持停止生成、文件上传（txt/md/csv/json/yaml/代码等文本文件作为上下文）、Markdown 富文本渲染（代码块带复制按钮、表格、列表、标题、链接、图片）、生成统计（token 数/耗时/速度）；2️⃣ **状态轮询优化**：`GetVersion` 新增 60s 缓存（版本号几乎不变，从每 5s 查询降为每 60s）；`IsAPIReady` 独立探针（`GET /`）移除，改为从 `ListRunningModels` 的成败推断 API 可达性；Lite/Full 状态采集均减少 2 个并行 goroutine；整体 Ollama API 调用量降低约 66%；更新完成后自动清除版本缓存 |
| v1.7.9 | 2026-04-01 | **Ollama 版本更新交互增强**。1️⃣ 后端新增 `GetLatestVersion()` 方法，通过 GitHub API (`/repos/ollama/ollama/releases/latest`) 查询 Ollama 最新发布版本号；2️⃣ `StreamUpdate` 版本检测从 Docker image digest 比对改为**版本号比对**（当前版本 vs GitHub 最新版本），版本相同则提示「当前已是最新版本 (x.x.x)」不执行任何操作；3️⃣ 发现新版本时弹确认框同时显示当前版本号和最新版本号，确认后显示 `正在更新 x.x.x → y.y.y`；4️⃣ 取消更新时顶栏版本处标黄显示 `x.x.x → y.y.y`，hover 提示两个版本号；5️⃣ `do_build` 新增模板同步检查，构建前自动检测 `docker-compose.yaml` 与模板差异并重新生成；6️⃣ 修复 `ollama.sh` help 输出端口号颜色未正确显示（`echo` → `echo -e`） |
| v1.7.8 | 2026-04-01 | **目录重命名 + 更新版本交互优化**。1️⃣ `web/` 目录重命名为 `console/`，所有引用同步更新：Go module `lynx-ollama-web` → `lynx-ollama-console`、二进制名/Docker 镜像名/容器名 `ollama-web` → `ollama-console`、`docker-compose.yaml.template` build context `./web` → `./console`、`ollama.sh` 中所有路径和容器名引用；2️⃣ 更新版本流程优化：点击「更新版本」后先自动检查是否有新版本——已是最新则直接提示无需操作；发现新版本时弹确认框显示当前版本号、询问是否更新并重启——确认则立即执行更新+重启流程，取消则在顶栏 Ollama 版本处标黄提示「有新版本」；后端 `StreamUpdate` WebSocket 新增 `update_available`/`cancelled` 阶段，支持前端发送 `confirm`/`cancel` 消息控制更新流程 |
| v1.7.7 | 2026-03-20 | **GPU 监控全面增强**。新增 GPU 详细信息显示：持久化模式、PCIe 总线 ID、显示活跃状态、ECC 错误计数、风扇转速、性能状态 (P-State)、计算模式、MIG 模式；新增 GPU 进程列表显示（进程 PID、名称、显存占用）；前端 GPU 卡片优化布局，支持进程列表滚动显示 |
| v1.7.6 | 2026-03-20 | **新增 build 命令**。新增 `./ollama.sh build [--recreate]` 命令，强制构建 Web 管理界面镜像；支持 `--recreate` 参数强制重新创建容器以应用新配置；适用于修改 Web 源码后快速重新构建、版本更新后强制构建、开发调试等场景 |
| v1.7.5 | 2026-03-19 | **GPU 自动监控与重启**。新增 GPU 监控服务（`GPUMonitorService`），每 30 秒检测 GPU 状态，当检测到 GPU 不可用（统一内存架构未初始化、显存信息为 [N/A]、无 GPU 等）时自动重启 Ollama 容器；智能重启策略：冷却时间 5 分钟、每小时最多重启 3 次，防止无限重启循环；支持统一内存架构（GB10/GH200/Grace Hopper）和普通 GPU；Web 服务启动时自动启动监控，优雅关闭时自动停止 |
| v1.7.4 | 2026-03-16 | **已下载模型列表搜索与排序**。模型管理页「已下载模型」tab 新增搜索框（按名称/family 实时过滤）和可排序表头（点击名称/大小/修改时间列切换升降序），云端和本地模型表格共享同一套搜索排序状态 |
| v1.7.3 | 2026-03-16 | **Web healthcheck 轻量化 + update 自动同步模板**。1️⃣ `ollama-web` 容器的 Docker healthcheck 从 `/api/health`（每次调用 `IsAPIReady` + `GetVersion` 向 Ollama 发送 `GET /` + `GET /api/version`）改为新增的 `/api/ping` 端点（仅返回 `{"status":"ok"}`，零外部调用），彻底消除无客户端时 Web 容器 healthcheck 产生的 Ollama API 请求；2️⃣ `ollama.sh update` 新增模板同步：`git pull` 后自动检测 `docker-compose.yaml.template` 是否与当前 `docker-compose.yaml` 不一致，有变更时自动备份旧文件并从模板重新生成，确保 healthcheck 等配置变更随代码更新自动生效 |
| v1.7.2 | 2026-03-16 | **StatusHub 按需轮询优化**。当没有任何 Web 客户端连接（或所有客户端都切换到后台标签页暂停）时，后端 StatusHub 自动停止对 Ollama/Docker 的定时轮询（`GET /`、`GET /api/version` 等），减少无意义的网络请求和日志输出；当有新客户端连接或从暂停恢复时自动重新启动轮询。核心改动：`StatusHub` 从 `sync.Once` 一次性启动改为动态启停——`add()` 时检测并启动、`remove()` 时检测并停止、客户端 `pause`/`resume` 时触发 `onClientStateChange()` 重新评估；`NewAPIHandler` 中移除 `statusHub.Start()` 调用 |
| v1.7.1 | 2026-03-16 | **服务控制操作隔离 + 流式进度显示**。1️⃣ **启动/停止/重启只操作 Ollama 容器**：`StartService` 从 `docker compose up -d`（操作全部容器）改为 `docker start ollama`（仅启动 Ollama），`StopService` 从 `docker compose down`（停止并删除全部容器）改为 `docker stop ollama`（仅停止 Ollama），`RestartService` 从 `docker compose down && up -d` 改为 `docker restart ollama`（仅重启 Ollama），避免 Web 管理面板被误操作下线；2️⃣ **服务控制流式进度推送**：新增 `/api/ws/service?action=start|stop|restart` WebSocket 端点和后端 `StartServiceStream`/`StopServiceStream`/`RestartServiceStream` 方法，操作过程分阶段推送进度（检查状态→执行操作→等待 API 就绪→完成）；3️⃣ **前端进度条统一化**：服务控制按钮从同步 HTTP POST + Toast 改为 WebSocket 流式连接 + 进度条，与「更新版本」共用同一个进度条 UI 组件，实时显示操作阶段和状态文本，成功/失败后自动隐藏 |
| v1.7.0 | 2026-03-16 | **Ollama 更新版本流式进度 + 版本检查**。1️⃣ **更新前自动检查是否已是最新**：后端新增 `CheckImageUpdate()` 方法，通过 `docker image inspect` 获取本地镜像 digest、`docker manifest inspect` 获取远程 digest 进行比对，镜像未变化时直接提示「当前版本已是最新」，跳过无意义的 pull 操作；2️⃣ **流式更新进度推送**：新增 `/api/ws/update` WebSocket 端点和后端 `UpdateServiceStream()` 方法，`docker pull` 通过 `StdoutPipe` 逐行读取输出并实时推送到前端，覆盖检查更新→拉取镜像→重建容器→等待就绪四个阶段；3️⃣ **前端进度条 UI**：「更新版本」按钮从同步 HTTP POST 改为 WebSocket 流式连接，服务控制卡片下方新增进度条，实时显示每个阶段的状态文本和进度百分比，更新完成后显示版本变化（old → new）或「已是最新」，8 秒后自动隐藏进度条；4️⃣ **`ollama.sh update` 容器版本一致性检查**：即使 `git pull` 无新变更，update 命令现在会通过 `/api/version` 接口比对运行中 Web 容器版本与源码版本，不一致时自动触发 `--build` 重建 Web 镜像，杜绝"代码已更新但容器还是旧版本"的问题 |
| v1.6.3 | 2026-03-15 | **模型翻译全量覆盖 + 版本定义收敛**。1️⃣ **模型描述翻译支持全量覆盖**：后端 `TranslateModelDescriptions` 移除硬编码 `maxBatch=100` 截断限制，改为分批 100 循环调用 `TranslateDescriptions`（每批独立 LLM 调用），支持翻译 500+ 条模型描述；前端 `translateMarketDescriptions` 同步改为分批循环发送请求（每批 100 条），进度条实时显示「第 N/M 批」，单批失败不中断后续批次；2️⃣ **版本号定义收敛为单一真相源**：唯一版本定义位置 `web/cmd/server/main.go` → `var Version = "vX.Y.Z"`；`ollama.sh` 不再硬编码 `VERSION="v1.6.x"`，改为运行时从 `main.go` 自动提取（`grep -m1 'var Version'`）；`docker-compose.yaml.template` 默认值从 `v1.6.2` 改为 `latest`（`ollama.sh` 总是 `export WEB_VERSION`）；`build.sh` 和 `Dockerfile` 早已自动提取，无需改动。今后升级版本只需修改 `main.go` 一处 |
| v1.6.2 | 2026-03-15 | **WebSocket 数据推送扩展 + UI 精简**。1️⃣ **Lite 模式新增 GPU 数据采集**：后端 `collectLiteStatus` 和 HTTP `GET /api/status/lite` 从 3 路并行查询扩展到 4 路（新增 `GetGPUInfo`），WebSocket lite 推送现在包含 GPU 状态数据；2️⃣ **GPU 页面实时刷新**：前端 GPU 页面不再依赖独立 HTTP 请求（`GET /api/gpu`），改为从 WebSocket 推送数据实时渲染（每 5 秒自动更新），切换到 GPU 页面立即显示缓存数据，离开 GPU 页面后停止 GPU 渲染（不再浪费 DOM 操作）；保留手动刷新按钮作为 HTTP fallback；3️⃣ **移除系统设置页面**：主题切换已集成到 Topbar 按钮组（v1.6.1），系统设置页面仅含主题切换功能已属冗余，移除侧边栏"🎨 系统设置"导航项、`page-settings` HTML 区域和 CSS 样式（~60 行） |
| v1.6.1 | 2026-03-15 | **新增全局顶部导航栏（Topbar）**，参考 OpenClaw Client 设计语言。Topbar 包含：左侧 Ollama logo + 项目名称 + "管理面板"副标题；右侧 WebSocket 实时连接状态指示灯（🟢 已连接 / 🟡 连接中 / 🔴 未连接）、主题快速切换按钮组（☀️/🌙/🖥，支持浅色/深色/跟随系统）、项目版本号徽章 + Ollama 引擎版本号、退出登录按钮。原侧边栏精简为纯导航菜单 + 底部 API 状态指示灯，移除了 sidebar-header/sidebar-meta/版本号/退出按钮等冗余元素。整体布局从 `flex(horizontal)` 调整为 `flex(vertical: topbar + flex(horizontal: sidebar + content))`。小屏幕适配：768px 以下隐藏副标题、WebSocket 状态文字和版本号 |
| v1.6.0 | 2026-03-15 | **前端通信架构升级：HTTP 轮询 → WebSocket 实时推送**。新增 `GET /api/ws/status` WebSocket 端点，后端通过 StatusHub（单一数据采集 goroutine + 多客户端广播模式）每 5 秒主动推送状态数据，替代前端 `setInterval` 10/30 秒周期性 HTTP 轮询；客户端支持发送 `subscribe`（切换 full/lite 模式）、`pause`/`resume`（Tab 隐藏时暂停推送）控制命令；WebSocket 断连时自动指数退避重连（1s→30s）；首次连接立即推送一次快照避免 5 秒等待；**显著降低前端内存缓存负载**——消除了 `setInterval` 定时器和冗余 HTTP 请求/响应对象堆积；后端共享数据采集避免每个客户端独立查询 Docker/Ollama。修复 `project_version` 显示为 `dev` 的问题：`build.sh` 和 `Dockerfile` 不再硬编码 `VERSION=dev` 默认值，改为自动从 `main.go` 源码中提取版本号（`grep var Version`），确保无论通过 `docker compose build`、`./build.sh` 还是 `docker build` 构建，版本号都与源码一致 |
| v1.5.2 | 2026-03-15 | 新增**系统设置页面**及**外观切换**功能：支持浅色模式、深色模式和跟随系统三种外观主题；浅色模式下 Ollama 图标自动显示为黑色，深色模式下显示为白色（通过 CSS 变量 `--logo-invert` 控制 `filter: invert()`）；主题偏好保存在 `localStorage`，刷新页面后自动恢复；监听 `prefers-color-scheme` 媒体查询实时响应操作系统外观变化；浅色主题定义完整的 CSS 变量覆盖（背景、文字、边框、强调色、阴影等）；侧边栏新增 🎨 系统设置导航项 |
| v1.5.1 | 2026-03-15 | 模型翻译效率大幅提升：**批量 JSON 翻译**——将所有模型描述打包为 JSON 对象通过一次 LLM 调用完成翻译（之前 N 条描述 = N 次串行调用），前端也从分批 5 条改为一次性发送全部；**翻译缓存机制**——新增 `sync.Map` 内存缓存（英文描述→中文翻译），首次翻译后缓存结果，刷新模型市场或重新搜索时直接读取缓存、不再调用大模型；**优雅降级**——批量 JSON 解析失败时自动回退为逐条翻译（`fallbackIndividualTranslate`），确保翻译可用性；翻译 prompt 重新设计为 JSON-in/JSON-out 格式，新增 `think:false` 禁用思维链、`stripMarkdownCodeBlock` 清理大模型返回的 markdown 代码块包裹；API Handler 批量上限从 10 条放宽至 100 条 |
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
