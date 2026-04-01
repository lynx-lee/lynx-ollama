# Ollama Web 管理界面

基于 Go 的轻量级 Web 管理界面，将 `ollama.sh` 的所有功能图形化展示。

## 功能

| 功能 | 描述 |
|------|------|
| 🐳 服务控制 | 启动、停止、重启 Ollama 服务 |
| ⬆ 版本更新 | 一键拉取最新镜像并重建容器 |
| 📊 仪表盘 | 容器状态、资源使用、模型统计总览 |
| 🤖 模型管理 | 列表、拉取（实时进度）、删除、详情 |
| 🩺 健康检查 | 6 项健康诊断（容器、API、GPU、磁盘等） |
| 📋 日志查看 | 历史日志加载 + WebSocket 实时流 |
| ⚙️ 配置管理 | 在线编辑 `.env` 配置 + 自动优化 |
| 🎮 GPU 状态 | 显存、利用率、温度、功耗实时监控 |
| 🧹 清理管理 | 轻度/深度清理容器和镜像 |

## 架构

```
web/
├── cmd/server/main.go          # 入口
├── internal/
│   ├── config/config.go        # 配置
│   ├── model/types.go          # 数据模型
│   ├── service/
│   │   ├── ollama.go           # Ollama API 交互
│   │   ├── docker.go           # Docker 容器管理
│   │   └── system.go           # 系统配置与健康检查
│   └── handler/
│       ├── router.go           # HTTP 路由
│       ├── api.go              # API Handler
│       ├── cmd.go              # 命令辅助
│       └── static/             # 嵌入式前端 (embed.FS)
│           ├── index.html
│           ├── style.css
│           └── app.js
├── Dockerfile                  # 多阶段构建
├── build.sh                    # 构建脚本
├── go.mod
└── go.sum
```

## 快速开始

### 方式 1: Docker Compose（推荐）

Web 界面已集成到 `docker-compose.yaml.template` 中，随 Ollama 一起部署：

```bash
# 在项目根目录
./ollama.sh init
./ollama.sh start
# Web 界面自动启动在 http://<host>:9981 (端口可通过 .env 的 WEB_PORT 配置)
```

### 方式 2: 独立构建运行

```bash
cd web

# 本地编译
bash build.sh v1.0.0 local
./ollama-web --project-dir /opt/ai/ollama --ollama-url http://localhost:11434

# 交叉编译 Linux ARM64（适配 DGX Spark）
bash build.sh v1.0.0 linux-arm64
# 将 ollama-web-linux-arm64 上传到服务器运行
```

### 方式 3: Docker 构建

```bash
cd web
bash build.sh v1.0.0 docker
docker run -d -p 9981:8080 \
  -v /opt/ai/ollama:/opt/ai/ollama \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e OLLAMA_API_URL=http://ollama:11434 \
  ollama-web:v1.0.0
```

## API 端点

所有 `/api/*` 端点（除标注外）需携带 API Key 认证。

| 方法 | 路径 | 描述 | 认证 |
|------|------|------|------|
| GET | `/api/health` | 健康检查 | 豁免 |
| POST | `/api/auth/verify` | 验证 API Key | 豁免 |
| GET | `/api/version` | 获取版本信息 | ✅ |
| GET | `/api/status` | 获取综合状态 | ✅ |
| POST | `/api/service/start` | 启动服务 | ✅ |
| POST | `/api/service/stop` | 停止服务 | ✅ |
| POST | `/api/service/restart` | 重启服务 | ✅ |
| POST | `/api/service/update` | 更新版本 | ✅ |
| GET | `/api/models` | 模型列表 | ✅ |
| GET | `/api/models/running` | 运行中模型 | ✅ |
| POST | `/api/models/pull` | 拉取模型 | ✅ |
| DELETE | `/api/models/{name}` | 删除模型 | ✅ |
| GET | `/api/models/{name}/info` | 模型详情 | ✅ |
| GET | `/api/models/search` | 搜索 Ollama 官网模型市场 | ✅ |
| POST | `/api/models/search/translate` | 批量翻译模型描述 | ✅ |
| POST | `/api/models/generate` | 生成对话 | ✅ |
| GET | `/api/gpu` | GPU 信息 | ✅ |
| GET | `/api/logs` | 获取日志 | ✅ |
| GET | `/api/config` | 读取配置 | ✅ |
| PUT | `/api/config` | 更新配置 | ✅ |
| POST | `/api/optimize` | 自动优化 | ✅ |
| POST | `/api/clean` | 清理操作 | ✅ |
| WS | `/api/ws/logs` | 实时日志流 | ✅ |
| WS | `/api/ws/pull` | 模型拉取进度 | ✅ |

## 配置

通过环境变量或命令行参数配置：

| 环境变量 | 命令行 | 默认值 | 描述 |
|----------|--------|--------|------|
| `WEB_LISTEN_ADDR` | `--listen` | `0.0.0.0:8080` | 容器内监听地址 |
| `WEB_API_KEY` | `--api-key` | 自动生成 | API Key（留空则首次启动自动生成） |
| `WEB_CORS_ORIGIN` | `--cors-origin` | 仅同源 | CORS 允许源（`*`=所有，逗号分隔多个） |
| `OLLAMA_API_URL` | `--ollama-url` | `http://localhost:11434` | Ollama API 地址 |
| `OLLAMA_PROJECT_DIR` | `--project-dir` | `/opt/ai/ollama` | 项目目录 |
| `OLLAMA_SCRIPT_PATH` | `--script` | `<project-dir>/ollama.sh` | 脚本路径 |
| `WEB_LOG_LEVEL` | `--log-level` | `info` | 日志级别 |
