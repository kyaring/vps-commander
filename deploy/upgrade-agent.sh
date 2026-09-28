#!/bin/bash
set -e

# VPS-Commander Agent 一键平滑安全升级脚本
# 支持 GitHub Release 与 Gitea 多源智能下载及自动故障切换

GITHUB_REPO="kyaring/vps-commander"
GITEA_HOST="gitea.king.nyc.mn"
GITEA_REPO="openclaw/vps-commander"

INSTALL_DIR="/opt/vps-commander"

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
[[ $EUID -ne 0 ]] && error "必须使用 root 权限运行此脚本"

# 检查是否安装过 agent
if [ ! -f "${INSTALL_DIR}/vps-commander-agent" ]; then
    error "未检测到已安装的 Agent (${INSTALL_DIR}/vps-commander-agent)，请先使用 install.sh 完整安装。"
fi

# 架构检测
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) TARGET_ARCH="amd64" ;;
    aarch64|arm64) TARGET_ARCH="arm64" ;;
    *) error "暂不支持的系统架构: $ARCH" ;;
esac

info "正在获取最新 Release 版本信息..."
LATEST_TAG=$(curl -fsSL -k --connect-timeout 4 "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null | grep '"tag_name":' | head -n1 | sed -E 's/.*"([^"]+)".*/\1/')
if [ -z "$LATEST_TAG" ]; then
    warn "无法通过 GitHub API 获取 Tag，回退至稳定版本 v1.1.5"
    LATEST_TAG="v1.1.5"
fi
info "目标升级版本: ${LATEST_TAG} (${TARGET_ARCH})"

BIN_NAME="vps-commander-agent-linux-${TARGET_ARCH}"
TMP_BIN="${INSTALL_DIR}/vps-commander-agent.tmp"
BAK_BIN="${INSTALL_DIR}/vps-commander-agent.bak"

GITHUB_URL="https://github.com/${GITHUB_REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"
GITEA_URL="https://${GITEA_HOST}/${GITEA_REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"

info "正在下载新版本二进制文件..."
# 优先尝试 GitHub，失败自动切换 Gitea
if ! curl -fsSL -k --connect-timeout 8 -o "${TMP_BIN}" "${GITHUB_URL}"; then
    warn "GitHub 下载失败或超时，自动切换至 Gitea 私有镜像下载..."
    curl -fsSL -k -o "${TMP_BIN}" "${GITEA_URL}" || error "所有源下载均失败，请检查网络"
fi

# 完整性校验：文件大小不能小于 1MB
FILE_SIZE=$(stat -c%s "${TMP_BIN}" 2>/dev/null || stat -f%z "${TMP_BIN}" 2>/dev/null || echo 0)
if [ "$FILE_SIZE" -lt 1000000 ]; then
    rm -f "${TMP_BIN}"
    error "下载的文件不完整或损坏 (大小: ${FILE_SIZE} 字节)，已终止升级"
fi

chmod +x "${TMP_BIN}"

info "备份旧版本并原子替换..."
cp -f "${INSTALL_DIR}/vps-commander-agent" "${BAK_BIN}" 2>/dev/null || true
mv -f "${TMP_BIN}" "${INSTALL_DIR}/vps-commander-agent"

info "重启 vps-commander-agent 服务..."
systemctl restart vps-commander-agent
sleep 2

if systemctl is-active --quiet vps-commander-agent; then
    success "Agent 成功平滑升级至 ${LATEST_TAG} 并已恢复运行！"
    rm -f "${BAK_BIN}"
else
    warn "服务启动异常，正在自动回滚原版本..."
    mv -f "${BAK_BIN}" "${INSTALL_DIR}/vps-commander-agent"
    systemctl restart vps-commander-agent
    error "升级失败，已安全回滚至上一版本，请查看日志: journalctl -u vps-commander-agent -n 20"
fi
