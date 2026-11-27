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
command -v tiffinfo >/dev/null 2>&1  || NEED="$NEED libtiff-tools"
command -v convert >/dev/null 2>&1   || NEED="$NEED imagemagick"
command -v paps >/dev/null 2>&1      || NEED="$NEED paps"
command -v ps2pdf >/dev/null 2>&1    || NEED="$NEED ghostscript"
# 中文字体（文本转传真、PDF 渲染）
fc-list 2>/dev/null | grep -qi "Noto Sans CJK" || NEED="$NEED fonts-noto-cjk"
# sox 读 mp3（edge-tts 输出格式）
sox --help 2>/dev/null | head -1 >/dev/null && ! sox -h 2>/dev/null | grep -qi mpeg \
    && NEED="$NEED libsox-fmt-mp3"
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
# 面板需要读 Asterisk 写的录音与传真文件（属于 asterisk 组）
if getent group asterisk >/dev/null 2>&1; then
    usermod -aG asterisk sipvoice 2>/dev/null || true
    # /var/spool/asterisk 的组若是 root，sipvoice 即使加入了 asterisk 组也进不去
    # （缺 x 位会导致面板读传真时报 permission denied）
    if [ -d /var/spool/asterisk ]; then
        if [ "$(stat -c '%G' /var/spool/asterisk)" != "asterisk" ]; then
            chgrp asterisk /var/spool/asterisk 2>/dev/null || true
        fi
        chmod g+rx /var/spool/asterisk 2>/dev/null || true
    fi
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
# 先确定录音/传真目录（后面写配置和部署服务单元都要用）
if mountpoint -q /data 2>/dev/null; then
    REC_DIR=/data/recordings
    FAX_DIR=/data/fax
else
    REC_DIR=/var/lib/sipvoice/recordings
    FAX_DIR=/var/lib/sipvoice/fax
fi

if [ ! -f "$ETC/config.json" ]; then
    install -m 660 -o root -g sipvoice "$SRC/config.example.json" "$ETC/config.json"
    # 把实际录音目录写进 storage 块，避免控制台按 /data/recordings 的默认值显示
    python3 - "$ETC/config.json" "$REC_DIR" <<'PYEOF'
import json, sys
path, rec = sys.argv[1], sys.argv[2]
cfg = json.load(open(path, encoding='utf-8'))
cfg.setdefault('storage', {})
cfg['storage'].setdefault('recording_enabled', True)
cfg['storage']['recording_dir'] = rec
cfg['storage'].setdefault('recording_quota_gb', 5)
cfg['storage'].setdefault('log_max_mb', 50)
cfg['storage'].setdefault('log_keep', 5)
json.dump(cfg, open(path, 'w', encoding='utf-8'), ensure_ascii=False, indent=2)
PYEOF
    log "已写入默认配置 $ETC/config.json（录音目录 ${REC_DIR}，AI Key 可在面板里填）"
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

    # transport 单独成文件：控制台保存话机会重新生成 pjsip.conf，
    # 把 NAT/公网地址集中在这里才不会被覆盖
    install -d -m 755 /etc/asterisk/pjsip.d
    if [ ! -f /etc/asterisk/pjsip.d/transport.conf ]; then
        PUBLIC_IP="${PUBLIC_IP:-$(curl -4 -s --max-time 6 https://api.ipify.org 2>/dev/null || true)}"
        {
            echo "; SIPVoice - PJSIP 传输层（含公网地址），由 install.sh 生成"
            echo "[transport-udp]"
            echo "type=transport"
            echo "protocol=udp"
            echo "bind=0.0.0.0:5060"
            echo "allow_reload=yes"
            if command -v ip >/dev/null 2>&1; then
                ip -o -4 addr show scope global 2>/dev/null | awk '{print $4}' | while read -r cidr; do
                    [ -n "$cidr" ] && echo "local_net=$cidr"
                done
            fi
            if [ -n "${PUBLIC_IP:-}" ]; then
                if ! ip -4 addr show 2>/dev/null | grep -q "inet ${PUBLIC_IP}/"; then
                    echo "external_signaling_address=$PUBLIC_IP"
                    echo "external_media_address=$PUBLIC_IP"
                    log "已写入公网地址：$PUBLIC_IP（1:1 NAT）"
                fi
            fi
        } > /etc/asterisk/pjsip.d/transport.conf
        chmod 640 /etc/asterisk/pjsip.d/transport.conf
        chown root:asterisk /etc/asterisk/pjsip.d/transport.conf 2>/dev/null || true
    fi

    # 老版本 pjsip.conf 里内联的 [transport-udp] 需要迁移掉，否则和 include 冲突
    if [ -f /etc/asterisk/pjsip.conf ] && ! grep -q "pjsip.d/transport.conf" /etc/asterisk/pjsip.conf; then
        if grep -q "^\[transport-udp\]" /etc/asterisk/pjsip.conf; then
            cp -a /etc/asterisk/pjsip.conf "/etc/asterisk/pjsip.conf.bak.$STAMP"
            awk '
                /^\[transport-udp\]/ { skip=1; next }
                skip && /^\[/ { skip=0 }
                skip { next }
                { print }
            ' "/etc/asterisk/pjsip.conf.bak.$STAMP" > /tmp/pjsip.migrated
            { echo "#include pjsip.d/transport.conf"; echo; cat /tmp/pjsip.migrated; } > /etc/asterisk/pjsip.conf
            rm -f /tmp/pjsip.migrated
            log "已把内联 transport 迁移到 pjsip.d/transport.conf"
        fi
    fi

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
    # 传真发送/失败归档/提示音目录
    install -d -m 755 -o sipvoice -g sipvoice /var/lib/sipvoice/prompts /var/lib/sipvoice/fax-failed
    # T.38 走 UDPTL，放行对应端口
    ufw allow 4000:4999/udp comment 'T.38 UDPTL' >/dev/null 2>&1 || true

    # ---------- 5.1 存储与日志保留 ----------
    # REC_DIR / FAX_DIR 已在第 4 步按是否有独立数据盘算好
    install -d -m 750 -o asterisk -g asterisk "$REC_DIR" "$REC_DIR/monitor" "$FAX_DIR"
    # 让面板（sipvoice 用户）能读这些目录：组写权限不需要，读+进入即可
    chgrp -R asterisk "$REC_DIR" "$FAX_DIR" 2>/dev/null || true
    chmod -R g+rX "$REC_DIR" "$FAX_DIR" 2>/dev/null || true
    log "录音目录：$REC_DIR（上限 ${REC_LIMIT_GB:-5}G，滚动覆盖）"

    # 把 Asterisk 的 monitor / fax 目录绑到存储目录（重启后由 fstab 兜底）
    for pair in "/var/spool/asterisk/monitor:$REC_DIR/monitor" "/var/spool/asterisk/fax:$FAX_DIR"; do
        src=${pair%%:*}; dst=${pair##*:}
        install -d -m 750 -o asterisk -g asterisk "$src" "$dst"
        if ! mountpoint -q "$src"; then
            mount --bind "$dst" "$src" 2>/dev/null || warn "绑定挂载失败：$dst -> $src"
        fi
        if ! grep -q " $src " /etc/fstab 2>/dev/null; then
            printf '%s  %s  none  bind,nofail  0  0\n' "$dst" "$src" >> /etc/fstab
        fi
    done

    # 滚动清理：每 5 分钟检查一次，超上限删最旧的
    install -m 755 "$SRC/sipvoice-rec-cleanup.sh" /usr/local/sbin/sipvoice-rec-cleanup.sh
    install -m 755 "$SRC/sipvoice-storage-apply.sh" /usr/local/sbin/sipvoice-storage-apply.sh
    install -m 644 "$SRC/systemd/sipvoice-rec-cleanup.service" /etc/systemd/system/
    install -m 644 "$SRC/systemd/sipvoice-rec-cleanup.timer" /etc/systemd/system/
    cat > /etc/default/sipvoice-rec <<EOF
# SIPVoice 录音保留策略
DIR=$REC_DIR
LIMIT_GB=${REC_LIMIT_GB:-5}
TARGET_PERCENT=90
REC=1
EOF
    systemctl daemon-reload
    systemctl enable --now sipvoice-rec-cleanup.timer >/dev/null 2>&1 || true
    log "录音清理定时器已启用（每 5 分钟）"

    # 面板可改录音开关/路径/额度与日志策略，初始化 AstDB 供拨号方案读取
    asterisk -rx "database put sipvoice rec 1" >/dev/null 2>&1 || true
    asterisk -rx "database put sipvoice rec_dir $REC_DIR/monitor" >/dev/null 2>&1 || true

    # 日志按大小轮转，防止 messages.log 无限增长
    install -m 644 "$SRC/logrotate-asterisk" /etc/logrotate.d/asterisk
    log "Asterisk 日志轮转已配置（超过 50M 切分，保留 5 份）"

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
sipvoice ALL=(root) NOPASSWD: /usr/local/sbin/sipvoice-storage-apply.sh
SUDOERS
chmod 440 /etc/sudoers.d/sipvoice-asterisk

install -m 644 "$SRC/sipvoice.service" /etc/systemd/system/sipvoice.service
# ReadWritePaths 中的路径必须真实存在，否则 systemd 以 226/NAMESPACE 拒绝启动
# （典型场景：没有独立数据盘、/data 不存在）。这里按实际录音目录重写整行。
if [ -n "${REC_DIR:-}" ] && [ -d "$REC_DIR" ]; then
    awk -v extra="$REC_DIR" '
        /^ReadWritePaths=/ {
            if (index($0, extra) == 0) { print $0 " " extra }
            else { print $0 }
            next
        }
        { print }
    ' "$SRC/sipvoice.service" > /etc/systemd/system/sipvoice.service
    chmod 644 /etc/systemd/system/sipvoice.service
    log "服务可写路径已包含 ${REC_DIR}"
fi
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
