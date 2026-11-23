#!/bin/bash
# Builds Kyokai's Rust core (core/kyokai-core) for Android with the NDK and
# places libkyokai_core.so into internal/androidhost/kotlin/src/main/jniLibs/<abi>/
# (16 KB page-size aligned, packed uncompressed into the APK by Gradle).
#
#   scripts/build-android-core.sh                 arm64-v8a
#   scripts/build-android-core.sh arm64-v8a x86_64
#   CHECK=1 scripts/build-android-core.sh         cargo check only
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)

abis=("$@")
[ ${#abis[@]} -eq 0 ] && abis=(arm64-v8a)

sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}"
if [ -z "$sdk" ]; then
	case "$(uname -s)" in
	MINGW* | MSYS* | CYGWIN*) sdk="$LOCALAPPDATA/Android/Sdk" ;;
	Darwin) sdk="$HOME/Library/Android/sdk" ;;
	*) sdk="$HOME/Android/Sdk" ;;
	esac
fi
ndk="${ANDROID_NDK_HOME:-$(ls -d "$sdk"/ndk/* 2>/dev/null | sort -V | tail -1)}"
[ -d "$ndk" ] || { echo "no NDK: set ANDROID_NDK_HOME" >&2; exit 1; }
host=$(ls "$ndk/toolchains/llvm/prebuilt" | head -1)
bin="$ndk/toolchains/llvm/prebuilt/$host/bin"
ext=""
[ -e "$bin/clang.exe" ] && ext=".cmd"
api=26

export PROTOC=${PROTOC:-$(scripts/protoc.sh)}

for abi in "${abis[@]}"; do
	case "$abi" in
	arm64-v8a) target=aarch64-linux-android ;;
	x86_64) target=x86_64-linux-android ;;
	*) echo "unsupported Android ABI $abi" >&2; exit 1 ;;
	esac
	rustup target add "$target" >/dev/null
	env=$(echo "$target" | tr - _)
	upper=$(echo "$env" | tr '[:lower:]' '[:upper:]')
	export "CC_$env=$bin/$target$api-clang$ext"
	export "CXX_$env=$bin/$target$api-clang++$ext"
	export "AR_$env=$bin/llvm-ar"
	export "CARGO_TARGET_${upper}_LINKER=$bin/$target$api-clang$ext"
	export "CARGO_TARGET_${upper}_RUSTFLAGS=-C link-arg=-Wl,-z,max-page-size=16384"
	if [ -n "${CHECK:-}" ]; then
		cargo check --manifest-path "$root/core/Cargo.toml" -p kyokai-core --target "$target"
	else
		cargo build --manifest-path "$root/core/Cargo.toml" -p kyokai-core --release --target "$target"
		out="$root/internal/androidhost/kotlin/src/main/jniLibs/$abi"
		mkdir -p "$out"
		cp "$root/core/target/$target/release/libkyokai_core.so" "$out/libkyokai_core.so"
	fi
done
