#!/usr/bin/env bash
set -euo pipefail

LLAMA_CPP_COMMIT="${LLAMA_CPP_COMMIT:-6d05498314db1b57f81c271080018aa2d0b89be9}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/.build/llama.cpp"
BUILD="$SRC/build"
DEST="$ROOT/x86_64/bin"

rm -rf "$SRC"
mkdir -p "$ROOT/.build" "$DEST"
git clone --filter=blob:none https://github.com/ggml-org/llama.cpp.git "$SRC"
git -C "$SRC" checkout --detach "$LLAMA_CPP_COMMIT"

# Local patches for the SSE4.2-only target (TS-253Be / Celeron J3455, no AVX).
# 0001 adds SSE4.1/SSSE3 vec_dot paths for Q4_K and Q6_K, which otherwise fall back to scalar code.
for patch in "$ROOT"/scripts/llama-patches/*.patch; do
  [ -e "$patch" ] || continue
  git -C "$SRC" apply --check "$patch"
  git -C "$SRC" apply "$patch"
  echo "Applied $(basename "$patch")"
done

cmake -S "$SRC" -B "$BUILD" -G Ninja \
  -DCMAKE_BUILD_TYPE=Release \
  -DBUILD_SHARED_LIBS=OFF \
  -DCMAKE_EXE_LINKER_FLAGS="-static" \
  -DGGML_STATIC=ON \
  -DGGML_NATIVE=OFF \
  -DGGML_SSE42=ON \
  -DGGML_AVX=OFF \
  -DGGML_AVX_VNNI=OFF \
  -DGGML_AVX2=OFF \
  -DGGML_BMI2=OFF \
  -DGGML_AVX512=OFF \
  -DGGML_AVX512_VBMI=OFF \
  -DGGML_AVX512_VNNI=OFF \
  -DGGML_AVX512_BF16=OFF \
  -DGGML_FMA=OFF \
  -DGGML_F16C=OFF \
  -DGGML_OPENMP=OFF \
  -DGGML_BLAS=OFF \
  -DGGML_LLAMAFILE=OFF \
  -DGGML_CUDA=OFF \
  -DGGML_HIP=OFF \
  -DGGML_VULKAN=OFF \
  -DLLAMA_CURL=OFF \
  -DLLAMA_OPENSSL=OFF \
  -DLLAMA_BUILD_TESTS=ON \
  -DLLAMA_BUILD_EXAMPLES=OFF

cmake --build "$BUILD" --target llama-server llama-bench test-quantize-fns -j"$(nproc)"
# Correctness of the patched quantized dot products (compares against the reference implementation).
"$BUILD/bin/test-quantize-fns"
cp "$BUILD/bin/llama-server" "$DEST/llama-server"
cp "$BUILD/bin/llama-bench" "$DEST/llama-bench"
chmod 0755 "$DEST/llama-server" "$DEST/llama-bench"

file "$DEST/llama-server" "$DEST/llama-bench"
