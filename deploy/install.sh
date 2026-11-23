#!/usr/bin/env bash
# ============================================================
# SIPVoice 一键部署（幂等，可重复执行）
#
# 用法：
#   sudo ./install.sh                 # 用当前目录下的 voicebridge
#   sudo ./install.sh /path/to/bin    # 指定二进制
#   sudo ./install.sh --skip-asterisk # 只装 Go 服务（Asterisk 自己管）
#
# 做的事：装依赖 → 建用户/目录 → 装二进制 → 写默认配置
#         → 部署 Asterisk 配置 → systemd → 健康检查
# 重复执行不会覆盖已有配置与 Asterisk 设置。
# ============================================================
set -euo pipefail

APP=/opt/sipvoice
ETC=/etc/sipvoice
MODELS=$APP/models
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN=""
SKIP_ASTERISK=0

usage() {
    sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
}

while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help) usage ;;
        --skip-asterisk) SKIP_ASTERISK=1 ;;
        -*) echo "未知参数: $1" >&2; exit 2 ;;
        *) BIN="$1" ;;
    esac
    shift
done
BIN="${BIN:-$SRC/voicebridge}"

log()  { printf '\033[1;34m[sipvoice]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[警告]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[错误]\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- 0. 前置检查 ----------
[ "$(id -u)" -eq 0 ] || die "请用 sudo 运行：sudo $0"
[ -f "$BIN" ] || die "找不到二进制：$BIN（把 voicebridge 放在脚本同目录，或用参数指定）"

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|aarch64) ;;
    *) die "不支持的架构：$ARCH" ;;
esac

if [ -f /etc/os-release ]; then
    . /etc/os-release
    case "${ID:-}" in
        ubuntu|debian) log "系统 ${PRETTY_NAME:-$ID}" ;;
        *) warn "系统 ${ID:-未知} 未经验证，继续尝试" ;;
    esac
fi

FREE_MB=$(df -Pm / | awk 'NR==2{print $4}')
[ "${FREE_MB:-0}" -ge 1024 ] || warn "根分区剩余不足 1GB（${FREE_MB}MB），可能装不下"

# ---------- 1. 依赖 ----------
NEED=""
command -v curl >/dev/null 2>&1      || NEED="$NEED curl"
[ -d /etc/ssl/certs ]                || NEED="$NEED ca-certificates"
command -v ffmpeg >/dev/null 2>&1    || NEED="$NEED ffmpeg"
command -v uuidgen >/dev/null 2>&1   || NEED="$NEED uuid-runtime"
if [ "$SKIP_ASTERISK" -eq 0 ]; then
    command -v asterisk >/dev/null 2>&1 || NEED="$NEED asterisk"
fi
if [ -n "$NEED" ]; then
    log "安装依赖：$NEED"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq || die "apt-get update 失败（检查网络或源）"
    # shellcheck disable=SC2086
    apt-get install -y -qq $NEED || die "依赖安装失败"
else
    log "依赖已就绪"
fi

# ---------- 2. 用户与目录 ----------
if ! id sipvoice >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin sipvoice
    log "已创建系统用户 sipvoice"
fi
install -d -m 755 "$APP"
install -d -m 755 "$MODELS"
install -d -m 770 -o root -g sipvoice "$ETC"
install -d -m 770 -o root -g sipvoice "$ETC/backups"
install -d -m 770 -o root -g sipvoice "$ETC/tls"
chown -R sipvoice:sipvoice "$MODELS"

# ---------- 3. 二进制 ----------
if systemctl is-active --quiet sipvoice 2>/dev/null; then
    systemctl stop sipvoice
fi
install -m 755 -o root -g root "$BIN" "$APP/voicebridge"
log "已安装二进制 $APP/voicebridge"

# ---------- 4. 配置 ----------
if [ ! -f "$ETC/config.json" ]; then
    install -m 660 -o root -g sipvoice "$SRC/config.example.json" "$ETC/config.json"
    log "已写入默认配置 $ETC/config.json（AI Key 可在面板里填）"
else
    chown root:sipvoice "$ETC/config.json" 2>/dev/null || true
    chmod 660 "$ETC/config.json" 2>/dev/null || true
    log "保留已有配置 $ETC/config.json"
fi

# ---------- 5. Asterisk ----------
if [ "$SKIP_ASTERISK" -eq 0 ]; then
    # 兜底：/etc/asterisk 被误删时 dpkg 不会自动补回默认配置
    if [ ! -f /etc/asterisk/asterisk.conf ]; then
        log "补齐 Asterisk 默认配置（asterisk-config）"
        export DEBIAN_FRONTEND=noninteractive
        apt-get install -y -qq --reinstall asterisk-config \
            || warn "asterisk-config 重装失败，Asterisk 可能起不来"
    fi
    if [ ! -f /etc/asterisk/asterisk.conf ]; then
        die "/etc/asterisk/asterisk.conf 仍缺失，请手动执行：sudo apt-get install --reinstall asterisk-config"
    fi

    STAMP=$(date +%Y%m%d-%H%M%S)
    for f in pjsip.conf extensions.conf rtp.conf; do
        if [ -f "/etc/asterisk/$f" ] && grep -q "SIPVoice" "/etc/asterisk/$f" 2>/dev/null; then
            log "Asterisk 配置已存在，跳过：$f"
            continue
        fi
        if [ -f "/etc/asterisk/$f" ]; then
            cp -a "/etc/asterisk/$f" "/etc/asterisk/$f.bak.$STAMP"
            log "已备份原配置：/etc/asterisk/$f.bak.$STAMP"
        fi
        install -m 640 -o root -g asterisk "$SRC/asterisk/$f" "/etc/asterisk/$f"
        log "已部署：$f"
    done

    # 面板需要写这两份文件（保存话机配置时）
    for f in pjsip.conf extensions.conf; do
        [ -f "/etc/asterisk/$f" ] || continue
        chgrp sipvoice "/etc/asterisk/$f" 2>/dev/null || true
        chmod 664 "/etc/asterisk/$f" 2>/dev/null || true
    done

    # Asterisk 22 首次启动偶尔不会创建 PJSIP 端点，装一个 drop-in 在启动后补一次 reload
    install -d -m 755 /etc/systemd/system/asterisk.service.d
    cat > /etc/systemd/system/asterisk.service.d/sipvoice-pjsip.conf <<'DROPIN'
[Service]
# SIPVoice：Asterisk 22 首次启动偶尔不创建 PJSIP 端点，起来后补一次 reload
ExecStartPost=/bin/sh -c 'sleep 4; /usr/sbin/asterisk -rx "pjsip reload" >/dev/null 2>&1 || true'
DROPIN
    systemctl daemon-reload

    # 传真落盘目录
    install -d -m 775 -o asterisk -g asterisk /var/spool/asterisk/fax 2>/dev/null || true

    systemctl enable asterisk >/dev/null 2>&1 || true
    systemctl restart asterisk || die "Asterisk 启动失败，看 journalctl -u asterisk"
    for _ in $(seq 1 15); do
        systemctl is-active --quiet asterisk && break
        sleep 1
    done
    systemctl is-active --quiet asterisk \
        && log "Asterisk 运行中" \
        || warn "Asterisk 未进入 active，请检查 journalctl -u asterisk"

    # 首次启动时 PJSIP 配置是异步加载的，等分机真正出现（必要时补一次 reload）
    ENDPOINTS=0
    for _ in $(seq 1 15); do
        N=$(asterisk -rx "pjsip show endpoints" 2>/dev/null | grep -c "Endpoint:" || true)
        if [ "${N:-0}" -ge 3 ]; then ENDPOINTS=1; break; fi
        asterisk -rx "pjsip reload" >/dev/null 2>&1 || true
        sleep 1
    done
    if [ "$ENDPOINTS" -eq 1 ]; then
        log "分机已加载：1001 / 1002 / calltest"
    else
        warn "分机未加载，请检查 /etc/asterisk/pjsip.conf"
    fi
fi

# ---------- 6. 权限与 systemd ----------
cat > /etc/sudoers.d/sipvoice-asterisk <<'SUDOERS'
sipvoice ALL=(root) NOPASSWD: /usr/sbin/asterisk
SUDOERS
chmod 440 /etc/sudoers.d/sipvoice-asterisk

install -m 644 "$SRC/sipvoice.service" /etc/systemd/system/sipvoice.service
systemctl daemon-reload
systemctl enable sipvoice >/dev/null 2>&1 || true
systemctl restart sipvoice

# ---------- 7. 健康检查 ----------
UP=0
for _ in $(seq 1 20); do
    if systemctl is-active --quiet sipvoice; then UP=1; break; fi
    sleep 1
done
[ "$UP" -eq 1 ] || die "sipvoice 启动失败，执行 journalctl -u sipvoice -n 50 查看原因"

PANEL=""
for _ in $(seq 1 20); do
    CODE=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 https://127.0.0.1:8080/api/status || true)
    if [ "$CODE" = "200" ] || [ "$CODE" = "401" ]; then PANEL="https://127.0.0.1:8080"; break; fi
    CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:8080/api/status || true)
    if [ "$CODE" = "200" ] || [ "$CODE" = "401" ]; then PANEL="http://127.0.0.1:8080"; break; fi
    sleep 1
done
[ -n "$PANEL" ] || warn "面板未在 8080 端口响应，检查 journalctl -u sipvoice"

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
[ -n "$IP" ] || IP="127.0.0.1"
SCHEME="https"; case "$PANEL" in http://*) SCHEME="http" ;; esac

echo
log "部署完成（版本 $("$APP/voicebridge" -version 2>/dev/null | head -n1 || echo "?"))"
cat <<EOF

  控制台   ${SCHEME}://${IP}:8080
          首次打开需要创建管理员账号（自签证书，浏览器需信任一次）
  分机     1001 / SipVoice1001、1002 / SipVoice1002
  拨号     9000 = 与 AI 通话，600 = 接收传真，1001/1002 互拨
  服务     systemctl status sipvoice
  日志     journalctl -u sipvoice -f
  升级     sudo ./upgrade.sh <新二进制>
  卸载     sudo ./uninstall.sh [--purge]

  下一步：打开控制台 → 「AI 服务」填服务商与 Key → 拨 9000 试一通。
EOF
