#!/bin/bash
# Builds Kyokai-<version>-<arch>.AppImage from what `mygo build` wrote for a
# Linux target (build/linux-<arch>: the executable, bin/, licenses/, the
# desktop entry and the icon), with the pinned appimagetool, downloaded into
# build/tools once and checked against its SHA-256.
#
#   scripts/appimage.sh            this machine's architecture
#   scripts/appimage.sh arm64
#
# The AppImage needs GTK 3, the AppIndicator library, PipeWire and libva on
# the system, and FFmpeg's libavcodec for H.264 and HEVC in the viewer, as
# the Debian package does (doc/en-us/packaging.md). It does not
# update itself: a new release is a new file.
set -euo pipefail
cd "$(dirname "$0")/.."

arch=${1:-$(go env GOARCH)}
tool_version=1.9.1
case "$arch" in
amd64) tarch=x86_64 sha=ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0 ;;
arm64) tarch=aarch64 sha=f0837e7448a0c1e4e650a93bb3e85802546e60654ef287576f46c71c126a9158 ;;
*) echo "appimage.sh: no AppImage for $arch" >&2; exit 1 ;;
esac
version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/version/version.go)
src="build/linux-$arch"
test -x "$src/kyokai" || { echo "appimage.sh: build $src first (scripts/build.sh linux/$arch)" >&2; exit 1; }

tool="build/tools/appimagetool-$tool_version-$tarch.AppImage"
if [ ! -x "$tool" ]; then
	mkdir -p build/tools
	curl -fsSL -o "$tool.part" "https://github.com/AppImage/appimagetool/releases/download/$tool_version/appimagetool-$tarch.AppImage"
	got=$(sha256sum "$tool.part" | cut -d' ' -f1)
	if [ "$got" != "$sha" ]; then
		rm -f "$tool.part"
		echo "appimage.sh: checksum mismatch for appimagetool $tool_version ($tarch)" >&2
		exit 1
	fi
	chmod +x "$tool.part" && mv "$tool.part" "$tool"
fi

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
app="$dir/Kyokai.AppDir"
mkdir -p "$app"
# The app's files as mygo build laid them out, beside the executable.
for f in kyokai bin licenses libghostty-vt.so icon.png kyokai.desktop kyokai.png; do
	[ -e "$src/$f" ] && cp -a "$src/$f" "$app/"
done
ln -s kyokai.png "$app/.DirIcon"
cat > "$app/AppRun" <<'EOF'
#!/bin/sh
here=$(dirname "$(readlink -f "$0")")
exec "$here/kyokai" "$@"
EOF
chmod +x "$app/AppRun"
out="build/linux-$arch/Kyokai-$version-$tarch.AppImage"
# The tool runs from its own extracted copy: no FUSE needed on the build
# machine or a runner.
ARCH=$tarch APPIMAGE_EXTRACT_AND_RUN=1 "$tool" --no-appstream "$app" "$out"
echo "$out"
