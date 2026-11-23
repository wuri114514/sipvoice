#!/usr/bin/env bash
# 组装单个架构的发布包：dist/release/sipvoice-<版本>-linux-<架构>.tar.gz
# 用法：VERSION=1.3.0 ./scripts/package.sh amd64
set -euo pipefail

ARCH="${1:?用法: package.sh <amd64|arm64>}"
VERSION="${VERSION:-$(sed -n 's/^var version = "\(.*\)"/\1/p' cmd/voicebridge/main.go)}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/dist/voicebridge-linux-$ARCH"
STAGE="$ROOT/dist/stage"
PKG="$STAGE/sipvoice-$VERSION"

if [ ! -f "$BIN" ]; then
    echo "找不到二进制：$BIN（先执行 make linux-$ARCH）" >&2
    exit 1
fi

rm -rf "$PKG"
mkdir -p "$PKG"
install -m 755 "$BIN" "$PKG/voicebridge"
install -m 644 "$ROOT/deploy/install.sh" "$PKG/install.sh"
install -m 644 "$ROOT/deploy/upgrade.sh" "$PKG/upgrade.sh"
install -m 644 "$ROOT/deploy/uninstall.sh" "$PKG/uninstall.sh"
install -m 644 "$ROOT/deploy/sipvoice.service" "$PKG/sipvoice.service"
install -m 644 "$ROOT/deploy/config.example.json" "$PKG/config.example.json"
mkdir -p "$PKG/asterisk"
install -m 644 "$ROOT/deploy/asterisk/"*.conf "$PKG/asterisk/"

cat > "$PKG/README.txt" <<EOF
SIPVoice $VERSION (linux-$ARCH)

安装（Ubuntu / Debian）：
    sudo bash install.sh

脚本会安装 asterisk/ffmpeg/curl，创建用户与目录，部署配置并启动服务，
最后打印控制台地址。首次打开控制台需要创建管理员账号。

常用：
    sudo bash upgrade.sh /path/to/新voicebridge    # 升级（失败自动回滚）
    sudo bash upgrade.sh --rollback                 # 回滚
    sudo bash uninstall.sh [--purge]                # 卸载
EOF

mkdir -p "$ROOT/dist/release"
OUT="$ROOT/dist/release/sipvoice-$VERSION-linux-$ARCH.tar.gz"
tar -czf "$OUT" -C "$STAGE" "sipvoice-$VERSION"
(cd "$ROOT/dist/release" && sha256sum "sipvoice-$VERSION-linux-$ARCH.tar.gz" > "sipvoice-$VERSION-linux-$ARCH.tar.gz.sha256")
echo "已生成：$OUT"
