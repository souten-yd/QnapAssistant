#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BOOTSTRAP_DIR="${1:-$ROOT/.bootstrap}"
QPKG=$(find "$BOOTSTRAP_DIR" -type f -name '*.qpkg' | head -n 1)

if [ -z "$QPKG" ]; then
  echo "No baseline QPKG artifact found in $BOOTSTRAP_DIR" >&2
  exit 1
fi

EXTRACT="$ROOT/.bootstrap-extracted"
rm -rf "$EXTRACT"
mkdir -p "$EXTRACT" "$ROOT/x86_64/bin"

# QDK QPKGs are self-extracting shell archives. Parse the generated header to
# locate the architecture data archive directly, avoiding a QDK install/build.
SCRIPT_LEN=$(grep -a -m1 '^script_len=' "$QPKG" | sed 's/^script_len=//')
CONTROL_PAD=$(grep -a -m1 '^offset=.*\$script_len + ' "$QPKG" | sed -n 's/.*\$script_len + \([0-9][0-9]*\)).*/\1/p')
DATA_SIZE=$(grep -a -m1 '^offset=.*\$offset + ' "$QPKG" | sed -n 's/.*\$offset + \([0-9][0-9]*\)).*/\1/p')

if ! [[ "$SCRIPT_LEN" =~ ^[0-9]+$ && "$CONTROL_PAD" =~ ^[0-9]+$ && "$DATA_SIZE" =~ ^[0-9]+$ ]]; then
  echo "Could not parse QPKG payload offsets" >&2
  exit 1
fi

DATA_OFFSET=$((SCRIPT_LEN + CONTROL_PAD))
tail -c +$((DATA_OFFSET + 1)) "$QPKG" | head -c "$DATA_SIZE" > "$EXTRACT/data.tar.xz"
tar -xJf "$EXTRACT/data.tar.xz" -C "$EXTRACT"

cp "$EXTRACT/bin/llama-server" "$ROOT/x86_64/bin/llama-server"
cp "$EXTRACT/bin/llama-bench" "$ROOT/x86_64/bin/llama-bench"
chmod 0755 "$ROOT/x86_64/bin/llama-server" "$ROOT/x86_64/bin/llama-bench"
rm -rf "$EXTRACT"
echo "Reused llama binaries from baseline QPKG: $QPKG"
