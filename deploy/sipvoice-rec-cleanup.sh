#!/bin/bash
# SIPVoice 录音滚动清理：目录超过上限时，从最旧的文件开始删，直到回落到 90%
# 配置：/etc/default/sipvoice-rec（DIR、LIMIT_GB、TARGET_PERCENT）
set -u

[ -r /etc/default/sipvoice-rec ] && . /etc/default/sipvoice-rec

DIR=${DIR:-/var/lib/sipvoice/recordings}
LIMIT_GB=${LIMIT_GB:-5}
TARGET_PERCENT=${TARGET_PERCENT:-90}
LOG_TAG=sipvoice-rec

[ -d "$DIR" ] || exit 0

LIMIT=$((LIMIT_GB * 1024 * 1024 * 1024))
TARGET=$((LIMIT * TARGET_PERCENT / 100))

total=$(du -sb "$DIR" 2>/dev/null | cut -f1)
total=${total:-0}
if [ "$total" -le "$LIMIT" ]; then
    exit 0
fi

logger -t "$LOG_TAG" "目录 $DIR 已用 ${total} 字节，超过 ${LIMIT_GB}G 上限，开始清理"

# 按修改时间从旧到新逐个删除
find "$DIR" -type f -printf '%T@ %s %p\n' 2>/dev/null | sort -n | while read -r _ts size path; do
    [ "$total" -le "$TARGET" ] && break
    rm -f -- "$path" && total=$((total - size))
    logger -t "$LOG_TAG" "删除 $(basename "$path")（${size} 字节）"
done

logger -t "$LOG_TAG" "清理完成，当前 $(du -sb "$DIR" 2>/dev/null | cut -f1) 字节"
