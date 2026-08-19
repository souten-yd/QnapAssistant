#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/x86_64/piper-compat"
LIB="$OUT/lib"
LICENSES="$OUT/licenses"

if [ "$(uname -m)" != "x86_64" ]; then
  echo "Piper QTS compatibility runtime staging requires x86_64 Linux" >&2
  exit 1
fi

rm -rf "$OUT"
mkdir -p "$LIB" "$LICENSES"

resolve_ldconfig_lib() {
  name="$1"
  ldconfig -p 2>/dev/null | awk -v n="$name" '$1 == n && $0 ~ /x86-64/ { print $NF; exit }'
}

copy_required_lib() {
  name="$1"
  src="$(resolve_ldconfig_lib "$name")"
  if [ -z "$src" ] || [ ! -e "$src" ]; then
    echo "Required compatibility library not found: $name" >&2
    exit 1
  fi
  cp -L "$src" "$LIB/$name"
}

copy_optional_lib() {
  name="$1"
  src="$(resolve_ldconfig_lib "$name")"
  if [ -n "$src" ] && [ -e "$src" ]; then
    cp -L "$src" "$LIB/$name"
  fi
}

LOADER=""
for candidate in /lib64/ld-linux-x86-64.so.2 /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2; do
  if [ -e "$candidate" ]; then
    LOADER="$(readlink -f "$candidate")"
    break
  fi
done
[ -n "$LOADER" ] || { echo "x86_64 glibc loader not found" >&2; exit 1; }
cp -L "$LOADER" "$OUT/ld-linux-x86-64.so.2"
chmod 0755 "$OUT/ld-linux-x86-64.so.2"

for lib in \
  libc.so.6 libm.so.6 libmvec.so.1 libpthread.so.0 libdl.so.2 librt.so.1 \
  libresolv.so.2 libz.so.1; do
  copy_required_lib "$lib"
done

for lib in libutil.so.1 libanl.so.1 libcrypt.so.1 libnss_files.so.2 libnss_dns.so.2 libgomp.so.1; do
  copy_optional_lib "$lib"
done

CXX_LIB="$(g++ -print-file-name=libstdc++.so.6)"
GCC_LIB="$(gcc -print-file-name=libgcc_s.so.1)"
[ -e "$CXX_LIB" ] || { echo "libstdc++.so.6 not found" >&2; exit 1; }
[ -e "$GCC_LIB" ] || { echo "libgcc_s.so.1 not found" >&2; exit 1; }
cp -L "$CXX_LIB" "$LIB/libstdc++.so.6"
cp -L "$GCC_LIB" "$LIB/libgcc_s.so.1"

if [ -f /usr/share/doc/libc6/copyright ]; then
  cp /usr/share/doc/libc6/copyright "$LICENSES/glibc-copyright.txt"
fi
if [ -f /usr/share/doc/libstdc++6/copyright ]; then
  cp /usr/share/doc/libstdc++6/copyright "$LICENSES/libstdc++-copyright.txt"
fi

GLIBC_VERSION="$(ldd --version 2>/dev/null | head -n 1 || true)"
GXX_VERSION="$(g++ --version 2>/dev/null | head -n 1 || true)"
{
  echo "QnapAssistant Piper QTS isolated compatibility runtime"
  echo "Generated from the GitHub Actions Ubuntu x86_64 build environment."
  echo "glibc=$GLIBC_VERSION"
  echo "gxx=$GXX_VERSION"
  echo "loader=ld-linux-x86-64.so.2"
  echo "purpose=Run Piper Plus without replacing or modifying the QTS system libc/libstdc++."
} > "$OUT/manifest.txt"

# Verify the private loader can run both a glibc-linked C binary and a C++17
# binary using only the staged compatibility libraries. This catches missing
# loader/libstdc++/libgcc pieces before building the QPKG.
"$OUT/ld-linux-x86-64.so.2" --library-path "$LIB" /usr/bin/true
TMP_CPP="$(mktemp /tmp/qnap-piper-compat-XXXXXX.cpp)"
TMP_BIN="${TMP_CPP%.cpp}"
trap 'rm -f "$TMP_CPP" "$TMP_BIN"' EXIT
cat > "$TMP_CPP" <<'EOF_CPP'
#include <iostream>
#include <string>
int main() { std::string s = "qnap-piper-compat"; std::cout << s << "\n"; return 0; }
EOF_CPP
g++ -std=c++17 -O2 "$TMP_CPP" -o "$TMP_BIN"
"$OUT/ld-linux-x86-64.so.2" --library-path "$LIB" "$TMP_BIN" >/dev/null

find "$OUT" -maxdepth 2 -type f -printf '%P %s bytes\n' | sort
