#!/usr/bin/env bash
# 把 deploy/asterisk 下的配置同步到 /etc/asterisk 并重启 Asterisk。
# 用法：sudo bash deploy.sh
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
DST=/etc/asterisk
STAMP="$(date +%Y%m%d-%H%M%S)"

for f in pjsip.conf extensions.conf rtp.conf; do
    if [ -f "$DST/$f" ]; then
        cp -a "$DST/$f" "$DST/$f.bak.$STAMP"
        echo "backup: $DST/$f.bak.$STAMP"
    fi
    install -m 640 -o root -g asterisk "$SRC/$f" "$DST/$f"
    echo "installed: $f"
done

systemctl restart asterisk
sleep 1.5
systemctl is-active asterisk
asterisk -rx "pjsip show transports" | tail -n 5
asterisk -rx "dialplan show internal" | tail -n 20
