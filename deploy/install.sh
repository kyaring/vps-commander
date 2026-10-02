#!/bin/bash
set -euo pipefail

# VPS-Commander 一键安装与配置脚本 (Hub / Agent)
# Repo: https://github.com/kyaring/vps-commander

REPO="kyaring/vps-commander"
INSTALL_DIR="/opt/vps-commander"
CONF_DIR="/etc/vps-commander"
RELEASE_BASE_URL="https://github.com"
API_URL="https://api.github.com"
LATEST_INSTALL_URL="https://github.com/${REPO}/releases/latest/download/install.sh"
INSTALL_COMMAND="curl -fsSL ${LATEST_INSTALL_URL} | sudo bash"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
info() { echo -e "${BLUE}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

[[ $EUID -ne 0 ]] && error "必须使用 root 权限运行此脚本 (例如: sudo bash install.sh)"
ARCH=$(uname -m)
case "$ARCH" in x86_64|amd64) TARGET_ARCH="amd64" ;; aarch64|arm64) TARGET_ARCH="arm64" ;; *) error "暂不支持的系统架构: $ARCH" ;; esac
command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y curl || yum install -y curl)

info "正在获取 VPS-Commander 最新版本信息..."
LATEST_TAG=$(curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 30 "${API_URL}/repos/${REPO}/releases/latest" | sed -n 's/^[[:space:]]*"tag_name": "\([^"]*\)".*/\1/p' | head -n1)
[ -n "$LATEST_TAG" ] || error "无法从 GitHub 获取最新正式 Release，已停止安装；不会回退到旧版本"
info "检测到最新版本: ${LATEST_TAG} (${TARGET_ARCH})"

TMP_FILES=()
cleanup() { [ "${#TMP_FILES[@]}" -eq 0 ] || rm -f "${TMP_FILES[@]}"; }
trap cleanup EXIT

download_verified() {
    local url="$1" dest="$2" asset_name="$3" tmp checksum expected actual
    tmp=$(mktemp "${dest}.tmp.XXXXXX") || error "无法创建临时下载文件: ${dest}"
    TMP_FILES+=("$tmp")
    info "正在下载: ${url}"
    curl -fL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 600 -o "$tmp" "$url" || error "下载失败: ${url}"
    [ -s "$tmp" ] || error "下载结果为空: ${asset_name}"
    command -v sha256sum >/dev/null 2>&1 || error "系统缺少 sha256sum，无法安全校验 Release"
    checksum=$(curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 60 "${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/checksums.txt") || error "无法获取 Release 校验文件"
    expected=$(printf '%s\n' "$checksum" | awk -v f="$asset_name" '$2 == f {print $1; exit}')
    [ -n "$expected" ] || error "Release 校验文件中缺少 ${asset_name}"
    actual=$(sha256sum "$tmp" | awk '{print $1}')
    [ "$actual" = "$expected" ] || error "SHA256 校验失败: ${asset_name}"
    mv -f "$tmp" "$dest" || error "无法安装下载文件到: ${dest}"
}

mkdir -p "$INSTALL_DIR" "$INSTALL_DIR/data" "$CONF_DIR"
chmod 750 "$INSTALL_DIR" "$CONF_DIR"

read_tty() {
    local prompt="$1" __var="$2" secret="${3:-0}" value
    if [ ! -r /dev/tty ]; then
        error "检测不到可用 TTY。请在带控制终端的 shell 中运行安装脚本。"
    fi
    if [ "$secret" = "1" ]; then
        if ! IFS= read -r -s -p "$prompt" value </dev/tty; then
            echo >&2
            error "无法读取敏感配置输入，请在带 TTY 的终端中运行安装脚本。"
        fi
        echo >&2
    else
        if ! IFS= read -r -p "$prompt" value </dev/tty; then
            error "无法读取交互输入，请在带 TTY 的终端中运行安装脚本。"
        fi
    fi
    printf -v "$__var" '%s' "$value"
}

validate_hub_ws() {
    case "$1" in *[[:space:]]*|*'$'*|*'`'*|*'"'*|*\\*) return 1 ;; esac
    [[ "$1" =~ ^wss://[^[:space:]]+/agent/ws$ ]]
}
validate_agent_name() { [[ "$1" =~ ^[A-Za-z0-9._:-]+$ ]]; }
validate_port() { [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }

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
    download_verified "$DOWNLOAD_URL" "$INSTALL_DIR/vps-commander-agent" "$BIN_NAME"
    chmod +x "$INSTALL_DIR/vps-commander-agent"

    echo -e "\n--- 配置 Agent 参数 ---"
    read_tty "请输入 Hub WSS 连接地址 (默认: wss://vpstool.kory.kdns.fr/agent/ws): " HUB_WS
    HUB_WS=${HUB_WS:-"wss://vpstool.kory.kdns.fr/agent/ws"}
    validate_hub_ws "$HUB_WS" || error "Hub WSS 地址格式无效或包含 shell 元字符: ${HUB_WS}"

    DEFAULT_NAME=$(hostname)
    read_tty "请输入本节点名称 (默认: ${DEFAULT_NAME}): " AGENT_NAME
    AGENT_NAME=${AGENT_NAME:-${DEFAULT_NAME}}
    validate_agent_name "$AGENT_NAME" || error "节点名称仅允许字母、数字、点、下划线、冒号和连字符: ${AGENT_NAME}"

    read_tty "请输入 Agent 独立凭据 Token: " AGENT_TOKEN 1
    while [ -z "$AGENT_TOKEN" ]; do
        read_tty "Agent Token 不能为空，请重新输入: " AGENT_TOKEN 1
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
    if ! grep -Fq "ExecStart=${INSTALL_DIR}/vps-commander-agent -hub \${VPS_COMMANDER_HUB_WS_URL} -name \${VPS_COMMANDER_AGENT_NAME}" /etc/systemd/system/vps-commander-agent.service; then
        error "生成的 Agent systemd 服务异常，ExecStart 未通过校验。安装已中止。"
    fi

    INSTALL_CHECK_SINCE=$(date --iso-8601=seconds)
    systemctl enable --now vps-commander-agent
    sleep 2
    systemctl is-active --quiet vps-commander-agent || error "Agent 服务启动失败，请执行: journalctl -u vps-commander-agent -n 50 --no-pager"
    AGENT_CMD=$(ps -eo args= | grep -F "${INSTALL_DIR}/vps-commander-agent" | grep -v grep | head -n1 || true)
    [[ "$AGENT_CMD" == *"-hub ${HUB_WS} -name ${AGENT_NAME}"* ]] || error "Agent 进程参数校验失败: ${AGENT_CMD}"

    CONNECTED=0
    for _ in {1..13}; do
        if journalctl -u vps-commander-agent --since "$INSTALL_CHECK_SINCE" --no-pager -o cat 2>/dev/null | grep -q "MCP services synced:"; then CONNECTED=1; break; fi
        sleep 1
    done
    [ "$CONNECTED" -eq 1 ] || error "Agent 已启动，但 15 秒内未确认成功连接 Hub，请检查: journalctl -u vps-commander-agent -n 50 --no-pager"
    success "VPS-Commander Agent 安装、启动并完成 Hub 连接校验！"
    info "节点名称: ${AGENT_NAME}"
}

install_hub() {
    info "开始安装 VPS-Commander Hub (${TARGET_ARCH})..."
    BIN_NAME="vps-commander-hub-linux-${TARGET_ARCH}"
    DOWNLOAD_URL="${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"
    download_verified "$DOWNLOAD_URL" "$INSTALL_DIR/vps-commander-hub" "$BIN_NAME"
    chmod +x "$INSTALL_DIR/vps-commander-hub"

    echo -e "\n--- 配置 Hub 参数 ---"
    if [ -f "${CONF_DIR}/hub.env" ]; then
        info "检测到已有 Hub 配置，将保留现有 API Key / Web 密码，不覆盖凭据。"
        if grep -q '^VPS_COMMANDER_API_KEY=' "${CONF_DIR}/hub.env" && grep -q '^VPS_COMMANDER_WEB_PASSWORD=' "${CONF_DIR}/hub.env"; then
            API_KEY=$(sed -n 's/^VPS_COMMANDER_API_KEY=//p' "${CONF_DIR}/hub.env" | head -n1)
            WEB_PASS=$(sed -n 's/^VPS_COMMANDER_WEB_PASSWORD=//p' "${CONF_DIR}/hub.env" | head -n1)
        else
            error "已有 hub.env 缺少必要凭据字段；为避免破坏现有配置，安装已中止。请先人工修复或备份后再运行。"
        fi
        HUB_PORT=$(sed -n 's/.*-addr 127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' /etc/systemd/system/vps-commander-hub.service 2>/dev/null | head -n1)
        HUB_PORT=${HUB_PORT:-9521}
    else
        read_tty "请输入监听端口 (默认 9521): " HUB_PORT
        HUB_PORT=${HUB_PORT:-9521}
        validate_port "$HUB_PORT" || error "监听端口必须为 1-65535 的数字: ${HUB_PORT}"
        GEN_API_KEY=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
        GEN_PASS=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
        read_tty "请输入 API Key (默认随机生成): " API_KEY 1
        API_KEY=${API_KEY:-$GEN_API_KEY}
        read_tty "请输入 Web 管理面板密码 (默认随机生成): " WEB_PASS 1
        WEB_PASS=${WEB_PASS:-$GEN_PASS}
        cat << ENV > "${CONF_DIR}/hub.env"
VPS_COMMANDER_API_KEY=${API_KEY}
VPS_COMMANDER_WEB_PASSWORD=${WEB_PASS}
VPS_COMMANDER_INSTALL_COMMAND="${INSTALL_COMMAND}"
ENV
        chmod 0600 "${CONF_DIR}/hub.env"
    fi

    validate_port "$HUB_PORT" || error "监听端口必须为 1-65535 的数字: ${HUB_PORT}"
    if [ -f "${CONF_DIR}/hub.env" ]; then
        if grep -q '^VPS_COMMANDER_INSTALL_COMMAND=' "${CONF_DIR}/hub.env"; then
            sed -i "s#^VPS_COMMANDER_INSTALL_COMMAND=.*#VPS_COMMANDER_INSTALL_COMMAND=\"${INSTALL_COMMAND}\"#" "${CONF_DIR}/hub.env"
        else
            printf '\nVPS_COMMANDER_INSTALL_COMMAND="%s"\n' "$INSTALL_COMMAND" >> "${CONF_DIR}/hub.env"
        fi
        chmod 0600 "${CONF_DIR}/hub.env"
    fi

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
        info "监听地址: 127.0.0.1:${HUB_PORT}"
        warn "现有凭据已保留，不会在重复安装时重新生成。"
    else
        warn "服务已启动但状态可能异常，请检查 journalctl -u vps-commander-hub -n 20。"
    fi
}

select_mode
case "$MODE" in 1) install_agent ;; 2) install_hub ;; *) error "无效选项" ;; esac
