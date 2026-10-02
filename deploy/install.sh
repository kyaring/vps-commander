#!/bin/bash
set -e

# VPS-Commander 一键安装与配置脚本 (Hub / Agent)
# Repo: https://github.com/kyaring/vps-commander

REPO="kyaring/vps-commander"
INSTALL_DIR="/opt/vps-commander"
CONF_DIR="/etc/vps-commander"
RELEASE_BASE_URL="https://github.com"
API_URL="https://api.github.com"
RAW_INSTALL_URL="https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/install.sh"
INSTALL_COMMAND="curl -fsSL ${RAW_INSTALL_URL} | bash"

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

# 获取最新版本 tag
info "正在获取 VPS-Commander 最新版本信息..."
LATEST_TAG=$(curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 30 "${API_URL}/repos/${REPO}/releases/latest" | sed -n 's/^[[:space:]]*"tag_name": "\([^"]*\)".*/\1/p' | head -n1)
if [ -z "$LATEST_TAG" ]; then
    error "无法从 GitHub 获取最新正式 Release，已停止安装；不会回退到旧版本"
fi
info "检测到最新版本: ${LATEST_TAG} (${TARGET_ARCH})"

download_verified() {
    local url="$1" dest="$2" asset_name="$3"
    local tmp checksum expected actual
    tmp=$(mktemp "${dest}.tmp.XXXXXX") || error "无法创建临时下载文件: ${dest}"
    rm -f "$tmp"
    trap 'rm -f "${tmp}"' RETURN
    info "正在下载: ${url}"
    if ! curl -fL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 600 -o "$tmp" "$url"; then
        rm -f "$tmp"
        error "下载失败: ${url}"
    fi
    if [ ! -s "$tmp" ]; then
        rm -f "$tmp"
        error "下载结果为空: ${asset_name}"
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        checksum=$(curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 60 "${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/checksums.txt") || error "无法获取 Release 校验文件"
        expected=$(printf '%s\n' "$checksum" | awk -v f="$asset_name" '$2 == f {print $1; exit}')
        [ -n "$expected" ] || error "Release 校验文件中缺少 ${asset_name}"
        actual=$(sha256sum "$tmp" | awk '{print $1}')
        [ "$actual" = "$expected" ] || error "SHA256 校验失败: ${asset_name}"
    else
        error "系统缺少 sha256sum，无法安全校验 Release"
    fi
    mv -f "$tmp" "$dest" || error "无法安装下载文件到: ${dest}"
    trap - RETURN
}

mkdir -p "$INSTALL_DIR" "$INSTALL_DIR/data" "$CONF_DIR"
chmod 750 "$INSTALL_DIR" "$CONF_DIR"

read_tty() {
    local prompt="$1"
    local __var="$2"
    if [ -r /dev/tty ]; then
        local value
        if ! IFS= read -r -p "$prompt" value < /dev/tty; then
            error "无法读取交互输入，请在带 TTY 的终端中运行安装脚本；也可以通过环境变量提供配置。"
        fi
        printf -v "$__var" '%s' "$value"
    else
        error "检测不到可用 TTY。当前命令可能通过管道运行；请使用带终端的 shell 执行，例如: curl -fsSL ${RAW_INSTALL_URL} | bash"
    fi
}

select_mode() {
    echo -e "\n请选择安装模式:"
    echo "1) 安装受控端 Agent (推荐：在受控 VPS 上一键连入集群)"
    echo "2) 安装中心端 Hub (管理中心)"
    read_tty "请输入选项 [1-2] (默认 1): " MODE
    MODE=${MODE:-1}
}

install_agent() {
    info "开始安装 VPS-Commander Agent (${TARGET_ARCH})..."
    BIN_NAME="vps-commander-agent-linux-${TARGET_ARCH}"
    DOWNLOAD_URL="${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"

    download_verified "${DOWNLOAD_URL}" "${INSTALL_DIR}/vps-commander-agent" "${BIN_NAME}"
    chmod +x "${INSTALL_DIR}/vps-commander-agent"

    echo -e "\n--- 配置 Agent 参数 ---"
    read_tty "请输入 Hub WSS 连接地址 (默认: wss://vpstool.kory.kdns.fr/agent/ws): " HUB_WS
    HUB_WS=${HUB_WS:-"wss://vpstool.kory.kdns.fr/agent/ws"}

    DEFAULT_NAME=$(hostname)
    read_tty "请输入本节点名称 (默认: ${DEFAULT_NAME}): " AGENT_NAME
    AGENT_NAME=${AGENT_NAME:-${DEFAULT_NAME}}

    read_tty "请输入 Agent 独立凭据 Token: " AGENT_TOKEN
    while [ -z "$AGENT_TOKEN" ]; do
        read_tty "Agent Token 不能为空，请重新输入: " AGENT_TOKEN
    done

    cat << ENV > "${CONF_DIR}/agent.env"
VPS_COMMANDER_HUB_WS_URL=${HUB_WS}
VPS_COMMANDER_AGENT_NAME=${AGENT_NAME}
VPS_COMMANDER_AGENT_TOKEN=${AGENT_TOKEN}
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
ExecStart=/bin/sh -c 'exec ${INSTALL_DIR}/vps-commander-agent -hub "$VPS_COMMANDER_HUB_WS_URL" -name "$VPS_COMMANDER_AGENT_NAME"'
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
    DOWNLOAD_URL="${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"

    download_verified "${DOWNLOAD_URL}" "${INSTALL_DIR}/vps-commander-hub" "${BIN_NAME}"
    chmod +x "${INSTALL_DIR}/vps-commander-hub"

    echo -e "\n--- 配置 Hub 参数 ---"
    read_tty "请输入监听端口 (默认 9521): " HUB_PORT
    HUB_PORT=${HUB_PORT:-9521}

    GEN_API_KEY=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
    GEN_PASS=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')

    read_tty "请输入 API Key (默认随机生成): " API_KEY
    API_KEY=${API_KEY:-$GEN_API_KEY}

    read_tty "请输入 Web 管理面板密码 (默认随机生成): " WEB_PASS
    WEB_PASS=${WEB_PASS:-$GEN_PASS}

    cat << ENV > "${CONF_DIR}/hub.env"
VPS_COMMANDER_API_KEY=${API_KEY}
VPS_COMMANDER_WEB_PASSWORD=${WEB_PASS}
VPS_COMMANDER_INSTALL_COMMAND="${INSTALL_COMMAND}"
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
