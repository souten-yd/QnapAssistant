#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VER="${SHERPA_ONNX_VERSION:-1.13.4}"
BINDING_COMMIT="${SHERPA_GO_LINUX_COMMIT:-a424c836679c18e71d7afcab293ea5ecf539044f}"
DEPS="$ROOT/.voice-deps"
STATIC="$DEPS/sherpa-static"
BINDING="$DEPS/sherpa-go-linux"
ARCHIVE="sherpa-onnx-v${VER}-linux-x64-static-lib.tar.bz2"
URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/v${VER}/${ARCHIVE}"
CHECKSUM_URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/v${VER}/checksum.txt"

mkdir -p "$DEPS" "$ROOT/x86_64/bin"

if ! find "$STATIC" -type f -name 'libsherpa-onnx-c-api.a' -print -quit 2>/dev/null | grep -q .; then
  rm -rf "$STATIC" "$DEPS/$ARCHIVE"
  mkdir -p "$STATIC"
  curl -fL --retry 3 -o "$DEPS/$ARCHIVE" "$URL"
  if curl -fsL --retry 3 -o "$DEPS/checksum.txt" "$CHECKSUM_URL"; then
    expected=$(awk -v f="$ARCHIVE" '$0 ~ f {print $1; exit}' "$DEPS/checksum.txt")
    if [ -n "$expected" ]; then
      actual=$(sha256sum "$DEPS/$ARCHIVE" | awk '{print $1}')
      [ "$actual" = "$expected" ] || { echo "sherpa static archive checksum mismatch" >&2; exit 1; }
    else
      echo "Warning: $ARCHIVE not found in upstream checksum.txt" >&2
    fi
  fi
  tar -xjf "$DEPS/$ARCHIVE" -C "$STATIC"
fi

if [ ! -d "$BINDING/.git" ]; then
  rm -rf "$BINDING"
  git clone --filter=blob:none --no-checkout https://github.com/k2-fsa/sherpa-onnx-go-linux.git "$BINDING"
fi
if [ "$(git -C "$BINDING" rev-parse HEAD 2>/dev/null || true)" != "$BINDING_COMMIT" ]; then
  git -C "$BINDING" fetch --depth=1 origin "$BINDING_COMMIT"
  git -C "$BINDING" checkout -f "$BINDING_COMMIT"
fi

LIBDIR="$BINDING/lib/x86_64-unknown-linux-gnu"
rm -rf "$LIBDIR"
mkdir -p "$LIBDIR"
mapfile -t archives < <(find "$STATIC" -type f -name '*.a' | sort)
[ "${#archives[@]}" -gt 0 ] || { echo "No static sherpa libraries found" >&2; exit 1; }

declare -A seen
flags=()
for src in "${archives[@]}"; do
  name="$(basename "$src")"
  if [ -n "${seen[$name]:-}" ]; then
    echo "Duplicate static archive basename: $name" >&2
    exit 1
  fi
  seen[$name]=1
  cp "$src" "$LIBDIR/$name"
  flags+=("\${SRCDIR}/lib/x86_64-unknown-linux-gnu/$name")
done

cat > "$BINDING/build_linux_amd64.go" <<EOF_GO
//go:build !android && linux && amd64 && !musl

package sherpa_onnx

// #cgo LDFLAGS: -Wl,--start-group ${flags[*]} -Wl,--end-group -lstdc++ -lm -lpthread -ldl -lrt
import "C"
EOF_GO

(
  cd "$ROOT/voiceworker"
  CGO_ENABLED=1 CGO_LDFLAGS_ALLOW='.*' \
    go build -tags 'netgo osusergo' -trimpath \
      -ldflags='-s -w -linkmode external -extldflags -static' \
      -o "$ROOT/x86_64/bin/qnap-voice-worker" .
)

file "$ROOT/x86_64/bin/qnap-voice-worker"
if ldd "$ROOT/x86_64/bin/qnap-voice-worker" 2>&1 | grep -vqE 'not a dynamic executable|statically linked'; then
  echo "qnap-voice-worker is unexpectedly dynamically linked" >&2
  exit 1
fi
