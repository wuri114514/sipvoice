#!/usr/bin/env bash
# ============================================================
# SIPVoice 卸载
#
# 用法：
#   sudo ./uninstall.sh           # 停止并移除服务，保留配置与数据
#   sudo ./uninstall.sh --purge   # 连同 /etc/sipvoice、/opt/sipvoice 与录音/传真数据一起删除
#
# 注意：不会自动删除 Asterisk 包与 /etc/asterisk 配置（可能被别的服务共用），
#       如需清理由你自行 apt remove asterisk。
# ============================================================
set -euo pipefail

APP=/opt/sipvoice
ETC=/etc/sipvoice
LIB=/var/lib/sipvoice
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

log() { printf '\033[1;34m[sipvoice]\033[0m %s\n' "$*"; }

[ "$(id -u)" -eq 0 ] || { echo "请用 sudo 运行" >&2; exit 1; }

# 1. 先停服务
systemctl stop sipvoice 2>/dev/null || true
systemctl disable sipvoice 2>/dev/null || true

# 2. 录音清理定时器：不先停掉的话，它还会按计划去跑已删除的脚本
if [ -f /etc/systemd/system/sipvoice-rec-cleanup.timer ]; then
    systemctl disable --now sipvoice-rec-cleanup.timer 2>/dev/null || true
fi

# 3. 移除服务单元与提权配置
rm -f /etc/systemd/system/sipvoice.service
rm -f /etc/systemd/system/sipvoice-rec-cleanup.service
rm -f /etc/systemd/system/sipvoice-rec-cleanup.timer
rm -f /etc/systemd/system/multi-user.target.wants/sipvoice.service
rm -f /etc/systemd/system/timers.target.wants/sipvoice-rec-cleanup.timer
rm -f /etc/sudoers.d/sipvoice-asterisk
systemctl daemon-reload
log "已停止并移除 sipvoice 服务"

if [ "$PURGE" -eq 1 ]; then
    # 面板调用的存储脚本、录音策略与日志轮转
    rm -f /usr/local/sbin/sipvoice-rec-cleanup.sh
    rm -f /usr/local/sbin/sipvoice-storage-apply.sh
    rm -f /etc/default/sipvoice-rec
    # logrotate：只在确认为 SIPVoice 生成的那份时才删，避免误删发行版自带的
    if [ -f /etc/logrotate.d/asterisk ] && grep -q 'SIPVoice' /etc/logrotate.d/asterisk 2>/dev/null; then
        rm -f /etc/logrotate.d/asterisk
    fi

    rm -rf "$ETC" "$APP" "$LIB"
    id sipvoice >/dev/null 2>&1 && userdel sipvoice 2>/dev/null || true
    log "已删除 $ETC、$APP、$LIB 与 sipvoice 用户"
else
    log "配置与二进制已保留：$ETC、$APP（加 --purge 可一并删除）"
fi
