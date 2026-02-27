#!/bin/bash

#===============================================================================
# Ollama AI 服务部署脚本 (适配 NVIDIA DGX Spark / GB10)
#
# 作者: lynxlee
#
# 用法:
#   ./deploy.sh [命令] [选项]
#
# 命令:
#   start       启动 Ollama 服务
#   stop        停止 Ollama 服务
#   restart     重启 Ollama 服务
#   status      查看服务状态与GPU信息
#   logs        查看服务日志
#   update      更新镜像并重启
#   clean       清理容器与镜像
#   init        初始化部署环境
#   backup      备份模型与配置
#   restore     恢复模型与配置
#   pull        拉取/更新模型
#   rm          删除已下载模型
#   models      列出已下载模型
#   run         交互式运行模型
#   bench       运行性能基准测试
#   gpu         查看GPU详细信息
#   exec        进入容器Shell
#   health      执行健康检查
#   optimize    检测硬件并优化docker-compose配置
#   search      搜索Ollama官网模型(自动匹配本机硬件)
#   help        显示帮助信息
#
#===============================================================================

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m' # No Color

# 项目配置
PROJECT_NAME="ollama"
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCKER_COMPOSE_FILE="${PROJECT_DIR}/docker-compose.yaml"
BACKUP_DIR="${PROJECT_DIR}/backups"
LOG_DIR="${PROJECT_DIR}/logs"
DATA_DIR="/opt/ai/ollama/ollama_data"
OLLAMA_HOST="localhost"
OLLAMA_PORT="11434"
OLLAMA_API="http://${OLLAMA_HOST}:${OLLAMA_PORT}"

# 默认配置
COMPOSE_PROJECT_NAME="${PROJECT_NAME}"

#-------------------------------------------------------------------------------
# 工具函数
#-------------------------------------------------------------------------------

log_info() {
    echo -e "${BLUE}[INFO]${NC} $(date '+%H:%M:%S') $1"
}

log_success() {
    echo -e "${GREEN}[OK]${NC}   $(date '+%H:%M:%S') $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $(date '+%H:%M:%S') $1"
}

log_error() {
    echo -e "${RED}[FAIL]${NC} $(date '+%H:%M:%S') $1"
}

log_step() {
    echo -e "${MAGENTA}[STEP]${NC} $(date '+%H:%M:%S') $1"
}

print_banner() {
    echo -e "${CYAN}"
    cat << 'BANNER'
╔══════════════════════════════════════════════════════════════════╗
║                                                                  ║
║      ██████╗ ██╗     ██╗      █████╗ ███╗   ███╗ █████╗         ║
║     ██╔═══██╗██║     ██║     ██╔══██╗████╗ ████║██╔══██╗        ║
║     ██║   ██║██║     ██║     ███████║██╔████╔██║███████║        ║
║     ██║   ██║██║     ██║     ██╔══██║██║╚██╔╝██║██╔══██║        ║
║     ╚██████╔╝███████╗███████╗██║  ██║██║ ╚═╝ ██║██║  ██║        ║
║      ╚═════╝ ╚══════╝╚══════╝╚═╝  ╚═╝╚═╝     ╚═╝╚═╝  ╚═╝        ║
║                                                                  ║
║          Ollama AI 服务部署工具  ·  DGX Spark Edition            ║
╚══════════════════════════════════════════════════════════════════╝
BANNER
    echo -e "${NC}"
}

print_separator() {
    echo -e "${DIM}──────────────────────────────────────────────────────────────${NC}"
}

# 格式化字节数为可读单位
format_bytes() {
    local bytes=$1
    if [ "$bytes" -ge 1073741824 ]; then
        echo "$(awk "BEGIN {printf \"%.1f GiB\", $bytes/1073741824}")"
    elif [ "$bytes" -ge 1048576 ]; then
        echo "$(awk "BEGIN {printf \"%.1f MiB\", $bytes/1048576}")"
    else
        echo "${bytes} B"
    fi
}

# 检查 Ollama API 是否可达
is_api_ready() {
    curl -sf "${OLLAMA_API}/" --connect-timeout 3 > /dev/null 2>&1
}

# 等待 API 就绪
wait_for_api() {
    local max_wait="${1:-120}"
    local elapsed=0

    log_info "等待 Ollama API 就绪 (最长 ${max_wait}s)..."

    while [ $elapsed -lt $max_wait ]; do
        if is_api_ready; then
            log_success "Ollama API 已就绪 (耗时 ${elapsed}s)"
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
        printf "\r  等待中... %ds / %ds" "$elapsed" "$max_wait"
    done

    echo ""
    log_error "Ollama API 启动超时 (${max_wait}s)"
    return 1
}

#-------------------------------------------------------------------------------
# 系统检查
#-------------------------------------------------------------------------------

check_requirements() {
    log_info "检查系统依赖..."
    local checks_passed=true

    # 检查 Docker
    if command -v docker &> /dev/null; then
        local docker_ver
        docker_ver=$(docker --version | grep -oP '\d+\.\d+\.\d+' | head -1)
        echo -e "  Docker:          ${GREEN}✓${NC} v${docker_ver}"
    else
        echo -e "  Docker:          ${RED}✗ 未安装${NC}"
        echo "    安装指南: https://docs.docker.com/get-docker/"
        checks_passed=false
    fi

    # 检查 Docker Compose
    if docker compose version &> /dev/null; then
        local compose_ver
        compose_ver=$(docker compose version --short 2>/dev/null || docker compose version | grep -oP '\d+\.\d+\.\d+')
        echo -e "  Docker Compose:  ${GREEN}✓${NC} v${compose_ver}"
    elif command -v docker-compose &> /dev/null; then
        local compose_ver
        compose_ver=$(docker-compose --version | grep -oP '\d+\.\d+\.\d+')
        echo -e "  Docker Compose:  ${GREEN}✓${NC} v${compose_ver} (legacy)"
    else
        echo -e "  Docker Compose:  ${RED}✗ 未安装${NC}"
        checks_passed=false
    fi

    # 检查 Docker 服务
    if docker info &> /dev/null; then
        echo -e "  Docker 服务:     ${GREEN}✓${NC} 运行中"
    else
        echo -e "  Docker 服务:     ${RED}✗ 未运行${NC}"
        checks_passed=false
    fi

    # 检查 NVIDIA Container Toolkit
    if command -v nvidia-smi &> /dev/null; then
        local gpu_name
        gpu_name=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | head -1)
        local driver_ver
        driver_ver=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -1)
        echo -e "  NVIDIA 驱动:     ${GREEN}✓${NC} v${driver_ver}"
        echo -e "  GPU 型号:        ${GREEN}✓${NC} ${gpu_name}"
    else
        echo -e "  NVIDIA 驱动:     ${RED}✗ 未检测到${NC}"
        checks_passed=false
    fi

    # 检查 NVIDIA Container Runtime
    if docker info 2>/dev/null | grep -q "nvidia"; then
        echo -e "  NVIDIA Runtime:  ${GREEN}✓${NC} 已集成"
    else
        echo -e "  NVIDIA Runtime:  ${YELLOW}⚠ 未检测到${NC} (可能影响GPU加速)"
    fi

    # 检查 curl
    if command -v curl &> /dev/null; then
        echo -e "  curl:            ${GREEN}✓${NC} $(curl --version | head -1 | awk '{print $2}')"
    else
        echo -e "  curl:            ${YELLOW}⚠ 未安装${NC} (健康检查将受限)"
    fi

    # 检查磁盘空间
    local data_mount
    data_mount=$(df -BG "${DATA_DIR%/*}" 2>/dev/null | tail -1 | awk '{print $4}' | tr -d 'G')
    if [ -n "$data_mount" ] && [ "$data_mount" -gt 50 ]; then
        echo -e "  磁盘空间:        ${GREEN}✓${NC} ${data_mount}G 可用 (${DATA_DIR%/*})"
    elif [ -n "$data_mount" ]; then
        echo -e "  磁盘空间:        ${YELLOW}⚠${NC} ${data_mount}G 可用 (建议>50G)"
    fi

    echo ""

    if [ "$checks_passed" = false ]; then
        log_error "依赖检查未通过，请先安装缺失组件"
        exit 1
    fi

    log_success "系统依赖检查全部通过"
}

# 获取 docker compose 命令
get_compose_cmd() {
    if docker compose version &> /dev/null; then
        echo "docker compose"
    else
        echo "docker-compose"
    fi
}

COMPOSE_CMD=$(get_compose_cmd)

#-------------------------------------------------------------------------------
# 核心功能
#-------------------------------------------------------------------------------

# 初始化环境
do_init() {
    log_info "初始化部署环境..."

    # 1. 创建数据目录
    log_step "创建数据目录..."
    sudo mkdir -p "${DATA_DIR}"
    sudo mkdir -p "${DATA_DIR}/models"
    mkdir -p "${BACKUP_DIR}"
    mkdir -p "${LOG_DIR}"
    log_success "数据目录创建完成: ${DATA_DIR}"

    # 2. 设置目录权限
    log_step "设置目录权限..."
    sudo chmod -R 755 "${DATA_DIR}"
    chmod -R 755 "${BACKUP_DIR}"
    chmod -R 755 "${LOG_DIR}"

    # 3. 生成 docker-compose.yaml（如果不存在）
    if [ ! -f "${DOCKER_COMPOSE_FILE}" ]; then
        log_step "生成 docker-compose.yaml..."
        cat > "${DOCKER_COMPOSE_FILE}" << 'COMPOSE_EOF'
services:
  ollama:
    image: ollama/ollama:latest
    container_name: ollama
    networks:
      - ai-network
    ports:
      - "11434:11434"
    environment:
      - TZ=Asia/Shanghai
      - OLLAMA_HOST=0.0.0.0
      - NVIDIA_VISIBLE_DEVICES=all
      - OLLAMA_FLASH_ATTENTION=1
      - OLLAMA_NUM_PARALLEL=8
      - OLLAMA_MAX_LOADED_MODELS=4
      - OLLAMA_KEEP_ALIVE=30m
      - OLLAMA_CONTEXT_LENGTH=131072
      - OLLAMA_KV_CACHE_TYPE=q8_0
      - OLLAMA_DEBUG=INFO
    volumes:
      - /opt/ai/ollama/ollama_data:/root/.ollama
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
          cpus: '4.0'
          memory: 8G
        limits:
          cpus: '10.0'
          memory: 120G
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - CHOWN
      - SETUID
      - SETGID
    healthcheck:
      test: ["CMD-SHELL", "curl -sf http://localhost:11434/ || exit 1"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 120s
    logging:
      driver: json-file
      options:
        max-size: "50m"
        max-file: "5"
    restart: unless-stopped

networks:
  ai-network:
    driver: bridge
COMPOSE_EOF
        log_success "docker-compose.yaml 已生成"
    else
        log_info "docker-compose.yaml 已存在，跳过生成"
    fi

    # 4. 生成 .env 文件（如果不存在）
    if [ ! -f "${PROJECT_DIR}/.env" ]; then
        log_step "生成 .env 配置文件..."
        cat > "${PROJECT_DIR}/.env" << 'ENV_EOF'
#===============================================================================
# Ollama 服务环境配置
# 修改后需运行: ./deploy.sh restart
#===============================================================================

# 基础配置
OLLAMA_HOST=0.0.0.0
OLLAMA_PORT=11434

# GPU 与性能
OLLAMA_FLASH_ATTENTION=1
OLLAMA_NUM_PARALLEL=8
OLLAMA_MAX_LOADED_MODELS=4
OLLAMA_KEEP_ALIVE=30m
OLLAMA_CONTEXT_LENGTH=131072
OLLAMA_KV_CACHE_TYPE=q8_0

# 日志级别: DEBUG | INFO | WARN | ERROR
OLLAMA_DEBUG=INFO

# 数据目录 (容器外路径)
OLLAMA_DATA_DIR=/opt/ai/ollama/ollama_data
ENV_EOF
        log_success ".env 配置文件已生成"
    fi

    # 5. 预拉取镜像
    log_step "预拉取 Ollama 镜像..."
    docker pull ollama/ollama:latest

    echo ""
    print_separator
    log_success "环境初始化完成！"
    echo ""
    echo -e "  后续步骤:"
    echo -e "    1. 编辑配置:  ${CYAN}vim ${PROJECT_DIR}/.env${NC}"
    echo -e "    2. 启动服务:  ${CYAN}./deploy.sh start${NC}"
    echo -e "    3. 拉取模型:  ${CYAN}./deploy.sh pull qwen2.5:72b-instruct-q4_K_M${NC}"
    echo ""
}

# 启动服务
do_start() {
    local detach="${1:--d}"

    log_info "启动 Ollama 服务..."

    cd "${PROJECT_DIR}"

    # 检查是否需要初始化
    if [ ! -f "${DOCKER_COMPOSE_FILE}" ]; then
        log_warn "docker-compose.yaml 不存在，先执行初始化..."
        do_init
    fi

    # 检查数据目录
    if [ ! -d "${DATA_DIR}" ]; then
        log_warn "数据目录不存在，自动创建..."
        sudo mkdir -p "${DATA_DIR}"
        sudo chmod 755 "${DATA_DIR}"
    fi

    # 启动
    $COMPOSE_CMD up $detach

    if [ "$detach" = "-d" ]; then
        # 等待服务就绪
        if wait_for_api 120; then
            echo ""
            print_separator
            log_success "Ollama 服务启动成功！"
            echo ""
            echo -e "  ${BOLD}服务端点:${NC}"
            echo -e "    API 地址:     ${CYAN}${OLLAMA_API}${NC}"
            echo -e "    模型列表:     ${CYAN}${OLLAMA_API}/api/tags${NC}"
            echo -e "    生成接口:     ${CYAN}${OLLAMA_API}/api/generate${NC}"
            echo -e "    对话接口:     ${CYAN}${OLLAMA_API}/api/chat${NC}"
            echo ""
            echo -e "  ${BOLD}常用操作:${NC}"
            echo -e "    查看日志:     ${CYAN}./deploy.sh logs${NC}"
            echo -e "    拉取模型:     ${CYAN}./deploy.sh pull <model_name>${NC}"
            echo -e "    运行模型:     ${CYAN}./deploy.sh run <model_name>${NC}"
            echo -e "    健康检查:     ${CYAN}./deploy.sh health${NC}"
            echo ""

            # 显示已加载的模型
            local model_count
            model_count=$(curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('models',[])))" 2>/dev/null || echo "0")
            if [ "$model_count" -gt 0 ]; then
                log_info "已有 ${model_count} 个模型可用"
            else
                log_warn "尚未下载任何模型，请运行: ./deploy.sh pull <model_name>"
            fi
        else
            log_error "服务启动可能存在问题，请查看日志: ./deploy.sh logs"
            exit 1
        fi
    fi
}

# 停止服务
do_stop() {
    log_info "停止 Ollama 服务..."

    cd "${PROJECT_DIR}"

    # 显示当前运行的模型
    if is_api_ready; then
        local running
        running=$(curl -sf "${OLLAMA_API}/api/ps" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
for m in models:
    print(f\"  - {m['name']}\")
" 2>/dev/null || true)
        if [ -n "$running" ]; then
            log_warn "以下模型正在运行中，将被卸载:"
            echo "$running"
        fi
    fi

    $COMPOSE_CMD down

    log_success "Ollama 服务已停止"
}

# 重启服务
do_restart() {
    log_info "重启 Ollama 服务..."

    cd "${PROJECT_DIR}"

    $COMPOSE_CMD restart

    # 等待 API 就绪
    wait_for_api 120

    log_success "Ollama 服务重启完成"
}

# 查看服务状态
do_status() {
    log_info "服务运行状态"
    echo ""

    cd "${PROJECT_DIR}"

    # Docker 容器状态
    echo -e "  ${BOLD}容器状态:${NC}"
    $COMPOSE_CMD ps
    echo ""

    # 容器资源使用
    local container_id
    container_id=$(docker ps -q --filter "name=${PROJECT_NAME}" 2>/dev/null)
    if [ -n "$container_id" ]; then
        echo -e "  ${BOLD}资源使用:${NC}"
        docker stats --no-stream --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.NetIO}}\t{{.BlockIO}}" "$container_id"
        echo ""
    fi

    # Ollama 服务信息
    if is_api_ready; then
        echo -e "  ${BOLD}Ollama 版本:${NC}"
        local version
        version=$(curl -sf "${OLLAMA_API}/api/version" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','未知'))" 2>/dev/null || echo "未知")
        echo -e "    版本: ${version}"
        echo ""

        # 已下载模型
        echo -e "  ${BOLD}已下载模型:${NC}"
        curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
if not models:
    print('    (无)')
else:
    for m in models:
        size_gb = m.get('size', 0) / (1024**3)
        modified = m.get('modified_at', 'N/A')[:19]
        print(f\"    {m['name']:<45s} {size_gb:>6.1f} GiB   {modified}\")
    print(f\"\n    共 {len(models)} 个模型\")
" 2>/dev/null || echo "    (无法获取)"
        echo ""

        # 当前运行的模型
        echo -e "  ${BOLD}运行中模型:${NC}"
        curl -sf "${OLLAMA_API}/api/ps" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
if not models:
    print('    (无)')
else:
    for m in models:
        vram_gb = m.get('size_vram', 0) / (1024**3)
        expires = m.get('expires_at', 'N/A')[:19]
        print(f\"    {m['name']:<45s} VRAM: {vram_gb:>6.1f} GiB   过期: {expires}\")
" 2>/dev/null || echo "    (无法获取)"
        echo ""

    else
        log_warn "Ollama API 不可达，服务可能未运行"
    fi

    # GPU 状态简览
    if command -v nvidia-smi &> /dev/null; then
        echo -e "  ${BOLD}GPU 概览:${NC}"
        nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu,temperature.gpu \
            --format=csv,noheader 2>/dev/null | while IFS=',' read -r name mem_used mem_total gpu_util temp; do
            echo -e "    ${name} | 显存: ${mem_used}/${mem_total} | GPU: ${gpu_util} | 温度: ${temp}"
        done
        echo ""
    fi

    # 磁盘使用
    echo -e "  ${BOLD}磁盘使用:${NC}"
    if [ -d "${DATA_DIR}" ]; then
        local data_size
        data_size=$(du -sh "${DATA_DIR}" 2>/dev/null | awk '{print $1}')
        local disk_avail
        disk_avail=$(df -h "${DATA_DIR}" 2>/dev/null | tail -1 | awk '{print $4}')
        echo -e "    模型数据: ${data_size} (可用: ${disk_avail})"
    else
        echo -e "    数据目录不存在: ${DATA_DIR}"
    fi
    echo ""
}

# 查看日志
do_logs() {
    local service="${1:-}"
    local lines="${2:-200}"

    cd "${PROJECT_DIR}"

    if [ -n "$service" ] && [ "$service" != "ollama" ]; then
        log_warn "当前仅有 ollama 服务，忽略参数: ${service}"
    fi

    log_info "查看日志 (最近 ${lines} 行, Ctrl+C 退出)..."
    $COMPOSE_CMD logs -f --tail="$lines"
}

# 更新镜像并重启
do_update() {
    log_info "更新 Ollama..."

    cd "${PROJECT_DIR}"

    # 获取当前版本
    local current_ver="未知"
    if is_api_ready; then
        current_ver=$(curl -sf "${OLLAMA_API}/api/version" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','未知'))" 2>/dev/null || echo "未知")
    fi
    log_info "当前版本: ${current_ver}"

    # 拉取最新镜像
    log_step "拉取最新 Ollama 镜像..."
    docker pull ollama/ollama:latest

    # 获取新镜像ID
    local new_image_id
    new_image_id=$(docker inspect --format='{{.Id}}' ollama/ollama:latest 2>/dev/null | cut -c8-19)
    log_info "新镜像ID: ${new_image_id}"

    # 重建并启动
    log_step "重建容器..."
    $COMPOSE_CMD up -d --force-recreate

    # 等待就绪
    if wait_for_api 120; then
        local new_ver
        new_ver=$(curl -sf "${OLLAMA_API}/api/version" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','未知'))" 2>/dev/null || echo "未知")
        log_success "更新完成: ${current_ver} → ${new_ver}"
    else
        log_error "更新后服务启动异常"
        exit 1
    fi

    # 清理旧镜像
    log_step "清理旧镜像..."
    docker image prune -f --filter "label=maintainer" > /dev/null 2>&1 || true
}

# 清理
do_clean() {
    local mode="${1:-}"

    cd "${PROJECT_DIR}"

    echo -e "${YELLOW}"
    echo "┌─────────────────────────────────────────────┐"
    echo "│  ⚠ 清理操作                                 │"
    echo "│                                              │"
    echo "│  --soft   仅停止容器 (保留镜像和数据)         │"
    echo "│  --hard   停止容器 + 删除镜像                 │"
    echo "│  --purge  停止容器 + 删除镜像 + 删除所有模型   │"
    echo "└─────────────────────────────────────────────┘"
    echo -e "${NC}"

    case "$mode" in
        --soft)
            log_info "执行软清理..."
            $COMPOSE_CMD down --remove-orphans
            log_success "容器已清理 (镜像和数据保留)"
            ;;
        --hard)
            log_warn "将停止容器并删除 Ollama 镜像"
            read -rp "  确认? [y/N]: " confirm
            if [[ "$confirm" =~ ^[yY]$ ]]; then
                $COMPOSE_CMD down --remove-orphans --rmi all
                docker image prune -f
                log_success "容器和镜像已清理 (数据保留)"
            else
                log_info "取消操作"
            fi
            ;;
        --purge)
            log_error "⚠ 此操作将删除所有已下载的模型！"
            log_warn "数据目录: ${DATA_DIR}"
            if [ -d "${DATA_DIR}" ]; then
                local data_size
                data_size=$(du -sh "${DATA_DIR}" 2>/dev/null | awk '{print $1}')
                log_warn "数据大小: ${data_size}"
            fi
            echo ""
            read -rp "  输入 'DELETE ALL' 确认: " confirm
            if [ "$confirm" = "DELETE ALL" ]; then
                $COMPOSE_CMD down --remove-orphans --rmi all -v
                sudo rm -rf "${DATA_DIR}"
                docker image prune -f
                log_success "所有数据已彻底清理"
            else
                log_info "取消操作"
            fi
            ;;
        *)
            log_info "请指定清理模式: --soft | --hard | --purge"
            echo ""
            echo "示例:"
            echo "  ./deploy.sh clean --soft     # 仅停止容器"
            echo "  ./deploy.sh clean --hard     # 停止 + 删除镜像"
            echo "  ./deploy.sh clean --purge    # 删除一切(含模型)"
            ;;
    esac
}

# 备份
do_backup() {
    local backup_name="${1:-ollama_backup_$(date +%Y%m%d_%H%M%S)}"
    local backup_path="${BACKUP_DIR}/${backup_name}"

    log_info "备份 Ollama 数据..."

    mkdir -p "${backup_path}"

    # 1. 备份模型清单
    log_step "导出模型清单..."
    if is_api_ready; then
        curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
with open('${backup_path}/model_list.txt', 'w') as f:
    for m in models:
        f.write(m['name'] + '\n')
print(f'  已记录 {len(models)} 个模型')
" 2>/dev/null || log_warn "无法导出模型清单 (API不可达)"
    fi

    # 2. 备份 Modelfile（自定义模型配置）
    log_step "备份 Modelfile 配置..."
    if is_api_ready; then
        mkdir -p "${backup_path}/modelfiles"
        curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json, subprocess
data = json.load(sys.stdin)
models = data.get('models', [])
for m in models:
    name = m['name']
    safe_name = name.replace(':', '_').replace('/', '_')
    result = subprocess.run(
        ['curl', '-sf', '${OLLAMA_API}/api/show', '-d', json.dumps({'name': name})],
        capture_output=True, text=True
    )
    if result.returncode == 0:
        with open(f'${backup_path}/modelfiles/{safe_name}.json', 'w') as f:
            f.write(result.stdout)
" 2>/dev/null || log_warn "Modelfile 备份跳过"
    fi

    # 3. 备份 docker-compose 和配置文件
    log_step "备份配置文件..."
    cp -f "${DOCKER_COMPOSE_FILE}" "${backup_path}/" 2>/dev/null || true
    cp -f "${PROJECT_DIR}/.env" "${backup_path}/" 2>/dev/null || true
    cp -f "${PROJECT_DIR}/deploy.sh" "${backup_path}/" 2>/dev/null || true

    # 4. 备份模型数据（可选，体积巨大）
    echo ""
    log_warn "是否备份模型二进制文件? (可能非常大)"
    if [ -d "${DATA_DIR}" ]; then
        local data_size
        data_size=$(du -sh "${DATA_DIR}" 2>/dev/null | awk '{print $1}')
        echo -e "  数据大小: ${YELLOW}${data_size}${NC}"
    fi
    read -rp "  备份模型文件? [y/N]: " backup_models

    if [[ "$backup_models" =~ ^[yY]$ ]]; then
        log_step "备份模型文件 (这可能需要很长时间)..."
        rsync -ah --progress "${DATA_DIR}/" "${backup_path}/ollama_data/" 2>/dev/null || \
            cp -r "${DATA_DIR}" "${backup_path}/ollama_data"
        log_success "模型文件备份完成"
    else
        log_info "跳过模型文件备份 (可通过模型清单重新下载)"
    fi

    # 5. 压缩
    log_step "压缩备份..."
    cd "${BACKUP_DIR}"
    tar -czf "${backup_name}.tar.gz" "${backup_name}"
    local archive_size
    archive_size=$(du -sh "${backup_name}.tar.gz" | awk '{print $1}')
    rm -rf "${backup_name}"

    echo ""
    print_separator
    log_success "备份完成！"
    echo -e "  文件: ${CYAN}${BACKUP_DIR}/${backup_name}.tar.gz${NC}"
    echo -e "  大小: ${archive_size}"
    echo ""
}

# 恢复
do_restore() {
    local backup_file="${1:-}"

    # 无参数时列出可用备份
    if [ -z "$backup_file" ]; then
        log_info "可用的备份文件:"
        echo ""
        if ls "${BACKUP_DIR}"/*.tar.gz &> /dev/null; then
            ls -lh "${BACKUP_DIR}"/*.tar.gz | awk '{printf "  %-50s %s\n", $NF, $5}'
        else
            echo "  (无备份文件)"
        fi
        echo ""
        echo "用法: ./deploy.sh restore <backup_file>"
        exit 0
    fi

    # 查找备份文件
    if [ ! -f "$backup_file" ]; then
        if [ -f "${BACKUP_DIR}/${backup_file}" ]; then
            backup_file="${BACKUP_DIR}/${backup_file}"
        else
            log_error "备份文件不存在: ${backup_file}"
            exit 1
        fi
    fi

    log_warn "恢复将覆盖当前配置，是否继续?"
    read -rp "  确认? [y/N]: " confirm
    if [[ ! "$confirm" =~ ^[yY]$ ]]; then
        log_info "取消恢复"
        exit 0
    fi

    log_info "恢复数据: ${backup_file}"

    # 解压
    local temp_dir
    temp_dir=$(mktemp -d)
    tar -xzf "$backup_file" -C "$temp_dir"
    local backup_name
    backup_name=$(ls "$temp_dir")
    local restore_path="${temp_dir}/${backup_name}"

    # 恢复配置文件
    if [ -f "${restore_path}/docker-compose.yaml" ]; then
        log_step "恢复 docker-compose.yaml..."
        cp -f "${restore_path}/docker-compose.yaml" "${DOCKER_COMPOSE_FILE}"
    fi

    if [ -f "${restore_path}/.env" ]; then
        log_step "恢复 .env..."
        cp -f "${restore_path}/.env" "${PROJECT_DIR}/.env"
    fi

    # 恢复模型数据
    if [ -d "${restore_path}/ollama_data" ]; then
        log_step "恢复模型数据..."
        sudo mkdir -p "${DATA_DIR}"
        sudo rsync -ah --progress "${restore_path}/ollama_data/" "${DATA_DIR}/" 2>/dev/null || \
            sudo cp -r "${restore_path}/ollama_data/"* "${DATA_DIR}/"
        log_success "模型数据恢复完成"
    elif [ -f "${restore_path}/model_list.txt" ]; then
        # 如果没有模型数据但有清单，提示重新下载
        log_warn "备份中不含模型文件，以下模型需要重新下载:"
        cat "${restore_path}/model_list.txt" | while read -r model; do
            echo "  - ${model}"
        done
        echo ""
        read -rp "  现在下载? [y/N]: " dl_confirm
        if [[ "$dl_confirm" =~ ^[yY]$ ]]; then
            # 先启动服务
            do_start "-d"
            cat "${restore_path}/model_list.txt" | while read -r model; do
                log_step "下载模型: ${model}"
                docker exec ollama ollama pull "$model"
            done
        fi
    fi

    # 清理
    rm -rf "$temp_dir"

    log_success "恢复完成！"
    log_info "如需重启服务: ./deploy.sh restart"
}

# 拉取模型
do_pull() {
    local model="${1:-}"

    if [ -z "$model" ]; then
        echo -e "  ${BOLD}用法:${NC} ./deploy.sh pull <model_name>"
        echo ""
        echo -e "  ${BOLD}推荐模型 (适合120GB VRAM):${NC}"
        echo ""
        echo "  ┌─────────────────────────────────────┬────────┬───────────────┐"
        echo "  │ 模型名称                             │ 大小   │ 适用场景       │"
        echo "  ├─────────────────────────────────────┼────────┼───────────────┤"
        echo "  │ qwen2.5:72b-instruct-q4_K_M         │ ~42GB  │ 中文通用       │"
        echo "  │ qwen2.5-coder:32b-instruct-q8_0     │ ~34GB  │ 代码生成       │"
        echo "  │ llama3.1:70b-instruct-q4_K_M        │ ~40GB  │ 英文通用       │"
        echo "  │ deepseek-r1:70b-q4_K_M              │ ~43GB  │ 推理/数学      │"
        echo "  │ deepseek-coder-v2:236b-q2_K          │ ~86GB  │ 代码(极限)     │"
        echo "  │ command-r-plus:104b-q4_K_M           │ ~60GB  │ RAG/工具调用   │"
        echo "  │ mixtral:8x22b-instruct-q4_K_M       │ ~80GB  │ MoE混合专家    │"
        echo "  │ nomic-embed-text                     │ ~0.3GB │ 文本嵌入       │"
        echo "  └─────────────────────────────────────┴────────┴───────────────┘"
        echo ""
        echo "  示例:"
        echo "    ./deploy.sh pull qwen2.5:72b-instruct-q4_K_M"
        echo "    ./deploy.sh pull deepseek-r1:70b"
        return 0
    fi

    # 确保服务运行
    if ! is_api_ready; then
        log_error "Ollama 服务未运行，请先启动: ./deploy.sh start"
        exit 1
    fi

    log_info "拉取模型: ${model}"
    echo ""

    # 使用 docker exec 拉取（有进度显示）
    docker exec -it ollama ollama pull "$model"

    echo ""
    log_success "模型 ${model} 拉取完成"

    # 显示模型信息
    echo ""
    docker exec ollama ollama show "$model" --modelfile 2>/dev/null | head -5 || true
}

# 删除模型
do_rm() {
    local model="${1:-}"
    local force=false

    # 解析参数
    if [ "$model" = "-f" ] || [ "$model" = "--force" ]; then
        force=true
        model="${2:-}"
    elif [ "${2:-}" = "-f" ] || [ "${2:-}" = "--force" ]; then
        force=true
    fi

    if [ -z "$model" ]; then
        echo -e "  ${BOLD}用法:${NC} ./deploy.sh rm <model_name> [-f]"
        echo ""
        echo -e "  ${BOLD}选项:${NC}"
        echo "    -f, --force   跳过确认直接删除"
        echo ""

        # 列出已有模型
        if is_api_ready; then
            echo -e "  ${BOLD}已下载模型:${NC}"
            echo ""
            curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
if not models:
    print('    (无)')
else:
    for m in models:
        size_gb = m.get('size', 0) / (1024**3)
        print(f\"    {m['name']:<45s} {size_gb:>6.1f} GiB\")
    total_gb = sum(m.get('size', 0) for m in models) / (1024**3)
    print(f\"\n    共 {len(models)} 个模型，总计 {total_gb:.1f} GiB\")
" 2>/dev/null || echo "    (无法获取模型列表)"
            echo ""
        fi

        echo "  示例:"
        echo "    ./deploy.sh rm qwen2.5:72b"
        echo "    ./deploy.sh rm nomic-embed-text -f"
        return 0
    fi

    # 确保服务运行
    if ! is_api_ready; then
        log_error "Ollama 服务未运行，请先启动: ./deploy.sh start"
        exit 1
    fi

    # 检查模型是否存在
    local model_exists
    model_exists=$(curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
names = [m['name'] for m in data.get('models', [])]
target = '${model}'
# 精确匹配或前缀匹配（如 qwen2.5 匹配 qwen2.5:latest）
found = [n for n in names if n == target or n.startswith(target + ':') or target == n.split(':')[0]]
for f in found:
    print(f)
" 2>/dev/null)

    if [ -z "$model_exists" ]; then
        log_error "模型 '${model}' 不存在"
        echo ""
        echo "  已有模型:"
        docker exec ollama ollama list 2>/dev/null | tail -n +2 | awk '{print "    " $1}' || true
        return 1
    fi

    # 检查模型是否正在运行
    local is_running
    is_running=$(curl -sf "${OLLAMA_API}/api/ps" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
running = [m['name'] for m in data.get('models', [])]
target = '${model}'
for r in running:
    if r == target or r.startswith(target + ':') or target == r.split(':')[0]:
        print(r)
" 2>/dev/null)

    if [ -n "$is_running" ]; then
        log_warn "模型 ${is_running} 正在运行中，删除后将卸载"
    fi

    # 获取模型大小
    local model_size
    model_size=$(curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
for m in data.get('models', []):
    if m['name'] == '${model}' or m['name'].startswith('${model}:') or '${model}' == m['name'].split(':')[0]:
        print(f\"{m.get('size', 0) / (1024**3):.1f} GiB\")
        break
" 2>/dev/null)

    # 确认删除
    if [ "$force" != true ]; then
        echo ""
        echo -e "  ${YELLOW}将删除模型: ${model_exists}${NC}"
        [ -n "$model_size" ] && echo -e "  大小: ${model_size}"
        echo ""
        read -rp "  确认删除? [y/N]: " confirm
        if [[ ! "$confirm" =~ ^[yY]$ ]]; then
            log_info "取消删除"
            return 0
        fi
    fi

    # 执行删除
    log_info "删除模型: ${model}"
    echo ""

    docker exec ollama ollama rm "$model"

    echo ""
    log_success "模型 ${model} 已删除"

    # 显示剩余模型数和空间
    local remaining
    remaining=$(curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
total_gb = sum(m.get('size', 0) for m in models) / (1024**3)
print(f'剩余 {len(models)} 个模型，共 {total_gb:.1f} GiB')
" 2>/dev/null)
    [ -n "$remaining" ] && log_info "$remaining"
}

# 列出模型
do_models() {
    if ! is_api_ready; then
        log_error "Ollama 服务未运行"
        exit 1
    fi

    log_info "已下载模型列表:"
    echo ""

    docker exec ollama ollama list

    echo ""

    # 显示运行中的模型
    log_info "运行中模型:"
    echo ""
    docker exec ollama ollama ps

    echo ""

    # 统计VRAM使用
    local total_running_vram
    total_running_vram=$(curl -sf "${OLLAMA_API}/api/ps" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
total = sum(m.get('size_vram', 0) for m in models)
print(f'{total / (1024**3):.1f}')
" 2>/dev/null || echo "0")

    echo -e "  ${BOLD}VRAM 使用: ${total_running_vram} GiB (总可用: ~120 GiB)${NC}"
    echo ""
}

# 交互式运行模型
do_run() {
    local model="${1:-}"

    if [ -z "$model" ]; then
        log_error "请指定模型名称"
        echo ""
        echo "用法: ./deploy.sh run <model_name>"
        echo ""
        if is_api_ready; then
            log_info "可用模型:"
            docker exec ollama ollama list 2>/dev/null
        fi
        exit 1
    fi

    if ! is_api_ready; then
        log_error "Ollama 服务未运行，请先启动: ./deploy.sh start"
        exit 1
    fi

    log_info "启动交互式会话: ${model} (输入 /bye 退出)"
    echo ""

    docker exec -it ollama ollama run "$model"
}

# 性能基准测试
do_bench() {
    local model="${1:-}"

    if [ -z "$model" ]; then
        log_error "请指定测试模型"
        echo "用法: ./deploy.sh bench <model_name>"
        exit 1
    fi

    if ! is_api_ready; then
        log_error "Ollama 服务未运行"
        exit 1
    fi

    log_info "运行基准测试: ${model}"
    echo ""
    print_separator

    # 测试1: 冷启动 + 短文本生成
    echo -e "\n${BOLD}测试 1/3: 冷启动 + 短文本生成${NC}"
    local start_time
    start_time=$(date +%s%N)

    local response
    response=$(curl -sf "${OLLAMA_API}/api/generate" \
        -d "{\"model\":\"${model}\",\"prompt\":\"Hello\",\"stream\":false}" 2>/dev/null)

    local end_time
    end_time=$(date +%s%N)
    local duration_ms=$(( (end_time - start_time) / 1000000 ))

    if [ -n "$response" ]; then
        local eval_count prompt_eval_count eval_duration prompt_eval_duration
        eval_count=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('eval_count',0))" 2>/dev/null || echo "0")
        prompt_eval_count=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('prompt_eval_count',0))" 2>/dev/null || echo "0")
        eval_duration=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('eval_duration',0))" 2>/dev/null || echo "0")
        prompt_eval_duration=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('prompt_eval_duration',0))" 2>/dev/null || echo "0")

        local tokens_per_sec="N/A"
        if [ "$eval_duration" -gt 0 ]; then
            tokens_per_sec=$(awk "BEGIN {printf \"%.1f\", $eval_count / ($eval_duration / 1000000000)}")
        fi

        local prompt_tokens_per_sec="N/A"
        if [ "$prompt_eval_duration" -gt 0 ]; then
            prompt_tokens_per_sec=$(awk "BEGIN {printf \"%.1f\", $prompt_eval_count / ($prompt_eval_duration / 1000000000)}")
        fi

        echo "  总耗时:       ${duration_ms}ms"
        echo "  Prompt处理:   ${prompt_eval_count} tokens @ ${prompt_tokens_per_sec} t/s"
        echo "  文本生成:     ${eval_count} tokens @ ${tokens_per_sec} t/s"
    else
        echo -e "  ${RED}请求失败${NC}"
    fi

    # 测试2: 热启动 + 长文本生成
    echo -e "\n${BOLD}测试 2/3: 热启动 + 长文本生成${NC}"
    start_time=$(date +%s%N)

    response=$(curl -sf "${OLLAMA_API}/api/generate" \
        -d "{\"model\":\"${model}\",\"prompt\":\"Write a detailed explanation of how neural networks work, covering at least 500 words.\",\"stream\":false}" 2>/dev/null)

    end_time=$(date +%s%N)
    duration_ms=$(( (end_time - start_time) / 1000000 ))

    if [ -n "$response" ]; then
        eval_count=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('eval_count',0))" 2>/dev/null || echo "0")
        eval_duration=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('eval_duration',0))" 2>/dev/null || echo "0")

        tokens_per_sec="N/A"
        if [ "$eval_duration" -gt 0 ]; then
            tokens_per_sec=$(awk "BEGIN {printf \"%.1f\", $eval_count / ($eval_duration / 1000000000)}")
        fi

        echo "  总耗时:       ${duration_ms}ms"
        echo "  生成:         ${eval_count} tokens @ ${tokens_per_sec} t/s"
    else
        echo -e "  ${RED}请求失败${NC}"
    fi

    # 测试3: 并发测试
    echo -e "\n${BOLD}测试 3/3: 并发测试 (4路)${NC}"
    start_time=$(date +%s%N)

    local pids=()
    for i in $(seq 1 4); do
        curl -sf "${OLLAMA_API}/api/generate" \
            -d "{\"model\":\"${model}\",\"prompt\":\"Count from 1 to 20 in words.\",\"stream\":false}" \
            > "/tmp/ollama_bench_${i}.json" 2>/dev/null &
        pids+=($!)
    done

    # 等待所有请求完成
    local all_success=true
    for pid in "${pids[@]}"; do
        if ! wait "$pid"; then
            all_success=false
        fi
    done

    end_time=$(date +%s%N)
    duration_ms=$(( (end_time - start_time) / 1000000 ))

    local total_tokens=0
    for i in $(seq 1 4); do
        if [ -f "/tmp/ollama_bench_${i}.json" ]; then
            local t
            t=$(python3 -c "import json; print(json.load(open('/tmp/ollama_bench_${i}.json')).get('eval_count',0))" 2>/dev/null || echo "0")
            total_tokens=$((total_tokens + t))
            rm -f "/tmp/ollama_bench_${i}.json"
        fi
    done

    local overall_tps="N/A"
    if [ "$duration_ms" -gt 0 ]; then
        overall_tps=$(awk "BEGIN {printf \"%.1f\", $total_tokens / ($duration_ms / 1000)}")
    fi

    echo "  总耗时:       ${duration_ms}ms"
    echo "  总token数:    ${total_tokens}"
    echo "  总吞吐量:     ${overall_tps} t/s (4路并发)"

    echo ""
    print_separator
    log_success "基准测试完成"
    echo ""
}

# GPU 详细信息
do_gpu() {
    log_info "GPU 详细信息"
    echo ""

    if ! command -v nvidia-smi &> /dev/null; then
        log_error "nvidia-smi 不可用"
        exit 1
    fi

    # GPU 基本信息
    echo -e "  ${BOLD}硬件信息:${NC}"
    nvidia-smi --query-gpu=index,name,driver_version,pci.bus_id,compute_cap \
        --format=csv,noheader 2>/dev/null | while IFS=',' read -r idx name driver pci compute; do
        echo "    GPU ${idx}:${name}"
        echo "    驱动: ${driver} | PCI: ${pci} | 算力: ${compute}"
    done
    echo ""

    # 显存信息
    echo -e "  ${BOLD}显存状态:${NC}"
    nvidia-smi --query-gpu=memory.total,memory.used,memory.free \
        --format=csv,noheader 2>/dev/null | while IFS=',' read -r total used free; do
        echo "    总量: ${total} | 已用: ${used} | 空闲: ${free}"
    done
    echo ""

    # 实时状态
    echo -e "  ${BOLD}实时状态:${NC}"
    nvidia-smi --query-gpu=utilization.gpu,utilization.memory,temperature.gpu,power.draw,power.limit,fan.speed,clocks.gr,clocks.mem \
        --format=csv,noheader 2>/dev/null | while IFS=',' read -r gpu_util mem_util temp power power_limit fan clk_gpu clk_mem; do
        echo "    GPU利用率: ${gpu_util} | 显存带宽: ${mem_util}"
        echo "    温度: ${temp} | 功耗: ${power} / ${power_limit}"
        echo "    风扇: ${fan} | 核心频率: ${clk_gpu} | 显存频率: ${clk_mem}"
    done
    echo ""

    # 容器内 GPU 视图
    local container_id
    container_id=$(docker ps -q --filter "name=ollama" 2>/dev/null)
    if [ -n "$container_id" ]; then
        echo -e "  ${BOLD}容器内 GPU 视图:${NC}"
        docker exec ollama nvidia-smi 2>/dev/null || echo "    (容器内 nvidia-smi 不可用)"
        echo ""
    fi

    # Ollama GPU 识别日志
    if [ -n "$container_id" ]; then
        echo -e "  ${BOLD}Ollama GPU 识别:${NC}"
        docker logs ollama 2>&1 | grep -E "inference compute|GPU|CUDA|VRAM" | tail -5 | while read -r line; do
            echo "    ${line}"
        done
        echo ""
    fi
}

# 进入容器
do_exec() {
    local cmd="${1:-/bin/bash}"

    # 如果第一个参数是 ollama，跳过
    if [ "$cmd" = "ollama" ]; then
        cmd="${2:-/bin/bash}"
    fi

    log_info "进入 Ollama 容器 (${cmd})..."
    docker exec -it ollama $cmd
}

# 健康检查
do_health() {
    log_info "执行健康检查..."
    echo ""

    local all_healthy=true
    local total_checks=0
    local passed_checks=0

    # 1. Docker 容器状态
    total_checks=$((total_checks + 1))
    local container_status
    container_status=$(docker inspect --format='{{.State.Status}}' ollama 2>/dev/null || echo "not_found")
    if [ "$container_status" = "running" ]; then
        local uptime
        uptime=$(docker inspect --format='{{.State.StartedAt}}' ollama 2>/dev/null | xargs -I{} date -d {} "+%Y-%m-%d %H:%M:%S" 2>/dev/null || echo "N/A")
        echo -e "  ✅ Docker 容器        运行中 (启动于: ${uptime})"
        passed_checks=$((passed_checks + 1))
    else
        echo -e "  ❌ Docker 容器        状态: ${container_status}"
        all_healthy=false
    fi

    # 2. Ollama API
    total_checks=$((total_checks + 1))
    local api_start api_end api_latency
    api_start=$(date +%s%N)
    if is_api_ready; then
        api_end=$(date +%s%N)
        api_latency=$(( (api_end - api_start) / 1000000 ))
        echo -e "  ✅ Ollama API         可达 (延迟: ${api_latency}ms)"
        passed_checks=$((passed_checks + 1))
    else
        echo -e "  ❌ Ollama API         不可达 (${OLLAMA_API})"
        all_healthy=false
    fi

    # 3. GPU 检测
    total_checks=$((total_checks + 1))
    local gpu_detected
    gpu_detected=$(docker logs ollama 2>&1 | grep "inference compute" | tail -1)
    if [ -n "$gpu_detected" ]; then
        local gpu_lib
        gpu_lib=$(echo "$gpu_detected" | grep -oP 'library=\K\w+')
        local gpu_vram
        gpu_vram=$(echo "$gpu_detected" | grep -oP 'total="\K[^"]+')
        echo -e "  ✅ GPU 加速           ${gpu_lib} | VRAM: ${gpu_vram}"
        passed_checks=$((passed_checks + 1))
    else
        echo -e "  ❌ GPU 加速           未检测到"
        all_healthy=false
    fi

    # 4. CUDA 状态
    total_checks=$((total_checks + 1))
    if docker exec ollama nvidia-smi &>/dev/null; then
        local cuda_ver
        cuda_ver=$(docker exec ollama nvidia-smi 2>/dev/null | grep -oP 'CUDA Version: \K[\d.]+' || echo "N/A")
        echo -e "  ✅ CUDA Runtime       版本: ${cuda_ver}"
        passed_checks=$((passed_checks + 1))
    else
        echo -e "  ❌ CUDA Runtime       容器内不可用"
        all_healthy=false
    fi

    # 5. FlashAttention
    total_checks=$((total_checks + 1))
    local flash_attn
    flash_attn=$(docker logs ollama 2>&1 | grep -oP 'OLLAMA_FLASH_ATTENTION:\K\w+' | tail -1)
    if [ "$flash_attn" = "true" ]; then
        echo -e "  ✅ FlashAttention     已启用"
        passed_checks=$((passed_checks + 1))
    else
        echo -e "  ⚠️  FlashAttention     未启用 (建议开启)"
    fi

    # 6. 模型目录
    total_checks=$((total_checks + 1))
    if [ -d "${DATA_DIR}" ]; then
        local data_size
        data_size=$(du -sh "${DATA_DIR}" 2>/dev/null | awk '{print $1}')
        local model_count
        model_count=$(curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('models',[])))" 2>/dev/null || echo "?")
        echo -e "  ✅ 模型存储           ${data_size} (${model_count} 个模型)"
        passed_checks=$((passed_checks + 1))
    else
        echo -e "  ❌ 模型存储           目录不存在: ${DATA_DIR}"
        all_healthy=false
    fi

    # 7. 磁盘空间
    total_checks=$((total_checks + 1))
    local disk_avail
    disk_avail=$(df -BG "${DATA_DIR}" 2>/dev/null | tail -1 | awk '{print $4}' | tr -d 'G')
    if [ -n "$disk_avail" ] && [ "$disk_avail" -gt 20 ]; then
        echo -e "  ✅ 磁盘空间           ${disk_avail}G 可用"
        passed_checks=$((passed_checks + 1))
    elif [ -n "$disk_avail" ]; then
        echo -e "  ⚠️  磁盘空间           ${disk_avail}G 可用 (低于20G警告线)"
    else
        echo -e "  ❌ 磁盘空间           无法检测"
        all_healthy=false
    fi

    # 8. 容器健康检查状态
    total_checks=$((total_checks + 1))
    local health_status
    health_status=$(docker inspect --format='{{.State.Health.Status}}' ollama 2>/dev/null || echo "unknown")
    case "$health_status" in
        healthy)
            echo -e "  ✅ 容器健康检查       ${health_status}"
            passed_checks=$((passed_checks + 1))
            ;;
        starting)
            echo -e "  ⏳ 容器健康检查       ${health_status} (启动中)"
            ;;
        unhealthy)
            echo -e "  ❌ 容器健康检查       ${health_status}"
            all_healthy=false
            ;;
        *)
            echo -e "  ⚠️  容器健康检查       ${health_status}"
            ;;
    esac

    # 9. 推理测试
    total_checks=$((total_checks + 1))
    if is_api_ready; then
        local test_model
        test_model=$(curl -sf "${OLLAMA_API}/api/tags" 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
models = data.get('models', [])
if models:
    # 优先选最小的模型测试
    smallest = min(models, key=lambda x: x.get('size', float('inf')))
    print(smallest['name'])
" 2>/dev/null)

        if [ -n "$test_model" ]; then
            local infer_start infer_end
            infer_start=$(date +%s%N)
            local infer_result
            infer_result=$(curl -sf "${OLLAMA_API}/api/generate" \
                -d "{\"model\":\"${test_model}\",\"prompt\":\"Hi\",\"stream\":false}" \
                --max-time 60 2>/dev/null)
            infer_end=$(date +%s%N)
            local infer_ms=$(( (infer_end - infer_start) / 1000000 ))

            if [ -n "$infer_result" ]; then
                local infer_tps
                infer_tps=$(echo "$infer_result" | python3 -c "
import sys, json
d = json.load(sys.stdin)
ec = d.get('eval_count', 0)
ed = d.get('eval_duration', 1)
print(f'{ec / (ed / 1e9):.1f}')
" 2>/dev/null || echo "N/A")
                echo -e "  ✅ 推理测试           通过 (${test_model}, ${infer_tps} t/s, ${infer_ms}ms)"
                passed_checks=$((passed_checks + 1))
            else
                echo -e "  ❌ 推理测试           超时或失败 (${test_model})"
                all_healthy=false
            fi
        else
            echo -e "  ⏭️  推理测试           跳过 (无可用模型)"
            passed_checks=$((passed_checks + 1))
        fi
    else
        echo -e "  ❌ 推理测试           API 不可达"
        all_healthy=false
    fi

    # 总结
    echo ""
    print_separator
    if [ "$all_healthy" = true ]; then
        echo -e "  ${GREEN}${BOLD}健康检查通过  ${passed_checks}/${total_checks} ✓${NC}"
    else
        echo -e "  ${RED}${BOLD}健康检查未通过  ${passed_checks}/${total_checks}${NC}"
        echo ""
        echo -e "  排查建议:"
        echo -e "    查看日志:   ${CYAN}./deploy.sh logs${NC}"
        echo -e "    重启服务:   ${CYAN}./deploy.sh restart${NC}"
        echo -e "    GPU信息:    ${CYAN}./deploy.sh gpu${NC}"
    fi
    echo ""

    [ "$all_healthy" = true ] && return 0 || return 1
}

# 硬件检测与配置优化
do_optimize() {
    local dry_run=false
    local auto_apply=false

    # 解析参数
    for arg in "$@"; do
        case "$arg" in
            --dry-run) dry_run=true ;;
            --yes|-y)  auto_apply=true ;;
        esac
    done

    log_info "检测宿主机硬件配置..."
    echo ""

    #--- 1. 检测 CPU ---
    local cpu_cores cpu_model
    if [ -f /proc/cpuinfo ]; then
        cpu_cores=$(nproc 2>/dev/null || grep -c '^processor' /proc/cpuinfo)
        cpu_model=$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | xargs)
    elif command -v sysctl &>/dev/null; then
        cpu_cores=$(sysctl -n hw.ncpu 2>/dev/null || echo "0")
        cpu_model=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "Unknown")
    else
        cpu_cores=$(nproc 2>/dev/null || echo "0")
        cpu_model="Unknown"
    fi
    echo -e "  ${BOLD}CPU:${NC}"
    echo -e "    型号:   ${cpu_model}"
    echo -e "    核心数: ${cpu_cores}"

    #--- 2. 检测内存 ---
    local total_mem_mb=0
    if [ -f /proc/meminfo ]; then
        total_mem_mb=$(awk '/MemTotal/ {printf "%.0f", $2/1024}' /proc/meminfo)
    elif command -v sysctl &>/dev/null; then
        local mem_bytes
        mem_bytes=$(sysctl -n hw.memsize 2>/dev/null || echo "0")
        total_mem_mb=$((mem_bytes / 1024 / 1024))
    fi
    local total_mem_gb=$((total_mem_mb / 1024))
    echo -e "  ${BOLD}内存:${NC}"
    echo -e "    总量:   ${total_mem_gb} GiB"

    #--- 3. 检测 GPU ---
    local gpu_count=0
    local gpu_name="N/A"
    local gpu_vram_mb=0
    local gpu_vram_gb=0
    local has_nvidia=false

    if command -v nvidia-smi &>/dev/null; then
        has_nvidia=true
        gpu_count=$(nvidia-smi --query-gpu=count --format=csv,noheader 2>/dev/null | head -1 || echo "0")
        gpu_name=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | head -1 || echo "N/A")
        gpu_vram_mb=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | head -1 | tr -dc '0-9' || echo "0")
        # 非数字兜底
        if ! [[ "$gpu_vram_mb" =~ ^[0-9]+$ ]] || [ -z "$gpu_vram_mb" ]; then
            gpu_vram_mb=0
        fi
        gpu_vram_gb=$((gpu_vram_mb / 1024))

        echo -e "  ${BOLD}GPU:${NC}"
        echo -e "    型号:   ${gpu_name}"
        echo -e "    数量:   ${gpu_count}"
        echo -e "    显存:   ${gpu_vram_gb} GiB (${gpu_vram_mb} MiB)"

        # 检测是否为统一内存架构 (Grace Hopper / DGX Spark / Jetson 等)
        # 统一内存特征：GPU VRAM ≈ 系统内存，或GPU VRAM > 80GB 且差值小
        local is_unified_memory=false
        local mem_diff=$((total_mem_gb - gpu_vram_gb))
        if [ "$mem_diff" -lt 0 ]; then
            mem_diff=$((-mem_diff))
        fi
        # 若 GPU VRAM 与系统内存差值 < 20%，判定为统一内存
        if [ "$total_mem_gb" -gt 0 ] && [ "$gpu_vram_gb" -gt 0 ]; then
            local threshold=$((total_mem_gb * 20 / 100))
            if [ "$mem_diff" -le "$threshold" ] || echo "$gpu_name" | grep -qiE "GH200|Grace|GB10|GB200|Jetson"; then
                is_unified_memory=true
            fi
        fi
        # 名称匹配兜底
        if echo "$gpu_name" | grep -qiE "GH200|Grace|GB10|GB200"; then
            is_unified_memory=true
        fi

        if [ "$is_unified_memory" = true ]; then
            echo -e "    架构:   ${CYAN}统一内存 (Unified Memory)${NC}"
        else
            echo -e "    架构:   独立显存 (Discrete GPU)"
        fi
    else
        echo -e "  ${BOLD}GPU:${NC}"
        echo -e "    ${YELLOW}未检测到 NVIDIA GPU${NC}"
    fi

    echo ""
    print_separator
    echo ""

    #--- 4. 计算最优配置 ---
    log_info "计算最优配置..."
    echo ""

    # === CPU 配置 ===
    # 预留 2 核给系统，其余分配给容器；limits 为全部核心
    local cpu_reservation cpu_limit
    if [ "$cpu_cores" -le 4 ]; then
        cpu_reservation=2
        cpu_limit=$cpu_cores
    elif [ "$cpu_cores" -le 8 ]; then
        cpu_reservation=$((cpu_cores - 2))
        cpu_limit=$cpu_cores
    else
        cpu_reservation=$((cpu_cores * 60 / 100))
        cpu_limit=$cpu_cores
    fi

    # === 内存配置 ===
    local mem_reservation_gb mem_limit_gb
    if [ "$is_unified_memory" = true ]; then
        # 统一内存：GPU VRAM 与系统内存共享，容器需要拿到大部分内存
        # 预留 4~8G 给系统，其余都给容器
        local sys_reserve=4
        if [ "$total_mem_gb" -ge 128 ]; then
            sys_reserve=8
        fi
        mem_limit_gb=$((total_mem_gb - sys_reserve))
        mem_reservation_gb=$((total_mem_gb / 8))  # reservation 设为 1/8
    else
        # 独立显存：容器内存按系统内存的 80% 分配
        mem_limit_gb=$((total_mem_gb * 80 / 100))
        mem_reservation_gb=$((total_mem_gb / 8))
    fi
    # 最小值保护
    if [ "$mem_reservation_gb" -lt 4 ]; then mem_reservation_gb=4; fi
    if [ "$mem_limit_gb" -lt 8 ]; then mem_limit_gb=8; fi

    # === Ollama 并行与模型配置 ===
    local num_parallel max_loaded_models context_length kv_cache_type keep_alive

    # 计算可用 VRAM（统一内存取内存总量减系统预留，独立显存取 GPU VRAM）
    local effective_vram_gb
    if [ "$is_unified_memory" = true ]; then
        effective_vram_gb=$mem_limit_gb
    elif [ "$has_nvidia" = true ]; then
        effective_vram_gb=$gpu_vram_gb
    else
        effective_vram_gb=0
    fi

    # 并行数：基于可用 VRAM
    if [ "$effective_vram_gb" -ge 96 ]; then
        num_parallel=8
    elif [ "$effective_vram_gb" -ge 48 ]; then
        num_parallel=4
    elif [ "$effective_vram_gb" -ge 24 ]; then
        num_parallel=2
    else
        num_parallel=1
    fi

    # 同时加载模型数
    if [ "$effective_vram_gb" -ge 96 ]; then
        max_loaded_models=4
    elif [ "$effective_vram_gb" -ge 48 ]; then
        max_loaded_models=3
    elif [ "$effective_vram_gb" -ge 24 ]; then
        max_loaded_models=2
    else
        max_loaded_models=1
    fi

    # 上下文长度
    if [ "$effective_vram_gb" -ge 96 ]; then
        context_length=131072
    elif [ "$effective_vram_gb" -ge 48 ]; then
        context_length=65536
    elif [ "$effective_vram_gb" -ge 24 ]; then
        context_length=32768
    elif [ "$effective_vram_gb" -ge 12 ]; then
        context_length=16384
    else
        context_length=8192
    fi

    # KV 缓存类型：VRAM 充裕用 q8_0，紧张用 q4_0
    if [ "$effective_vram_gb" -ge 48 ]; then
        kv_cache_type="q8_0"
    else
        kv_cache_type="q4_0"
    fi

    # Keep alive：统一内存切换成本低可以更长
    if [ "$is_unified_memory" = true ] && [ "$effective_vram_gb" -ge 64 ]; then
        keep_alive="30m"
    elif [ "$effective_vram_gb" -ge 48 ]; then
        keep_alive="15m"
    else
        keep_alive="5m"
    fi

    # healthcheck start_period：大VRAM机器可能加载大模型需要更久
    local start_period
    if [ "$effective_vram_gb" -ge 64 ]; then
        start_period="120s"
    else
        start_period="60s"
    fi

    #--- 5. 显示优化方案 ---
    echo -e "  ${BOLD}优化方案:${NC}"
    echo ""
    echo "  ┌──────────────────────────────────┬────────────┬────────────────────────┐"
    echo "  │ 配置项                            │ 推荐值     │ 依据                   │"
    echo "  ├──────────────────────────────────┼────────────┼────────────────────────┤"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "deploy.resources.limits.cpus"      "${cpu_limit}.0"         "总核心数: ${cpu_cores}"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "deploy.resources.reservations.cpus" "${cpu_reservation}.0"  "预留系统核心"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "deploy.resources.limits.memory"     "${mem_limit_gb}G"      "总内存: ${total_mem_gb}G"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "deploy.resources.reservations.mem"  "${mem_reservation_gb}G" "最低保障"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "OLLAMA_NUM_PARALLEL"               "$num_parallel"          "有效VRAM: ${effective_vram_gb}G"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "OLLAMA_MAX_LOADED_MODELS"           "$max_loaded_models"     "有效VRAM: ${effective_vram_gb}G"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "OLLAMA_CONTEXT_LENGTH"              "$context_length"        "有效VRAM: ${effective_vram_gb}G"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "OLLAMA_KV_CACHE_TYPE"               "$kv_cache_type"         "VRAM充裕度"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "OLLAMA_KEEP_ALIVE"                  "$keep_alive"            "内存架构/容量"
    printf "  │ %-34s │ %-10s │ %-22s │\n" "healthcheck.start_period"            "$start_period"         "模型加载预估"
    echo "  └──────────────────────────────────┴────────────┴────────────────────────┘"
    echo ""

    if [ "$dry_run" = true ]; then
        log_info "Dry-run 模式，不会修改文件"
        return 0
    fi

    #--- 6. 确认并写入 ---
    if [ "$auto_apply" != true ]; then
        echo -e "  ${YELLOW}将修改: ${DOCKER_COMPOSE_FILE}${NC}"
        read -rp "  应用以上优化? [y/N]: " confirm
        if [[ ! "$confirm" =~ ^[yY]$ ]]; then
            log_info "取消优化"
            return 0
        fi
    fi

    # 备份原文件
    local backup_file="${DOCKER_COMPOSE_FILE}.bak.$(date +%Y%m%d_%H%M%S)"
    cp -f "${DOCKER_COMPOSE_FILE}" "${backup_file}"
    log_step "已备份原文件: ${backup_file}"

    #--- 7. 生成优化后的 docker-compose.yaml ---
    log_step "生成优化配置..."

    # 判断 GPU 设备块
    local gpu_block=""
    if [ "$has_nvidia" = true ]; then
        gpu_block="        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
          cpus: '${cpu_reservation}.0'
          memory: ${mem_reservation_gb}G"
    else
        gpu_block="        reservations:
          cpus: '${cpu_reservation}.0'
          memory: ${mem_reservation_gb}G"
    fi

    cat > "${DOCKER_COMPOSE_FILE}" << COMPOSE_EOF
# ============================================================================
# Ollama Docker Compose 配置
# 由 deploy.sh optimize 自动生成于 $(date '+%Y-%m-%d %H:%M:%S')
# 硬件: ${cpu_model:-Unknown} | ${cpu_cores} cores | ${total_mem_gb}G RAM | ${gpu_name}
# ============================================================================

services:
  ollama:
    image: ollama/ollama:latest
    container_name: ollama
    networks:
      - ai-network
    ports:
      - "11434:11434"
    environment:
      - TZ=Asia/Shanghai
      - OLLAMA_HOST=0.0.0.0
      - NVIDIA_VISIBLE_DEVICES=all
      - OLLAMA_FLASH_ATTENTION=1

      # 并行请求数 (基于 ${effective_vram_gb}G 有效 VRAM)
      - OLLAMA_NUM_PARALLEL=${num_parallel}

      # 同时驻留模型数
      - OLLAMA_MAX_LOADED_MODELS=${max_loaded_models}

      # 模型保持时间
      - OLLAMA_KEEP_ALIVE=${keep_alive}

      # 上下文窗口大小
      - OLLAMA_CONTEXT_LENGTH=${context_length}

      # KV 缓存精度
      - OLLAMA_KV_CACHE_TYPE=${kv_cache_type}

      # 日志级别
      - OLLAMA_DEBUG=INFO

    volumes:
      - ${DATA_DIR}:/root/.ollama
    deploy:
      resources:
${gpu_block}
        limits:
          cpus: '${cpu_limit}.0'
          memory: ${mem_limit_gb}G
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - CHOWN
      - SETUID
      - SETGID
    healthcheck:
      test: ["CMD-SHELL", "curl -sf http://localhost:11434/ || exit 1"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: ${start_period}
    logging:
      driver: json-file
      options:
        max-size: "50m"
        max-file: "5"
    restart: unless-stopped

networks:
  ai-network:
    driver: bridge
COMPOSE_EOF

    echo ""
    print_separator
    log_success "配置优化完成！"
    echo ""
    echo -e "  ${BOLD}文件:${NC}   ${DOCKER_COMPOSE_FILE}"
    echo -e "  ${BOLD}备份:${NC}   ${backup_file}"
    echo ""
    echo -e "  ${BOLD}后续操作:${NC}"
    echo -e "    查看配置:  ${CYAN}cat ${DOCKER_COMPOSE_FILE}${NC}"
    echo -e "    应用生效:  ${CYAN}./deploy.sh restart${NC}"
    echo -e "    回滚配置:  ${CYAN}cp ${backup_file} ${DOCKER_COMPOSE_FILE}${NC}"
    echo ""

    # 如果服务正在运行，提示重启
    if is_api_ready; then
        log_warn "服务正在运行中，需要重启才能使新配置生效"
        if [ "$auto_apply" = true ]; then
            log_step "自动重启服务..."
            do_restart
        else
            read -rp "  现在重启? [y/N]: " restart_confirm
            if [[ "$restart_confirm" =~ ^[yY]$ ]]; then
                do_restart
            fi
        fi
    fi
}

# 搜索 Ollama 官网模型
do_search() {
    local query=""
    local category=""
    local show_all=false
    local max_results=20
    local page=1
    local sort_order=""

    # 解析参数
    while [ $# -gt 0 ]; do
        case "$1" in
            -c|--category) category="$2"; shift 2 ;;
            -n|--num)      max_results="$2"; shift 2 ;;
            -p|--page)     page="$2"; shift 2 ;;
            -s|--sort)
                case "$2" in
                    newest|new|updated) sort_order="newest" ;;
                    popular|pop|hot)    sort_order="popular" ;;
                    *)
                        log_error "未知排序方式: $2 (可选: newest|popular)"
                        return 1
                        ;;
                esac
                shift 2
                ;;
            --newest)      sort_order="newest"; shift ;;
            --all)         show_all=true; shift ;;
            -h|--help)
                echo -e "  ${BOLD}用法:${NC} ./deploy.sh search [关键词] [选项]"
                echo ""
                echo -e "  ${BOLD}选项:${NC}"
                echo "    -c, --category <type>   按类型筛选 (vision|tools|thinking|embedding|cloud)"
                echo "    -n, --num <count>       显示数量 (默认20, 超过20自动翻页)"
                echo "    -p, --page <num>        起始页码 (默认1, 每页20条)"
                echo "    -s, --sort <order>      排序方式: newest=最近更新 | popular=热门(默认)"
                echo "    --newest                按最近更新排序 (等同 -s newest)"
                echo "    --all                   显示所有模型 (不按本机硬件过滤)"
                echo ""
                echo -e "  ${BOLD}示例:${NC}"
                echo "    ./deploy.sh search                  # 浏览热门模型 (自动匹配本机)"
                echo "    ./deploy.sh search qwen             # 搜索 qwen 相关模型"
                echo "    ./deploy.sh search -c vision        # 搜索视觉模型"
                echo "    ./deploy.sh search coder --all      # 搜索代码模型 (不过滤)"
                echo "    ./deploy.sh search -n 50            # 显示50个结果 (自动拉取3页)"
                echo "    ./deploy.sh search -p 3             # 从第3页开始浏览"
                echo "    ./deploy.sh search -n 100 -p 2      # 从第2页开始显示100条"
                echo "    ./deploy.sh search --newest         # 按最近更新排序"
                echo "    ./deploy.sh search -s newest -n 50  # 最近更新的50个模型"
                return 0
                ;;
            -*)
                log_error "未知选项: $1"
                return 1
                ;;
            *)
                query="$1"; shift
                ;;
        esac
    done

    log_info "检索 Ollama 官网模型库..."
    echo ""

    #--- 1. 检测本机硬件容量 ---
    local effective_vram_gb=0
    local hw_summary=""

    if [ "$show_all" != true ]; then
        # 检测内存
        local total_mem_mb=0
        if [ -f /proc/meminfo ]; then
            total_mem_mb=$(awk '/MemTotal/ {printf "%.0f", $2/1024}' /proc/meminfo)
        elif command -v sysctl &>/dev/null; then
            local mem_bytes
            mem_bytes=$(sysctl -n hw.memsize 2>/dev/null || echo "0")
            total_mem_mb=$((mem_bytes / 1024 / 1024))
        fi
        local total_mem_gb=$((total_mem_mb / 1024))

        # 检测 GPU
        local gpu_vram_gb=0
        local gpu_name="N/A"
        local is_unified_memory=false

        if command -v nvidia-smi &>/dev/null; then
            gpu_name=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | head -1 || echo "N/A")
            local gpu_vram_mb
            gpu_vram_mb=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | head -1 | tr -dc '0-9' || echo "0")
            if ! [[ "$gpu_vram_mb" =~ ^[0-9]+$ ]] || [ -z "$gpu_vram_mb" ]; then
                gpu_vram_mb=0
            fi
            gpu_vram_gb=$((gpu_vram_mb / 1024))

            # 统一内存检测
            local mem_diff=$((total_mem_gb - gpu_vram_gb))
            [ "$mem_diff" -lt 0 ] && mem_diff=$((-mem_diff))
            if [ "$total_mem_gb" -gt 0 ] && [ "$gpu_vram_gb" -gt 0 ]; then
                local threshold=$((total_mem_gb * 20 / 100))
                if [ "$mem_diff" -le "$threshold" ] || echo "$gpu_name" | grep -qiE "GH200|Grace|GB10|GB200|Jetson"; then
                    is_unified_memory=true
                fi
            fi
            if echo "$gpu_name" | grep -qiE "GH200|Grace|GB10|GB200"; then
                is_unified_memory=true
            fi

            if [ "$is_unified_memory" = true ]; then
                local sys_reserve=4
                [ "$total_mem_gb" -ge 128 ] && sys_reserve=8
                effective_vram_gb=$((total_mem_gb - sys_reserve))
                hw_summary="${gpu_name} | ${total_mem_gb}G 统一内存 | 可用 ~${effective_vram_gb}G"
            else
                effective_vram_gb=$gpu_vram_gb
                hw_summary="${gpu_name} | ${gpu_vram_gb}G VRAM | 系统 ${total_mem_gb}G"
            fi
        else
            # 无 GPU，仅 CPU 推理
            effective_vram_gb=$((total_mem_gb * 70 / 100))
            hw_summary="CPU-only | 系统 ${total_mem_gb}G | 可用 ~${effective_vram_gb}G"
        fi

        echo -e "  ${BOLD}本机硬件:${NC} ${hw_summary}"
        echo ""
    fi

    #--- 2. 构建 URL 并抓取页面（支持多页） ---
    local base_url="https://ollama.com/search"
    local base_params="?"
    if [ -n "$query" ]; then
        base_params="${base_params}q=$(python3 -c "import urllib.parse; print(urllib.parse.quote('${query}'))" 2>/dev/null || echo "$query")&"
    fi
    if [ -n "$category" ]; then
        base_params="${base_params}c=${category}&"
    fi
    if [ -n "$sort_order" ]; then
        base_params="${base_params}o=${sort_order}&"
    fi

    local html=""
    local current_page=$page
    local pages_needed=$(( (max_results + 19) / 20 ))  # 每页20条，向上取整
    local pages_fetched=0

    while [ $pages_fetched -lt $pages_needed ]; do
        local url="${base_url}${base_params}page=${current_page}"
        if [ $pages_fetched -eq 0 ]; then
            log_info "正在从 ${url} 获取数据..."
        else
            log_info "正在获取第 ${current_page} 页..."
        fi

        local page_html
        page_html=$(curl -sf --connect-timeout 10 --max-time 30 \
            -H "User-Agent: Mozilla/5.0" \
            -H "Accept: text/html" \
            "$url" 2>/dev/null)

        if [ -z "$page_html" ]; then
            if [ $pages_fetched -eq 0 ]; then
                log_error "无法访问 Ollama 官网，请检查网络连接"
                echo "  提示: 可尝试手动访问 https://ollama.com/search"
                return 1
            fi
            break  # 后续页获取失败就停止
        fi

        # 检查本页是否有模型数据
        local model_count
        model_count=$(echo "$page_html" | grep -c 'x-test-model' 2>/dev/null || echo "0")
        if [ "$model_count" -eq 0 ]; then
            if [ $pages_fetched -eq 0 ]; then
                log_warn "未找到任何模型"
            fi
            break  # 没有更多数据了
        fi

        html="${html}${page_html}"
        pages_fetched=$((pages_fetched + 1))
        current_page=$((current_page + 1))
    done

    #--- 3. 检测本地 Ollama 翻译能力 ---
    local ollama_translate_model=""
    local ollama_api="http://localhost:11434"

    # 尝试找到一个可用的小模型做翻译
    if curl -sf --connect-timeout 2 "${ollama_api}/api/tags" &>/dev/null; then
        local available_models
        available_models=$(curl -sf "${ollama_api}/api/tags" 2>/dev/null | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    names = [m['name'] for m in data.get('models', [])]
    # 优先选小模型做翻译
    prefer = ['qwen2.5:7b', 'qwen2.5:14b', 'qwen3:8b', 'qwen3:4b', 'qwen3:1.7b', 'qwen3:0.6b', 'glm4:9b', 'llama3.1:8b', 'gemma2:9b', 'mistral:7b']
    for p in prefer:
        for n in names:
            if n.startswith(p.split(':')[0]) and ((':' not in p) or p in n):
                print(n); sys.exit(0)
    # 任何含 qwen/glm/llama/gemma/mistral 的都行
    for n in names:
        for kw in ['qwen', 'glm', 'llama', 'gemma', 'mistral']:
            if kw in n.lower():
                print(n); sys.exit(0)
    # 实在没有就用第一个
    if names:
        print(names[0])
except: pass
" 2>/dev/null)
        if [ -n "$available_models" ]; then
            ollama_translate_model="$available_models"
            log_info "将使用本地模型 ${ollama_translate_model} 翻译描述"
        fi
    fi

    #--- 4. 解析 HTML 并展示 ---
    echo "$html" | python3 -c "
import sys
import re
import html as html_mod
import json
import urllib.request
import textwrap

content = sys.stdin.read()
ollama_model = '${ollama_translate_model}'
ollama_api = '${ollama_api}'

# ============================================================
# 翻译函数
# ============================================================
_translate_cache = {}

def ollama_translate(text):
    \"\"\"用本地 Ollama 模型翻译英文为中文\"\"\"
    if not ollama_model or not text:
        return text
    if text in _translate_cache:
        return _translate_cache[text]
    try:
        prompt = f'将以下英文翻译为简洁流畅的中文，只输出翻译结果，不要解释：\n{text}'
        payload = json.dumps({
            'model': ollama_model,
            'prompt': prompt,
            'stream': False,
            'options': {'temperature': 0.1, 'num_predict': 256}
        }).encode('utf-8')
        req = urllib.request.Request(
            f'{ollama_api}/api/generate',
            data=payload,
            headers={'Content-Type': 'application/json'}
        )
        with urllib.request.urlopen(req, timeout=15) as resp:
            result = json.loads(resp.read().decode('utf-8'))
            translated = result.get('response', '').strip()
            # 清理可能的前缀
            for prefix in ['翻译：', '翻译:', '译文：', '译文:']:
                if translated.startswith(prefix):
                    translated = translated[len(prefix):].strip()
            if translated:
                _translate_cache[text] = translated
                return translated
    except:
        pass
    return text

def translate_desc(text):
    \"\"\"翻译模型描述\"\"\"
    if not text:
        return '(无描述)'
    # 如果有本地模型，用整句翻译
    if ollama_model:
        return ollama_translate(text)
    # 否则保留原文
    return text

# ============================================================
# 解析 HTML — 基于 x-test-* 属性精确提取
# ============================================================
models = []

# 按 <li x-test-model> 分割模型卡片
cards = re.split(r'<li\s+x-test-model[^>]*>', content)
cards = cards[1:]  # 第一段是页面头部，跳过

for card in cards:
    name_m = re.search(r'<span\s+x-test-search-response-title[^>]*>([^<]+)</span>', card)
    if not name_m:
        continue

    model = {
        'name': name_m.group(1).strip(),
        'desc': '',
        'sizes': [],
        'tags': [],
        'pulls': '',
        'updated': '',
    }

    # 提取能力标签 (vision/tools/thinking/embedding/code/cloud)
    for cap_m in re.finditer(r'<span\s+x-test-capability[^>]*>([^<]+)</span>', card):
        tag = cap_m.group(1).strip().lower()
        if tag and tag not in model['tags']:
            model['tags'].append(tag)

    # 提取参数大小 (7b/14b/70b/0.6b/350m 等)
    for size_m in re.finditer(r'<span\s+x-test-size[^>]*>([^<]+)</span>', card):
        s = size_m.group(1).strip().lower()
        if s and s not in model['sizes']:
            model['sizes'].append(s)

    # 提取下载量
    pull_m = re.search(r'<span\s+x-test-pull-count[^>]*>([^<]+)</span>', card)
    if pull_m:
        model['pulls'] = pull_m.group(1).strip()

    # 提取更新时间
    upd_m = re.search(r'<span\s+x-test-updated[^>]*>([^<]+)</span>', card)
    if upd_m:
        model['updated'] = upd_m.group(1).strip()

    # 提取描述文本 (在 <p> 标签中的较长文本)
    desc_m = re.search(r'<p[^>]*>([^<]{20,})</p>', card)
    if desc_m:
        model['desc'] = html_mod.unescape(desc_m.group(1).strip())
    else:
        # 回退：找 href="/library/name" 后的较长纯文本
        text_blocks = re.findall(r'>([^<]{25,})<', card)
        for tb in text_blocks:
            tb = html_mod.unescape(tb.strip())
            if tb and not re.match(r'^[\d,.]+[KMB]?\s', tb):
                model['desc'] = tb
                break

    models.append(model)

# 去重
seen = set()
unique_models = []
for m in models:
    if m['name'] not in seen:
        seen.add(m['name'])
        unique_models.append(m)
models = unique_models

if not models:
    print('  未找到任何模型。请尝试其他搜索关键词或直接访问:')
    print('  https://ollama.com/search')
    sys.exit(0)

# ============================================================
# 硬件过滤
# ============================================================
effective_vram = ${effective_vram_gb}
show_all = '${show_all}'
start_page = ${page}
pages_fetched = ${pages_fetched}
sort_order = '${sort_order}'

def parse_size_gb(size_str):
    \"\"\"将参数量标记转为大致模型文件大小(GB)\"\"\"
    size_str = size_str.lower().strip()
    m = re.match(r'([\d.]+)([bm])', size_str)
    if not m:
        return 0
    num = float(m.group(1))
    unit = m.group(2)
    if unit == 'm':
        # 百万参数，按 FP16 约 0.002GB/M 参数
        return num * 0.002
    else:
        # 十亿参数，按 Q4 量化约 0.6GB/B 参数
        return num * 0.6

def get_fit_sizes(sizes, vram_gb):
    \"\"\"过滤出适合指定 VRAM 的参数大小\"\"\"
    if not sizes:
        return sizes, True  # 无参数信息时保留
    fit = []
    for s in sizes:
        est_gb = parse_size_gb(s)
        if est_gb <= vram_gb * 0.85:  # 留 15% 余量
            fit.append(s)
    return fit, len(fit) > 0

# 分类：适合 / 不适合
fit_models = []
big_models = []

for m in models:
    if show_all == 'true' or effective_vram == 0:
        fit_models.append(m)
    else:
        fit_sizes, has_fit = get_fit_sizes(m['sizes'], effective_vram)
        if has_fit:
            m['fit_sizes'] = fit_sizes
            fit_models.append(m)
        else:
            big_models.append(m)

# ============================================================
# 格式化输出
# ============================================================

# 标签中文映射
TAG_CN = {
    'vision': '👁 视觉',
    'tools': '🔧 工具',
    'thinking': '🧠 推理',
    'embedding': '📐 嵌入',
    'cloud': '☁ 云端',
    'code': '💻 代码',
}

def format_sizes(sizes, fit_sizes=None):
    if not sizes:
        return '(查看详情)'
    if fit_sizes is not None:
        parts = []
        for s in sizes:
            if s in fit_sizes:
                parts.append('\033[0;32m' + s + '\033[0m')  # 绿色=适合
            else:
                parts.append('\033[2m' + s + '\033[0m')      # 暗色=太大
        return ' | '.join(parts)
    return ' | '.join(sizes)

def format_pulls(p):
    if not p:
        return ''
    return f'⬇ {p}'

def format_updated(u):
    if not u:
        return ''
    # 翻译英文时间为中文
    t = u.strip()
    t = re.sub(r'(\d+)\s+seconds?\s+ago', r'\1秒前', t)
    t = re.sub(r'(\d+)\s+minutes?\s+ago', r'\1分钟前', t)
    t = re.sub(r'(\d+)\s+hours?\s+ago', r'\1小时前', t)
    t = re.sub(r'(\d+)\s+days?\s+ago', r'\1天前', t)
    t = re.sub(r'(\d+)\s+weeks?\s+ago', r'\1周前', t)
    t = re.sub(r'(\d+)\s+months?\s+ago', r'\1个月前', t)
    t = re.sub(r'(\d+)\s+years?\s+ago', r'\1年前', t)
    t = t.replace('yesterday', '昨天')
    t = t.replace('just now', '刚刚')
    return f'🕐 {t}'

max_show = ${max_results}
shown = 0

if fit_models:
    sort_label = ' · 按最近更新' if sort_order == 'newest' else ''
    if show_all != 'true' and effective_vram > 0:
        print(f'  \033[1m✅ 适合本机的模型 ({len(fit_models)} 个，VRAM ≈{effective_vram}G{sort_label}):\033[0m')
    else:
        print(f'  \033[1m📦 模型列表 ({len(fit_models)} 个{sort_label}):\033[0m')
    print()

    for m in fit_models[:max_show]:
        shown += 1
        name = m['name']
        desc = translate_desc(m.get('desc', ''))
        sizes = m.get('sizes', [])
        fit_sz = m.get('fit_sizes', sizes)
        tags = m.get('tags', [])
        pulls = m.get('pulls', '')
        updated = m.get('updated', '')

        # 第1行: 序号 + 名称 + 下载量 + 更新时间
        pulls_str = format_pulls(pulls)
        updated_str = format_updated(updated)
        print(f'  \033[1;36m{shown:>2}. {name}\033[0m', end='')
        if pulls_str:
            print(f'  \033[2m{pulls_str}\033[0m', end='')
        if updated_str:
            print(f'  \033[2m{updated_str}\033[0m', end='')
        print()

        # 第2行: 描述 (翻译后，完整显示)
        if desc:
            # 自动换行，每行宽度 70 字符，缩进 6 空格
            wrapped = textwrap.fill(desc, width=70, initial_indent='      ', subsequent_indent='      ')
            print(wrapped)

        # 第3行: 参数规格 + 标签
        info_parts = []
        size_str = format_sizes(sizes, fit_sz if show_all != 'true' else None)
        if size_str:
            info_parts.append(f'参数: {size_str}')
        if tags:
            tag_str = ' '.join(TAG_CN.get(t, t) for t in tags)
            info_parts.append(tag_str)
        if info_parts:
            print(f'      {\"  │  \".join(info_parts)}')

        # 第4行: 安装命令
        # 选择最大的适合大小，或默认
        install_tag = ''
        if fit_sz:
            # 找最大适合尺寸
            best = max(fit_sz, key=lambda s: parse_size_gb(s))
            if best != sizes[0] if sizes else True:
                install_tag = f':{best}'
        print(f'      \033[2m$ ollama pull {name}{install_tag}\033[0m')
        print()

    if len(fit_models) > max_show:
        print(f'  \033[2m... 还有 {len(fit_models) - max_show} 个模型，使用 -n {len(fit_models)} 查看全部\033[0m')
        print()

if big_models and show_all != 'true':
    print(f'  \033[2m──────────────────────────────────────────────────────────────\033[0m')
    print(f'  \033[1;33m⚠ 超出本机容量的模型 ({len(big_models)} 个):\033[0m')
    for m in big_models[:5]:
        sizes_str = ' | '.join(m.get('sizes', []))
        est = max((parse_size_gb(s) for s in m.get('sizes', ['0b'])), default=0)
        print(f'      \033[2m{m[\"name\"]:30s} 参数: {sizes_str:20s} 最小需 ~{est:.0f}G VRAM\033[0m')
    if len(big_models) > 5:
        print(f'      \033[2m... 还有 {len(big_models) - 5} 个\033[0m')
    print()

# 底部提示
next_page = start_page + pages_fetched
print(f'  \033[2m────────────────────────────────────────────────────────\033[0m')
print(f'  \033[2m数据来源: https://ollama.com/search  (已获取第 {start_page}~{start_page + pages_fetched - 1} 页)\033[0m')
if show_all != 'true' and effective_vram > 0:
    print(f'  \033[2m绿色参数 = 适合本机 | 使用 --all 查看全部模型\033[0m')
print(f'  \033[2m下一页: ./deploy.sh search -p {next_page} | 更多: -n 50 自动拉取多页\033[0m')
print(f'  \033[2m拉取模型: ./deploy.sh pull <模型名>\033[0m')
print()
" 2>/dev/null

    local py_exit=$?
    if [ $py_exit -ne 0 ]; then
        log_error "解析失败，可能是网络问题或页面结构变更"
        echo ""
        echo "  请直接访问: https://ollama.com/search"
        return 1
    fi
}

# 显示帮助
show_help() {
    print_banner
    echo "用法: ./deploy.sh [命令] [选项]"
    echo ""
    echo -e "${BOLD}服务管理:${NC}"
    echo "  start               启动 Ollama 服务"
    echo "  stop                停止 Ollama 服务"
    echo "  restart             重启 Ollama 服务"
    echo "  status              查看服务状态 (容器/模型/GPU/磁盘)"
    echo "  logs [lines]        查看日志 (默认200行, Ctrl+C退出)"
    echo "  update              拉取最新镜像并重启"
    echo ""
    echo -e "${BOLD}模型管理:${NC}"
    echo "  pull <model>        拉取/更新模型"
    echo "  rm <model>          删除已下载模型 (-f 跳过确认)"
    echo "  models              列出所有已下载模型"
    echo "  run <model>         交互式运行模型"
    echo "  search [keyword]    搜索Ollama官网模型 (自动匹配本机硬件)"
    echo "                        -c <type>  按类型筛选 (vision|tools|thinking|embedding)"
    echo "                        -s <order> 排序: newest=最近更新 | popular=热门(默认)"
    echo "                        --newest   按最近更新排序"
    echo "                        --all      显示所有模型不过滤"
    echo ""
    echo -e "${BOLD}GPU 与性能:${NC}"
    echo "  gpu                 查看GPU详细信息"
    echo "  bench <model>       运行性能基准测试"
    echo "  health              全面健康检查 (9项)"
    echo "  optimize            检测硬件并优化docker-compose配置"
    echo "                        --dry-run  仅显示方案不修改文件"
    echo "                        --yes      跳过确认直接应用"
    echo ""
    echo -e "${BOLD}维护操作:${NC}"
    echo "  init                初始化部署环境"
    echo "  backup [name]       备份模型与配置"
    echo "  restore [file]      恢复模型与配置"
    echo "  clean <mode>        清理 (--soft|--hard|--purge)"
    echo "  exec [cmd]          进入容器 (默认bash)"
    echo "  help                显示帮助信息"
    echo ""
    echo -e "${BOLD}示例:${NC}"
    echo -e "  ${DIM}# 首次部署${NC}"
    echo "  ./deploy.sh init"
    echo "  ./deploy.sh start"
    echo "  ./deploy.sh pull qwen2.5:72b-instruct-q4_K_M"
    echo ""
    echo -e "  ${DIM}# 日常使用${NC}"
    echo "  ./deploy.sh run qwen2.5:72b-instruct-q4_K_M"
    echo "  ./deploy.sh bench qwen2.5:72b-instruct-q4_K_M"
    echo "  ./deploy.sh status"
    echo ""
    echo -e "  ${DIM}# 维护操作${NC}"
    echo "  ./deploy.sh logs 500"
    echo "  ./deploy.sh backup weekly_backup"
    echo "  ./deploy.sh update"
    echo "  ./deploy.sh clean --soft"
    echo "  ./deploy.sh optimize              # 检测硬件自动优化配置"
    echo "  ./deploy.sh optimize --dry-run    # 仅查看优化方案"
    echo "  ./deploy.sh search                # 搜索适合本机的模型"
    echo "  ./deploy.sh search qwen -c tools  # 搜索带工具能力的qwen模型"
    echo ""
    echo -e "${BOLD}硬件:${NC} NVIDIA DGX Spark (GB10) | 120 GiB 统一内存 | CUDA 12.x"
    echo ""
}

#-------------------------------------------------------------------------------
# 主程序
#-------------------------------------------------------------------------------

main() {
    local command="${1:-help}"
    shift 2>/dev/null || true

    case "$command" in
        start)
            check_requirements
            print_banner
            do_start "$@"
            ;;
        stop)
            print_banner
            do_stop
            ;;
        restart)
            print_banner
            do_restart "$@"
            ;;
        status)
            print_banner
            do_status
            ;;
        logs)
            do_logs "$@"
            ;;
        update)
            check_requirements
            print_banner
            do_update
            ;;
        clean)
            print_banner
            do_clean "$@"
            ;;
        init)
            check_requirements
            print_banner
            do_init
            ;;
        backup)
            print_banner
            do_backup "$@"
            ;;
        restore)
            print_banner
            do_restore "$@"
            ;;
        pull)
            do_pull "$@"
            ;;
        rm|remove|delete)
            do_rm "$@"
            ;;
        models|model)
            do_models
            ;;
        run)
            do_run "$@"
            ;;
        bench|benchmark)
            print_banner
            do_bench "$@"
            ;;
        gpu|nvidia)
            print_banner
            do_gpu
            ;;
        exec|shell)
            do_exec "$@"
            ;;
        health|check)
            print_banner
            do_health
            ;;
        optimize|opt|tune)
            print_banner
            do_optimize "$@"
            ;;
        search|find|browse)
            print_banner
            do_search "$@"
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            log_error "未知命令: ${command}"
            echo ""
            echo "运行 './deploy.sh help' 查看所有可用命令"
            exit 1
            ;;
    esac
}

main "$@"
