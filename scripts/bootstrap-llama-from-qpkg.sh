#!/usr/bin/env bash
set -euo pipefail

QDK_COMMIT="${QDK_COMMIT:-78c77b898fecba8f860b630177248b5fa3f5baaf}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BOOTSTRAP_DIR="${1:-$ROOT/.bootstrap}"
QPKG=$(find "$BOOTSTRAP_DIR" -type f -name '*.qpkg' | head -n 1)

if [ -z "$QPKG" ]; then
  echo "No baseline QPKG artifact found in $BOOTSTRAP_DIR" >&2
  exit 1
fi

QDK="$ROOT/.qdk-bootstrap/QDK"
EXTRACT="$ROOT/.bootstrap-extracted"
rm -rf "$ROOT/.qdk-bootstrap" "$EXTRACT"
mkdir -p "$ROOT/.qdk-bootstrap" "$EXTRACT" "$ROOT/x86_64/bin"

git clone --quiet --filter=blob:none https://github.com/qnap-dev/QDK.git "$QDK"
git -C "$QDK" checkout --quiet --detach "$QDK_COMMIT"
cat > "$QDK/shared/qdk.conf" <<EOF
QDK_VERSION=2.5.3
QDK_PATH="$QDK/shared"
EOF
chmod +x "$QDK/shared/bin/qbuild"

QDK_PATH="$QDK/shared" "$QDK/shared/bin/qbuild" --extract "$QPKG" "$EXTRACT"

cp "$EXTRACT/bin/llama-server" "$ROOT/x86_64/bin/llama-server"
cp "$EXTRACT/bin/llama-bench" "$ROOT/x86_64/bin/llama-bench"
chmod 0755 "$ROOT/x86_64/bin/llama-server" "$ROOT/x86_64/bin/llama-bench"

rm -rf "$ROOT/.qdk-bootstrap" "$EXTRACT"
echo "Reused llama binaries from baseline QPKG: $QPKG"
