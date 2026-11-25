#!/usr/bin/env bash
# ============================================================
# SIPVoice 卸载
#
# 用法：
#   sudo ./uninstall.sh           # 停止并移除服务，保留配置与数据
#   sudo ./uninstall.sh --purge   # 连同 /etc/sipvoice 与 /opt/sipvoice 一起删除
#
# 注意：不会自动删除 Asterisk 包与 /etc/asterisk 配置（可能被别的服务共用），
#       如需清理由你自行 apt remove asterisk。
# ============================================================
set -euo pipefail

APP=/opt/sipvoice
ETC=/etc/sipvoice
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

log() { printf '\033[1;34m[sipvoice]\033[0m %s\n' "$*"; }

[ "$(id -u)" -eq 0 ] || { echo "请用 sudo 运行" >&2; exit 1; }

systemctl stop sipvoice 2>/dev/null || true
systemctl disable sipvoice 2>/dev/null || true
rm -f /etc/systemd/system/sipvoice.service
rm -f /etc/sudoers.d/sipvoice-asterisk
systemctl daemon-reload
log "已停止并移除 sipvoice 服务"

if [ "$PURGE" -eq 1 ]; then
    rm -rf "$ETC" "$APP"
    id sipvoice >/dev/null 2>&1 && userdel sipvoice 2>/dev/null || true
    log "已删除 $ETC、$APP 与 sipvoice 用户"
else
    log "配置与二进制已保留：$ETC、$APP（加 --purge 可一并删除）"
fi
