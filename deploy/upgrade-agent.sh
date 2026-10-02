#!/bin/bash
set -euo pipefail

# VPS-Commander Agent 一键安全升级脚本
# Repo: https://github.com/kyaring/vps-commander

REPO="kyaring/vps-commander"
INSTALL_DIR="/opt/vps-commander"
RELEASE_BASE_URL="https://github.com"
API_URL="https://api.github.com"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
info() { echo -e "${BLUE}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

[[ $EUID -ne 0 ]] && error "必须使用 root 权限运行此脚本"
[ -f "${INSTALL_DIR}/vps-commander-agent" ] || error "未检测到已安装的 Agent (${INSTALL_DIR}/vps-commander-agent)，请先使用 install.sh 完整安装。"
command -v curl >/dev/null 2>&1 || error "系统缺少 curl"
command -v sha256sum >/dev/null 2>&1 || error "系统缺少 sha256sum，无法安全校验 Release"

ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) TARGET_ARCH="amd64" ;;
    aarch64|arm64) TARGET_ARCH="arm64" ;;
    *) error "暂不支持的系统架构: $ARCH" ;;
esac

info "正在获取最新正式 Release 版本信息..."
LATEST_TAG=$(curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 30 \
    "${API_URL}/repos/${REPO}/releases/latest" |
    sed -n 's/^[[:space:]]*"tag_name": "\\([^"]*\\)".*/\\1/p' | head -n1)
[ -n "$LATEST_TAG" ] || error "无法从 GitHub 获取最新正式 Release，已停止升级；不会回退到旧版本"
info "目标升级版本: ${LATEST_TAG} (${TARGET_ARCH})"

BIN_NAME="vps-commander-agent-linux-${TARGET_ARCH}"
DOWNLOAD_URL="${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"
CHECKSUM_URL="${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/checksums.txt"
TMP_BIN=$(mktemp "${INSTALL_DIR}/vps-commander-agent.tmp.XXXXXX") || error "无法创建临时文件"
TMP_SUM=$(mktemp "${INSTALL_DIR}/vps-commander-checksums.tmp.XXXXXX") || { rm -f "$TMP_BIN"; error "无法创建校验文件"; }
BAK_BIN="${INSTALL_DIR}/vps-commander-agent.bak"
cleanup() { rm -f "$TMP_BIN" "$TMP_SUM"; }
trap cleanup EXIT

info "正在下载新版本二进制文件..."
curl -fL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 600 -o "$TMP_BIN" "$DOWNLOAD_URL" || error "下载失败: ${DOWNLOAD_URL}"
[ -s "$TMP_BIN" ] || error "下载结果为空"

info "正在校验 SHA256..."
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 60 -o "$TMP_SUM" "$CHECKSUM_URL" || error "无法获取 Release 校验文件"
EXPECTED=$(awk -v f="$BIN_NAME" '$2 == f {print $1; exit}' "$TMP_SUM")
[ -n "$EXPECTED" ] || error "Release 校验文件中缺少 ${BIN_NAME}"
ACTUAL=$(sha256sum "$TMP_BIN" | awk '{print $1}')
[ "$ACTUAL" = "$EXPECTED" ] || error "SHA256 校验失败: ${BIN_NAME}"
info "SHA256 校验通过: ${ACTUAL}"

chmod +x "$TMP_BIN"
info "备份旧版本并原子替换..."
cp -f "${INSTALL_DIR}/vps-commander-agent" "$BAK_BIN" 2>/dev/null || true
mv -f "$TMP_BIN" "${INSTALL_DIR}/vps-commander-agent"

info "重启 vps-commander-agent 服务..."
systemctl restart vps-commander-agent
sleep 2
if systemctl is-active --quiet vps-commander-agent; then
    success "Agent 成功升级至 ${LATEST_TAG}，SHA256 已验证并恢复运行。"
    rm -f "$BAK_BIN"
else
    warn "服务启动异常，正在自动回滚原版本..."
    mv -f "$BAK_BIN" "${INSTALL_DIR}/vps-commander-agent"
    systemctl restart vps-commander-agent
    error "升级失败，已安全回滚至上一版本，请查看日志: journalctl -u vps-commander-agent -n 20"
fi
