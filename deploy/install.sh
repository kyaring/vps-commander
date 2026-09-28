#!/bin/bash
set -e

# VPS-Commander 一键安装与配置脚本 (Hub / Agent)
# 支持 GitHub Release 与 Gitea 多源智能下载及自动故障切换

GITHUB_REPO="kyaring/vps-commander"
GITEA_HOST="gitea.king.nyc.mn"
GITEA_REPO="openclaw/vps-commander"

INSTALL_DIR="/opt/vps-commander"
CONF_DIR="/etc/vps-commander"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info() { echo -e "${BLUE}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# 检查 root 权限
[[ $EUID -ne 0 ]] && error "必须使用 root 权限运行此脚本 (例如: sudo bash install.sh)"

# 架构检测
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) TARGET_ARCH="amd64" ;;
    aarch64|arm64) TARGET_ARCH="arm64" ;;
    *) error "暂不支持的系统架构: $ARCH (仅支持 x86_64/amd64 与 aarch64/arm64)" ;;
esac

# 检查基础依赖
command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y curl || yum install -y curl)

# 动态解析最新版本 Tag
info "正在获取最新 Release 版本信息..."
LATEST_TAG=$(curl -fsSL -k --connect-timeout 4 "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null | grep '"tag_name":' | head -n1 | sed -E 's/.*"([^"]+)".*/\1/')
if [ -z "$LATEST_TAG" ]; then
    warn "无法通过 GitHub API 获取 Tag，回退至稳定基线版本 v1.1.5"
    LATEST_TAG="v1.1.5"
fi
info "目标安装版本: ${LATEST_TAG} (${TARGET_ARCH})"

mkdir -p "$INSTALL_DIR" "$INSTALL_DIR/data" "$CONF_DIR"
chmod 750 "$INSTALL_DIR" "$CONF_DIR"

# 智能多源下载函数（支持 GitHub 与 Gitea 自动容灾互备）
download_binary() {
    local BIN_NAME="$1"
    local TARGET_FILE="$2"
    
    local GITHUB_URL="https://github.com/${GITHUB_REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"
    local GITEA_URL="https://${GITEA_HOST}/${GITEA_REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"
    
    # 判断是否优先使用 Gitea（如果脚本调用来源为 Gitea 或显式指定 USE_GITEA=1）
    if [[ "$DOWNLOAD_SOURCE" == "gitea" ]] || [[ "$USE_GITEA" == "1" ]]; then
        info "正在从私有源 Gitea 下载: ${GITEA_URL}"
        if curl -fsSL -k --connect-timeout 8 -o "${TARGET_FILE}" "${GITEA_URL}"; then
            return 0
        fi
        warn "Gitea 源下载失败，尝试回退至 GitHub 源..."
    fi

    info "正在从 GitHub 下载: ${GITHUB_URL}"
    if curl -fsSL -k --connect-timeout 8 -o "${TARGET_FILE}" "${GITHUB_URL}"; then
        return 0
    fi

    warn "GitHub 下载失败或超时，自动切换至 Gitea 备用源: ${GITEA_URL}"
    curl -fsSL -k -o "${TARGET_FILE}" "${GITEA_URL}" || error "所有源下载均失败，请检查网络连接！"
}

select_mode() {
    echo -e "\n请选择安装模式:"
    echo "1) 安装受控端 Agent (推荐：在受控 VPS 上一键连入集群)"
    echo "2) 安装中心端 Hub (管理中心)"
    read -rp "请输入选项 [1-2] (默认 1): " MODE
    MODE=${MODE:-1}
}

install_agent() {
    info "开始安装 VPS-Commander Agent (${TARGET_ARCH})..."
    BIN_NAME="vps-commander-agent-linux-${TARGET_ARCH}"
    
    download_binary "${BIN_NAME}" "${INSTALL_DIR}/vps-commander-agent"
    chmod +x "${INSTALL_DIR}/vps-commander-agent"

    echo -e "\n--- 配置 Agent 参数 ---"
    read -rp "请输入 Hub WSS 连接地址 (默认: wss://vpstool.kory.kdns.fr/agent/ws): " HUB_WS
    HUB_WS=${HUB_WS:-"wss://vpstool.kory.kdns.fr/agent/ws"}

    DEFAULT_NAME=$(hostname)
    read -rp "请输入本节点名称 (默认: ${DEFAULT_NAME}): " AGENT_NAME
    AGENT_NAME=${AGENT_NAME:-${DEFAULT_NAME}}

    read -rp "请输入 Cluster Secret (集群通信密钥): " CLUSTER_SECRET
    while [ -z "$CLUSTER_SECRET" ]; do
        read -rp "Cluster Secret 不能为空，请重新输入: " CLUSTER_SECRET
    done

    cat << ENV > "${CONF_DIR}/agent.env"
VPS_COMMANDER_HUB_WS_URL=${HUB_WS}
VPS_COMMANDER_AGENT_NAME=${AGENT_NAME}
VPS_COMMANDER_CLUSTER_SECRET=${CLUSTER_SECRET}
ENV
    chmod 0600 "${CONF_DIR}/agent.env"

    cat << SVC > /etc/systemd/system/vps-commander-agent.service
[Unit]
Description=VPS-Commander Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=-${CONF_DIR}/agent.env
ExecStart=${INSTALL_DIR}/vps-commander-agent -hub \${VPS_COMMANDER_HUB_WS_URL} -name \${VPS_COMMANDER_AGENT_NAME}
Restart=always
RestartSec=3s
KillSignal=SIGTERM
TimeoutStopSec=10s
LimitNOFILE=65536
Environment=GOGC=50

[Install]
WantedBy=multi-user.target
SVC

    systemctl daemon-reload
    systemctl enable --now vps-commander-agent
    sleep 1

    if systemctl is-active --quiet vps-commander-agent; then
        success "VPS-Commander Agent 安装并启动成功！"
        info "节点名称: ${AGENT_NAME}"
        info "可通过 'systemctl status vps-commander-agent' 查看运行状态。"
    else
        warn "服务已启动但状态可能异常，请检查 'journalctl -u vps-commander-agent -n 20'。"
    fi
}

install_hub() {
    info "开始安装 VPS-Commander Hub (${TARGET_ARCH})..."
    BIN_NAME="vps-commander-hub-linux-${TARGET_ARCH}"

    download_binary "${BIN_NAME}" "${INSTALL_DIR}/vps-commander-hub"
    chmod +x "${INSTALL_DIR}/vps-commander-hub"

    echo -e "\n--- 配置 Hub 参数 ---"
    read -rp "请输入监听端口 (默认 9521): " HUB_PORT
    HUB_PORT=${HUB_PORT:-9521}

    GEN_API_KEY=$(head -c 24 /dev/urandom | xxd -p)
    GEN_SECRET=$(head -c 24 /dev/urandom | xxd -p)
    GEN_PASS=$(head -c 16 /dev/urandom | xxd -p)

    read -rp "请输入 API Key (默认随机生成): " API_KEY
    API_KEY=${API_KEY:-$GEN_API_KEY}

    read -rp "请输入 Cluster Secret (默认随机生成): " CLUSTER_SECRET
    CLUSTER_SECRET=${CLUSTER_SECRET:-$GEN_SECRET}

    read -rp "请输入 Web 管理面板密码 (默认随机生成): " WEB_PASS
    WEB_PASS=${WEB_PASS:-$GEN_PASS}

    cat << ENV > "${CONF_DIR}/hub.env"
VPS_COMMANDER_API_KEY=${API_KEY}
VPS_COMMANDER_CLUSTER_SECRET=${CLUSTER_SECRET}
VPS_COMMANDER_WEB_PASSWORD=${WEB_PASS}
ENV
    chmod 0600 "${CONF_DIR}/hub.env"

    cat << SVC > /etc/systemd/system/vps-commander-hub.service
[Unit]
Description=VPS-Commander Hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=-${CONF_DIR}/hub.env
ExecStart=${INSTALL_DIR}/vps-commander-hub -addr 127.0.0.1:${HUB_PORT} -db ${INSTALL_DIR}/data/commander.db
Restart=always
RestartSec=3s
KillSignal=SIGTERM
TimeoutStopSec=10s
LimitNOFILE=65536
Environment=GOGC=50

[Install]
WantedBy=multi-user.target
SVC

    systemctl daemon-reload
    systemctl enable --now vps-commander-hub
    sleep 1

    if systemctl is-active --quiet vps-commander-hub; then
        success "VPS-Commander Hub 安装并启动成功！"
        echo -e "\n================== Hub 配置凭据 =================="
        echo -e "监听地址: 127.0.0.1:${HUB_PORT}"
        echo -e "API Key: ${API_KEY}"
        echo -e "Cluster Secret: ${CLUSTER_SECRET}"
        echo -e "Web 面板密码: ${WEB_PASS}"
        echo -e "==================================================\n"
        warn "请妥善保管上述凭据，反代请将域名反代至 127.0.0.1:${HUB_PORT}"
    else
        warn "服务已启动但状态可能异常，请检查 'journalctl -u vps-commander-hub -n 20'。"
    fi
}

select_mode
if [ "$MODE" = "1" ]; then
    install_agent
elif [ "$MODE" = "2" ]; then
    install_hub
else
    error "无效选项"
fi
