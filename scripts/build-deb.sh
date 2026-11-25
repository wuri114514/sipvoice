#!/usr/bin/env bash
# 构建 .deb：scripts/build-deb.sh [amd64|arm64]
# 产物：dist/release/sipvoice_<版本>_<架构>.deb
set -euo pipefail

ARCH="${1:-amd64}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="$(sed -n 's/^var version = "\(.*\)"/\1/p' "$ROOT/cmd/voicebridge/main.go")"
BIN="$ROOT/dist/voicebridge-linux-$ARCH"
OUTDIR="$ROOT/dist/release"

[ -f "$BIN" ] || { echo "缺少二进制：$BIN（先 make linux-$ARCH）" >&2; exit 1; }
mkdir -p "$OUTDIR"

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
PKG="$STAGE/sipvoice_${VERSION}_${ARCH}"

mkdir -p "$PKG/DEBIAN" \
         "$PKG/opt/sipvoice" \
         "$PKG/usr/lib/systemd/system/asterisk.service.d" \
         "$PKG/usr/share/sipvoice/asterisk" \
         "$PKG/usr/share/doc/sipvoice"

install -m 0755 "$BIN" "$PKG/opt/sipvoice/voicebridge"
install -m 0644 "$ROOT/deploy/sipvoice.service" "$PKG/usr/lib/systemd/system/sipvoice.service"
install -m 0644 "$ROOT/deploy/config.example.json" "$PKG/usr/share/sipvoice/config.example.json"
install -m 0644 "$ROOT/deploy/asterisk/pjsip.conf" "$PKG/usr/share/sipvoice/asterisk/pjsip.conf"
install -m 0644 "$ROOT/deploy/asterisk/extensions.conf" "$PKG/usr/share/sipvoice/asterisk/extensions.conf"
install -m 0644 "$ROOT/deploy/asterisk/rtp.conf" "$PKG/usr/share/sipvoice/asterisk/rtp.conf"
install -m 0644 "$ROOT/LICENSE" "$PKG/usr/share/doc/sipvoice/LICENSE"
install -m 0644 "$ROOT/THIRD_PARTY_NOTICES.md" "$PKG/usr/share/doc/sipvoice/THIRD_PARTY_NOTICES.md"
install -m 0644 "$ROOT/deploy/deb/copyright" "$PKG/usr/share/doc/sipvoice/copyright"

# Asterisk 启动后自动 reload 的 drop-in（同 install.sh）
cat > "$PKG/usr/lib/systemd/system/asterisk.service.d/sipvoice-pjsip.conf" <<'DROPIN'
[Service]
# SIPVoice：Asterisk 22 首次启动偶尔不创建 PJSIP 端点，起来后补一次 reload
ExecStartPost=/bin/sh -c 'sleep 4; /usr/sbin/asterisk -rx "pjsip reload" >/dev/null 2>&1 || true'
DROPIN
chmod 0644 "$PKG/usr/lib/systemd/system/asterisk.service.d/sipvoice-pjsip.conf"

sed -e "s/@VERSION@/$VERSION/" -e "s/@ARCH@/$ARCH/" \
    "$ROOT/deploy/deb/control.in" > "$PKG/DEBIAN/control"
chmod 0644 "$PKG/DEBIAN/control"

for s in postinst prerm postrm; do
    install -m 0755 "$ROOT/deploy/deb/$s" "$PKG/DEBIAN/$s"
done

# md5sums（Debian 规范建议）
( cd "$PKG" && find . -path ./DEBIAN -prune -o -type f -print \
    | sed 's|^\./||' | sort | xargs md5sum > DEBIAN/md5sums )
chmod 0644 "$PKG/DEBIAN/md5sums"

OUT="$OUTDIR/sipvoice_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group -Zxz "$PKG" "$OUT" >/dev/null
( cd "$OUTDIR" && sha256sum "$(basename "$OUT")" > "$(basename "$OUT").sha256" )
echo "已生成：$OUT"
dpkg-deb -I "$OUT" | sed -n '1,12p'
