#!/usr/bin/env bash
# ============================================================
# SIPVoice 升级 / 回滚
#
# 用法：
#   sudo ./upgrade.sh /path/to/voicebridge   # 升级到新二进制，失败自动回滚
#   sudo ./upgrade.sh --rollback             # 回滚到上一个备份
#   sudo ./upgrade.sh --list                 # 查看可用备份
# ============================================================
set -euo pipefail

APP=/opt/sipvoice
BIN_PATH=$APP/voicebridge
BACKUP=$APP/backups
KEEP=5

log()  { printf '\033[1;34m[sipvoice]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[错误]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "请用 sudo 运行"
[ -x "$BIN_PATH" ] || die "未发现已安装的 $BIN_PATH，请先运行 install.sh"
mkdir -p "$BACKUP"

panel_ok() {
    local code
    code=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 https://127.0.0.1:8080/api/status || true)
    case "$code" in 200|401) return 0 ;; esac
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:8080/api/status || true)
    case "$code" in 200|401) return 0 ;; esac
    return 1
}

wait_ready() {
    for _ in $(seq 1 20); do
        systemctl is-active --quiet sipvoice && panel_ok && return 0
        sleep 1
    done
    return 1
}

restore() {
    local src="$1"
    log "回滚到 $src"
    systemctl stop sipvoice 2>/dev/null || true
    install -m 755 -o root -g root "$src" "$BIN_PATH"
    systemctl start sipvoice
    wait_ready || log "回滚后仍未就绪，请查看 journalctl -u sipvoice"
}

case "${1:-}" in
    ""|-h|--help)
        sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
        exit 0
        ;;
    --list)
        ls -1t "$BACKUP"/voicebridge.* 2>/dev/null | sed "s#$BACKUP/##" || echo "（还没有备份）"
        exit 0
        ;;
    --rollback)
        LAST=$(ls -1t "$BACKUP"/voicebridge.* 2>/dev/null | head -n1 || true)
        [ -n "$LAST" ] || die "没有可用的备份"
        restore "$LAST"
        log "完成"
        exit 0
        ;;
esac

NEW="$1"
[ -f "$NEW" ] || die "找不到新二进制：$NEW"

TS=$(date +%Y%m%d-%H%M%S)
cp -a "$BIN_PATH" "$BACKUP/voicebridge.$TS"
log "已备份当前版本 → $BACKUP/voicebridge.$TS"

OLD_VER=$("$BIN_PATH" -version 2>/dev/null | head -n1 || echo "?")

systemctl stop sipvoice
install -m 755 -o root -g root "$NEW" "$BIN_PATH"
NEW_VER=$("$BIN_PATH" -version 2>/dev/null | head -n1 || echo "?")
systemctl start sipvoice

if wait_ready; then
    log "升级成功：$OLD_VER → $NEW_VER"
    # 只保留最近 N 个备份
    ls -1t "$BACKUP"/voicebridge.* 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -f
    exit 0
fi

log "新版本未就绪，自动回滚"
restore "$BACKUP/voicebridge.$TS"
die "升级失败，已回滚到 $OLD_VER"
