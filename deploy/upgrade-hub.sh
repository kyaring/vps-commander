#!/bin/bash
set -euo pipefail

# VPS-Commander Hub 一键安全升级脚本
# Repo: https://github.com/kyaring/vps-commander
# 只替换 Hub 二进制，不修改 /etc/vps-commander/hub.env。

REPO="kyaring/vps-commander"
INSTALL_DIR="/opt/vps-commander"
CONF_DIR="/etc/vps-commander"
RELEASE_BASE_URL="https://github.com"
API_URL="https://api.github.com"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
info() { echo -e "${BLUE}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

[[ $EUID -ne 0 ]] && error "必须使用 root 权限运行此脚本"
[ -f "${INSTALL_DIR}/vps-commander-hub" ] || error "未检测到已安装的 Hub，请先使用 install.sh 完整安装。"
[ -f "${CONF_DIR}/hub.env" ] || error "未检测到 Hub 配置 ${CONF_DIR}/hub.env，为避免升级时丢失配置已停止。"
command -v curl >/dev/null 2>&1 || error "系统缺少 curl"
command -v sha256sum >/dev/null 2>&1 || error "系统缺少 sha256sum，无法安全校验 Release"

ARCH=$(uname -m)
case "$ARCH" in x86_64|amd64) TARGET_ARCH="amd64" ;; aarch64|arm64) TARGET_ARCH="arm64" ;; *) error "暂不支持的系统架构: $ARCH" ;; esac

SERVICE_FILE="/etc/systemd/system/vps-commander-hub.service"
[ -f "$SERVICE_FILE" ] || error "未检测到 Hub systemd 服务"
HUB_PORT=$(sed -n 's/.*-addr 127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' "$SERVICE_FILE" | head -n1)
[ -n "$HUB_PORT" ] || HUB_PORT=9521
[[ "$HUB_PORT" =~ ^[0-9]+$ ]] && [ "$HUB_PORT" -ge 1 ] && [ "$HUB_PORT" -le 65535 ] || error "无法从现有 Hub unit 获取合法端口: $HUB_PORT"

info "正在获取最新正式 Release 版本信息..."
LATEST_TAG=$(curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 30 \
    "${API_URL}/repos/${REPO}/releases/latest" |
    sed -n 's/^[[:space:]]*"tag_name": "\([^"]*\)".*/\1/p' | head -n1)
[ -n "$LATEST_TAG" ] || error "无法从 GitHub 获取最新正式 Release，已停止升级；不会回退到旧版本"
info "目标升级版本: ${LATEST_TAG} (${TARGET_ARCH})"

BIN_NAME="vps-commander-hub-linux-${TARGET_ARCH}"
DOWNLOAD_URL="${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/${BIN_NAME}"
CHECKSUM_URL="${RELEASE_BASE_URL}/${REPO}/releases/download/${LATEST_TAG}/checksums.txt"
TMP_BIN=$(mktemp "${INSTALL_DIR}/vps-commander-hub.tmp.XXXXXX") || error "无法创建临时文件"
TMP_SUM=$(mktemp "${INSTALL_DIR}/vps-commander-checksums.tmp.XXXXXX") || { rm -f "$TMP_BIN"; error "无法创建校验文件"; }
BAK_BIN="${INSTALL_DIR}/vps-commander-hub.bak"
cleanup() { rm -f "$TMP_BIN" "$TMP_SUM"; }
trap cleanup EXIT

curl -fL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 600 -o "$TMP_BIN" "$DOWNLOAD_URL" || error "下载失败: ${DOWNLOAD_URL}"
[ -s "$TMP_BIN" ] || error "下载结果为空"
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 60 -o "$TMP_SUM" "$CHECKSUM_URL" || error "无法获取 Release 校验文件"
EXPECTED=$(awk -v f="$BIN_NAME" '$2 == f {print $1; exit}' "$TMP_SUM")
[ -n "$EXPECTED" ] || error "Release 校验文件中缺少 ${BIN_NAME}"
ACTUAL=$(sha256sum "$TMP_BIN" | awk '{print $1}')
[ "$ACTUAL" = "$EXPECTED" ] || error "SHA256 校验失败: ${BIN_NAME}"
chmod +x "$TMP_BIN"

rm -f "$BAK_BIN"
cp -f "${INSTALL_DIR}/vps-commander-hub" "$BAK_BIN" || error "无法创建 Hub 旧版本备份，升级已中止"
chmod 0755 "$BAK_BIN"
mv -f "$TMP_BIN" "${INSTALL_DIR}/vps-commander-hub" || error "无法替换 Hub 二进制，旧版本仍保留"

INSTALL_CHECK_SINCE=$(date --iso-8601=seconds)
if ! systemctl restart vps-commander-hub; then
    warn "Hub 重启失败，正在回滚原版本..."
    if mv -f "$BAK_BIN" "${INSTALL_DIR}/vps-commander-hub" && systemctl restart vps-commander-hub; then
        error "升级失败，已恢复旧版本并重新启动 Hub。"
    fi
    error "升级失败且旧版本恢复后仍无法启动，请立即检查: systemctl status vps-commander-hub; journalctl -u vps-commander-hub -n 50 --no-pager"
fi

if ! systemctl is-active --quiet vps-commander-hub; then
    warn "Hub 服务未保持 active，正在回滚..."
    if mv -f "$BAK_BIN" "${INSTALL_DIR}/vps-commander-hub" && systemctl restart vps-commander-hub; then
        error "升级失败，已恢复旧版本并重新启动 Hub。"
    fi
    error "升级失败且旧版本恢复后仍无法启动，请立即检查 Hub。"
fi

CONNECTED=0
for _ in {1..13}; do
    if curl -fsS --max-time 2 "http://127.0.0.1:${HUB_PORT}/healthz" >/dev/null 2>&1; then
        CONNECTED=1
        break
    fi
    sleep 1
done

if [ "$CONNECTED" -ne 1 ]; then
    warn "Hub 进程运行但 /healthz 未在 15 秒内恢复，正在回滚..."
    if mv -f "$BAK_BIN" "${INSTALL_DIR}/vps-commander-hub" && systemctl restart vps-commander-hub; then
        error "升级失败，已恢复旧版本并重新启动 Hub；请检查反代和日志。"
    fi
    error "升级失败且旧版本恢复后仍无法启动，请立即检查: journalctl -u vps-commander-hub -n 50 --no-pager"
fi

rm -f "$BAK_BIN"
success "Hub 成功升级至 ${LATEST_TAG}，SHA256、systemd、/healthz 均验证通过；hub.env 未修改。"
