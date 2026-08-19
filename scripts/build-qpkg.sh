#!/usr/bin/env bash
set -euo pipefail

QDK_COMMIT="${QDK_COMMIT:-78c77b898fecba8f860b630177248b5fa3f5baaf}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
QDK="$ROOT/.qdk/QDK"
DIST="$ROOT/dist"

rm -rf "$ROOT/.qdk" "$DIST" "$ROOT/build"
mkdir -p "$ROOT/.qdk" "$DIST"

git clone --filter=blob:none https://github.com/qnap-dev/QDK.git "$QDK"
git -C "$QDK" checkout --detach "$QDK_COMMIT"

# QDK's source-tree qdk.conf assumes its repository itself is installed as
# a directory named QDK. In CI we run qbuild directly from shared/bin, so
# replace that development default with the actual runtime path.
cat > "$QDK/shared/qdk.conf" <<EOF
QDK_VERSION=2.5.3
QDK_PATH="$QDK/shared"
EOF

# qbuild uses qpkg_encrypt when it is not running on a QNAP NAS.
make -C "$QDK/src"
export PATH="$QDK/src/bin:$PATH"

chmod +x "$ROOT/shared/start-stop.sh" "$ROOT/shared/download-model.sh"
chmod +x "$QDK/shared/bin/qbuild"

QDK_PATH="$QDK/shared" \
  "$QDK/shared/bin/qbuild" \
  --root "$ROOT" \
  --build-dir "$ROOT/build" \
  --xz amd64

find "$ROOT/build" -maxdepth 2 -type f -name '*.qpkg' -print -exec cp {} "$DIST/" \;
QPKG=$(find "$DIST" -maxdepth 1 -type f -name '*.qpkg' | head -n 1)
if [ -z "$QPKG" ]; then
  echo "QPKG was not produced" >&2
  exit 1
fi

sha256sum "$QPKG" > "$QPKG.sha256"
ls -lh "$QPKG" "$QPKG.sha256"
