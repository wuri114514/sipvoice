#!/bin/bash
# 容器入口：准备数据目录 → 启动 Asterisk → 启动 voicebridge → 转发停止信号
set -euo pipefail

ETC=/etc/sipvoice
APP=/opt/sipvoice
WEB=${SIPVOICE_WEB:-0.0.0.0:8080}

log() { printf '\033[1;34m[sipvoice]\033[0m %s\n' "$*"; }

# ---------- 数据目录 ----------
mkdir -p "$ETC/tls" "$ETC/backups" "$APP/models" /run/asterisk /var/log/asterisk
chown -R sipvoice:sipvoice "$ETC" "$APP/models"
chown asterisk:asterisk /run/asterisk /var/log/asterisk 2>/dev/null || true

if [ ! -f "$ETC/config.json" ]; then
    cp /usr/share/sipvoice/config.example.json "$ETC/config.json"
    chown sipvoice:sipvoice "$ETC/config.json"
    log "首次启动：已生成默认配置 $ETC/config.json（请在控制台填服务商 Key）"
fi

# 面板保存话机配置时需要写这两份文件
chgrp sipvoice /etc/asterisk/pjsip.conf /etc/asterisk/extensions.conf 2>/dev/null || true
chmod 664 /etc/asterisk/pjsip.conf /etc/asterisk/extensions.conf 2>/dev/null || true

# ---------- Asterisk ----------
log "启动 Asterisk"
/usr/sbin/asterisk -f -U asterisk -G asterisk -p >/var/log/asterisk/console.log 2>&1 &
AST_PID=$!

for _ in $(seq 1 30); do
    if /usr/sbin/asterisk -rx "core show uptime" >/dev/null 2>&1; then break; fi
    if ! kill -0 "$AST_PID" 2>/dev/null; then
        log "Asterisk 启动失败，日志："
        tail -20 /var/log/asterisk/console.log || true
        exit 1
    fi
    sleep 1
done
# 首次启动 PJSIP 配置是异步加载的，补一次 reload 确保分机可用
sleep 1
/usr/sbin/asterisk -rx "pjsip reload" >/dev/null 2>&1 || true

# ---------- voicebridge ----------
log "启动 voicebridge（控制台 $WEB）"
setpriv --reuid=sipvoice --regid=sipvoice --init-groups \
    "$APP/voicebridge" -mode agent -config "$ETC/config.json" -web "$WEB" &
VB_PID=$!

shutdown() {
    log "收到停止信号，正在退出"
    kill -TERM "$VB_PID" 2>/dev/null || true
    /usr/sbin/asterisk -rx "core stop now" >/dev/null 2>&1 || true
    sleep 1
    kill -KILL "$VB_PID" 2>/dev/null || true
    exit 0
}
trap shutdown TERM INT

# 任一进程退出即结束容器（便于 --restart 拉起）
wait -n "$AST_PID" "$VB_PID"
