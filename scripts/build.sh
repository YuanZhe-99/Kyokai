#!/bin/bash
# Builds Kyokai's packages with `mygo build`.
#
#   scripts/build.sh                     this machine's platform
#   scripts/build.sh windows/amd64 windows/arm64 ...
#
# For each target it builds the Rust core (cargo, --release) and the
# engine (kyokai-svc, CGO_ENABLED=0) into resources/<goos>-<goarch>/bin,
# which `mygo build` ships with the app, and copies the license texts into
# resources/licenses. macOS targets need a macOS host; Windows targets need
# Windows (the core links with MSVC).
#
# CORE_PREBUILT=1 skips cargo and uses the core libraries already present in
# resources/<goos>-<goarch>/bin (the release workflow builds them on native
# runners and packages Windows on Ubuntu). MYGO_SIGN passes an identity to
# `mygo build -sign` (macOS release builds).
set -euo pipefail
cd "$(dirname "$0")/.."

targets=("$@")
if [ ${#targets[@]} -eq 0 ]; then
	targets=("$(go env GOOS)/$(go env GOARCH)")
fi

rust_target() {
	case "$1" in
	windows/amd64) echo x86_64-pc-windows-msvc ;;
	windows/arm64) echo aarch64-pc-windows-msvc ;;
	darwin/amd64) echo x86_64-apple-darwin ;;
	darwin/arm64) echo aarch64-apple-darwin ;;
	*) echo "unsupported target $1" >&2; exit 1 ;;
	esac
}

mkdir -p resources/licenses
cp LICENSE THIRD_PARTY_NOTICES.md third_party/licenses/GPL-3.0.txt resources/licenses/
# Every Rust crate in the core with its license text (EasyTier's LGPL-3.0
# among them; LGPL-3.0 also asks for the GPL-3.0 text, copied above).
command -v cargo-about >/dev/null || { echo "cargo-about is missing: cargo install cargo-about --locked" >&2; exit 1; }
cargo about generate --manifest-path core/Cargo.toml -c core/about.toml -o resources/licenses/RUST_LICENSES.txt core/about.hbs

for t in "${targets[@]}"; do
	goos=${t%/*}
	goarch=${t#*/}
	bin="resources/$goos-$goarch/bin"
	mkdir -p "$bin"
	case "$goos" in
	windows) lib=kyokai_core.dll ext=.exe ;;
	darwin) lib=libkyokai_core.dylib ext="" ;;
	esac
	if [ -z "${CORE_PREBUILT:-}" ]; then
		rt=$(rust_target "$t")
		rustup target add "$rt" >/dev/null
		if [ "$goos" = darwin ]; then
			export MACOSX_DEPLOYMENT_TARGET=14.0
		fi
		PROTOC=${PROTOC:-$(scripts/protoc.sh)} cargo build --release --manifest-path core/Cargo.toml --target "$rt"
		cp "core/target/$rt/release/$lib" "$bin/$lib"
	fi
	test -f "$bin/$lib" || { echo "missing $bin/$lib" >&2; exit 1; }
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" -o "$bin/kyokai-svc$ext" ./cmd/kyokai-svc
	# Release builds sign the helper's files, so an installed helper updates
	# itself without a prompt (doc/en-us/run-modes.md, "Updates").
	if [ "$goos" = windows ] && [ -n "${KYOKAI_SIGNING_KEY:-}" ]; then
		go run ./tools/sign bundle -dir "$bin" -version "$(sed -n 's/^const Version = "(.*)"//p' internal/version/version.go)"
	fi
done

sign=()
if [ -n "${MYGO_SIGN:-}" ]; then
	sign=(-sign "$MYGO_SIGN")
fi
go tool mygo build ${sign[@]+"${sign[@]}"} -platform "$(IFS=,; echo "${targets[*]}")"
