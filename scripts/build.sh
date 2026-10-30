#!/bin/bash
# Builds Kyokai's packages with `mygo build`.
#
#   scripts/build.sh                     this machine's platform
#   scripts/build.sh windows/amd64 windows/arm64 ...
#
# For each target it builds the native codec libraries (scripts/codecs.sh),
# the Rust core (cargo, --release) and the engine (kyokai-svc,
# CGO_ENABLED=0) into resources/<goos>-<goarch>/bin, which `mygo build`
# ships with the app, and copies the license texts into resources/licenses. macOS targets need a macOS host; Windows targets need
# Windows (the core links with MSVC); Linux targets need a Linux host of that
# architecture (the core links with libpipewire, whose bindings are
# generated as it builds).
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
	linux/amd64) echo x86_64-unknown-linux-gnu ;;
	linux/arm64) echo aarch64-unknown-linux-gnu ;;
	*) echo "unsupported target $1" >&2; exit 1 ;;
	esac
}

# helper_bundle writes the Info.plist and icon of Kyokai Helper.app (the
# engine's bundle on macOS), so System Settings lists its privacy
# permissions as "Kyokai Helper".
helper_bundle() {
	local contents=$1 version iconset
	version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/version/version.go)
	cat > "$contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>com.yuanzhe.kyokai.helper</string>
	<key>CFBundleName</key><string>Kyokai Helper</string>
	<key>CFBundleDisplayName</key><string>Kyokai Helper</string>
	<key>CFBundleExecutable</key><string>kyokai-svc</string>
	<key>CFBundleIconFile</key><string>icon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>LSMinimumSystemVersion</key><string>14.0</string>
	<key>LSUIElement</key><true/>
	<key>NSLocalNetworkUsageDescription</key><string>Kyokai connects directly to your other devices on this network.</string>
	<key>NSCameraUsageDescription</key><string>Kyokai shares the picture of the NanoKVM USB capture card you choose with the devices you let use it.</string>
	<key>NSMicrophoneUsageDescription</key><string>Kyokai shares the sound of the input you choose with the devices that control this Mac.</string>
</dict>
</plist>
PLIST
	mkdir -p "$contents/Resources"
	iconset=$(mktemp -d)/icon.iconset
	mkdir -p "$iconset"
	for size in 16 32 128 256 512; do
		sips -z $size $size resources/icon.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
		sips -z $((size * 2)) $((size * 2)) resources/icon.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
	done
	iconutil -c icns "$iconset" -o "$contents/Resources/icon.icns"
	rm -rf "$(dirname "$iconset")"
}

mkdir -p resources/licenses
# The GPL-3.0 text (for EasyTier's LGPL-3.0) and the native codec
# libraries' licenses and patent grants (libvpx, libaom, Opus).
cp LICENSE THIRD_PARTY_NOTICES.md third_party/licenses/*.txt resources/licenses/
# Every Rust crate in the core with its license text (EasyTier's LGPL-3.0
# among them; LGPL-3.0 also asks for the GPL-3.0 text, copied above).
command -v cargo-about >/dev/null || { echo "cargo-about is missing: cargo install cargo-about --locked" >&2; exit 1; }
cargo about generate --manifest-path core/Cargo.toml -c core/about.toml -o resources/licenses/RUST_LICENSES.txt core/about.hbs

for t in "${targets[@]}"; do
	goos=${t%/*}
	goarch=${t#*/}
	bin="resources/$goos-$goarch/bin"
	case "$goos" in
	windows) lib=kyokai_core.dll ext=.exe ;;
	# On macOS the engine and core live in a bundle of their own, which the
	# app starts and the helper copies (doc/en-us/run-modes.md).
	darwin) lib=libkyokai_core.dylib ext="" ;;
	linux) lib=libkyokai_core.so ext="" ;;
	esac
	mkdir -p "$bin"
	# On macOS the core lives in Kyokai Helper.app beside the engine, with
	# the helper's own engine, so the helper's agent is listed as "Kyokai
	# Helper" while the app's engine counts as Kyokai (doc/en-us/run-modes.md).
	libdir=$bin
	if [ "$goos" = darwin ]; then
		libdir="$bin/Kyokai Helper.app/Contents/MacOS"
		rm -f "$bin/$lib"
		mkdir -p "$libdir"
	fi
	if [ -z "${CORE_PREBUILT:-}" ]; then
		rt=$(rust_target "$t")
		rustup target add "$rt" >/dev/null
		if [ "$goos" = darwin ]; then
			export MACOSX_DEPLOYMENT_TARGET=14.0
		fi
		# libvpx and libaom, linked statically into the core.
		scripts/codecs.sh "$t"
		PROTOC=${PROTOC:-$(scripts/protoc.sh)} cargo build --release --manifest-path core/Cargo.toml --target "$rt"
		cp "core/target/$rt/release/$lib" "$libdir/$lib"
	fi
	test -f "$libdir/$lib" || { echo "missing $libdir/$lib" >&2; exit 1; }
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" -o "$bin/kyokai-svc$ext" ./cmd/kyokai-svc
	if [ "$goos" = darwin ]; then
		# The x86_64 linker leaves the core unsigned, and codesign refuses
		# the bundle's engine while a library beside it carries no signature
		# at all; the ad hoc one is replaced when the app is signed.
		codesign --force --sign - "$libdir/$lib"
		cp "$bin/kyokai-svc" "$libdir/kyokai-svc"
		helper_bundle "$(dirname "$libdir")"
	fi
	# Release builds sign the helper's files, so an installed helper updates
	# itself without a prompt (doc/en-us/run-modes.md, "Updates").
	if [ "$goos" != darwin ] && [ -n "${KYOKAI_SIGNING_KEY:-}" ]; then
		go run ./tools/sign bundle -dir "$bin" -version "$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/version/version.go)"
	fi
done

sign=()
if [ -n "${MYGO_SIGN:-}" ]; then
	sign=(-sign "$MYGO_SIGN")
fi
go tool mygo build ${sign[@]+"${sign[@]}"} -platform "$(IFS=,; echo "${targets[*]}")"
