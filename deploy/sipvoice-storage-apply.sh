#!/bin/bash
# SIPVoice 存储策略应用（仅 root）：由面板通过 sudo 调用
#   --rec 0|1 --dir /path --quota 5 --logmax 50 --logkeep 5
#   --clean     立即执行录音清理
#   --rotate    立即轮转 Asterisk 日志
set -u

DEFAULTS=/etc/default/sipvoice-rec
LOGROTATE=/etc/logrotate.d/asterisk

rec=""
dir=""
quota=""
logmax=""
logkeep=""
do_clean=0
do_rotate=0

while [ $# -gt 0 ]; do
    case "$1" in
        --rec) rec="$2"; shift 2 ;;
        --dir) dir="$2"; shift 2 ;;
        --quota) quota="$2"; shift 2 ;;
        --logmax) logmax="$2"; shift 2 ;;
        --logkeep) logkeep="$2"; shift 2 ;;
        --clean) do_clean=1; shift ;;
        --rotate) do_rotate=1; shift ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done

# 读取现有值做默认
[ -r "$DEFAULTS" ] && . "$DEFAULTS"
DIR=${dir:-${DIR:-/data/recordings}}
LIMIT_GB=${quota:-${LIMIT_GB:-5}}
LOG_MAX_MB=${logmax:-50}
LOG_KEEP=${logkeep:-5}
REC=${rec:-${REC:-1}}

# 目录必须存在且属于 asterisk
mkdir -p "$DIR/monitor" 2>/dev/null || true
chown -R asterisk:asterisk "$DIR" 2>/dev/null || true
chmod 750 "$DIR" "$DIR/monitor" 2>/dev/null || true

cat > "$DEFAULTS" <<EOF
# SIPVoice 录音保留策略（由面板生成）
DIR=$DIR
LIMIT_GB=$LIMIT_GB
TARGET_PERCENT=90
REC=$REC
EOF

cat > "$LOGROTATE" <<EOF
# SIPVoice：Asterisk 日志按大小轮转（由面板生成）
/var/log/asterisk/messages.log
/var/log/asterisk/full
/var/log/asterisk/debug
/var/log/asterisk/queue_log
/var/log/asterisk/security_log
{
    daily
    size ${LOG_MAX_MB}M
    rotate ${LOG_KEEP}
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    su asterisk asterisk
    create 0640 asterisk asterisk
}
EOF
chmod 644 "$LOGROTATE"

# 拨号方案通过 AstDB 读取这两个键
/usr/sbin/asterisk -rx "database put sipvoice rec $REC" >/dev/null 2>&1 || true
/usr/sbin/asterisk -rx "database put sipvoice rec_dir $DIR/monitor" >/dev/null 2>&1 || true

[ "$do_clean" -eq 1 ] && /usr/local/sbin/sipvoice-rec-cleanup.sh
[ "$do_rotate" -eq 1 ] && /usr/sbin/logrotate -f /etc/logrotate.d/asterisk >/dev/null 2>&1

echo "ok rec=$REC dir=$DIR quota=${LIMIT_GB}G log=${LOG_MAX_MB}M*${LOG_KEEP}"
