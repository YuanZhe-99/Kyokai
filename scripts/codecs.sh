#!/bin/bash
# Builds the core's native codec libraries (libvpx, libaom) with vcpkg in
# manifest mode (core/vcpkg.json), static, into
# core/vcpkg_installed/<triplet> (one tree per triplet: manifest mode keeps
# an install tree to exactly one target).
#
#   scripts/codecs.sh                    this machine's platform
#   scripts/codecs.sh windows/arm64 darwin/amd64 ...
#
# vcpkg comes from VCPKG_ROOT when set (CI clones the pinned release), else
# from Visual Studio's own copy (Windows) or the PATH. Its tools, downloads
# and binary cache stay in vcpkg's shared default places.
set -euo pipefail
cd "$(dirname "$0")/.."

targets=("$@")
if [ ${#targets[@]} -eq 0 ]; then
	targets=("$(go env GOOS)/$(go env GOARCH)")
fi

triplet() {
	case "$1" in
	windows/amd64) echo x64-windows-static ;;
	windows/arm64) echo arm64-windows-static ;;
	darwin/amd64) echo x64-osx ;;
	darwin/arm64) echo arm64-osx ;;
	*) echo "unsupported target $1" >&2; exit 1 ;;
	esac
}

vcpkg=""
if [ -n "${VCPKG_ROOT:-}" ]; then
	vcpkg="$VCPKG_ROOT/vcpkg"
	[ -e "$vcpkg.exe" ] && vcpkg="$vcpkg.exe"
elif [ "$(go env GOHOSTOS)" = windows ]; then
	vswhere="/c/Program Files (x86)/Microsoft Visual Studio/Installer/vswhere.exe"
	if [ -x "$vswhere" ]; then
		vs=$("$vswhere" -latest -products '*' -property installationPath | tr -d '\r')
		vcpkg="$(cygpath -u "$vs")/VC/vcpkg/vcpkg.exe"
	fi
else
	vcpkg=$(command -v vcpkg || true)
fi
[ -n "$vcpkg" ] && [ -e "$vcpkg" ] || { echo "vcpkg not found: install Visual Studio's vcpkg component or set VCPKG_ROOT" >&2; exit 1; }

for t in "${targets[@]}"; do
	tr=$(triplet "$t")
	"$vcpkg" install --x-manifest-root=core --x-install-root="core/vcpkg_installed/$tr" \
		--triplet "$tr" --no-print-usage
done
