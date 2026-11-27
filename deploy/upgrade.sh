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

panel_port() {
    # 面板端口可能被 drop-in 改过（ExecStart 里的 -web 0.0.0.0:PORT），
    # 写死 8080 会在这种机器上误判「升级后未就绪」并自动回滚
    local p
    p=$(systemctl show -p ExecStart --value sipvoice 2>/dev/null \
        | grep -oE '\-web [^ ]+' | grep -oE '[0-9]+$' | head -n1)
    printf '%s' "${p:-8080}"
}

panel_ok() {
    local port code
    port=$(panel_port)
    code=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 "https://127.0.0.1:$port/api/status" || true)
    case "$code" in 200|401) return 0 ;; esac
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$port/api/status" || true)
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

# ------------------------------------------------------------
# 2.0 升级前置：补齐新依赖、修目录权限、准备密钥文件
# 1.x 升级上来时这些都不存在，缺了会导致新版本起不来
# ------------------------------------------------------------
prepare_v2() {
    log "检查 2.0 所需的新依赖"

    local need=""
    command -v paps >/dev/null 2>&1                 || need="$need paps"
    command -v ps2pdf >/dev/null 2>&1               || need="$need ghostscript"
    fc-list 2>/dev/null | grep -qi "Noto Sans CJK"  || need="$need fonts-noto-cjk"
    sox -h 2>/dev/null | grep -qi "mp3"             || need="$need libsox-fmt-mp3"
    python3 -c 'import venv' 2>/dev/null            || need="$need python3-venv"

    if [ -n "$need" ]; then
        log "安装：$need"
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq || log "apt-get update 失败，继续尝试"
        apt-get install -y -qq $need || log "部分依赖安装失败，文本转传真等功能可能不可用"
    else
        log "依赖齐全"
    fi

    if [ -d /etc/sipvoice ]; then
        chown root:sipvoice /etc/sipvoice 2>/dev/null || true
        chmod 2770 /etc/sipvoice 2>/dev/null || true
        log "已调整 /etc/sipvoice 权限为 2770 root:sipvoice"
    fi

    if [ ! -f /etc/sipvoice/secrets.json ]; then
        : > /etc/sipvoice/secrets.json
        log "已创建 secrets.json（首次保存配置时会把密钥迁进去）"
    fi
    chown sipvoice:sipvoice /etc/sipvoice/secrets.json 2>/dev/null || true
    chmod 600 /etc/sipvoice/secrets.json 2>/dev/null || true

    install -d -m 775 -o asterisk -g sipvoice /var/spool/asterisk/fax 2>/dev/null || true
    install -d -m 775 -o asterisk -g sipvoice /var/lib/sipvoice/fax-failed 2>/dev/null || true
    install -d -m 755 /var/lib/sipvoice/prompts 2>/dev/null || true
    install -d -m 777 /var/lib/sipvoice/callmap 2>/dev/null || true

    if id sipvoice >/dev/null 2>&1 && getent group asterisk >/dev/null 2>&1; then
        usermod -aG asterisk sipvoice 2>/dev/null || true
    fi
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
NEW_VER=$("$NEW" -version 2>/dev/null | head -n1 || echo "?")

# 升到 2.x 时先补齐依赖与权限，否则新版本起不来
case "$NEW_VER" in
    *[2-9].*|*1[0-9].*) prepare_v2 ;;
esac

systemctl stop sipvoice
install -m 755 -o root -g root "$NEW" "$BIN_PATH"
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
