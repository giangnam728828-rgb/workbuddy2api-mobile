#!/usr/bin/env bash
# 把 Go 网关交叉编译成 Android 可执行的 ELF，按 ABI 放进 jniLibs。
#
# 为什么不编 GOOS=android：Go 的 android 端口必须带 cgo（依赖 NDK），而我们这套服务
# 是纯 Go（CGO_ENABLED=0）。Android 内核就是 Linux，linux/arm64 静态二进制可以直接跑。
#
# 为什么名字叫 lib*.so：Android 10+ 禁止 exec 应用数据目录里的文件，只有打包进
# jniLibs 的 .so 会被解压到 nativeLibraryDir（可执行）。文件名必须满足 lib*.so 才会被
# 当成原生库打包，所以这里借用命名规则，内容是普通 ELF 可执行文件。
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
out="$here/app/src/main/jniLibs"
mkdir -p "$out/arm64-v8a" "$out/armeabi-v7a" "$out/x86_64"

cd "$root"
ldflags="-s -w"

build() { # $1=GOARCH $2=GOARM(可空) $3=dest
  local arch="$1" arm="$2" dest="$3"
  echo "→ $arch${arm:+/arm$arm}  $dest"
  if [ -n "$arm" ]; then
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOARM="$arm" \
      go build -trimpath -ldflags="$ldflags" -o "$dest" ./cmd/server
  else
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -ldflags="$ldflags" -o "$dest" ./cmd/server
  fi
}

build arm64 ""      "$out/arm64-v8a/libwb2api.so"
build arm   7       "$out/armeabi-v7a/libwb2api.so"
build amd64 ""      "$out/x86_64/libwb2api.so"

ls -lh "$out"/*/libwb2api.so
