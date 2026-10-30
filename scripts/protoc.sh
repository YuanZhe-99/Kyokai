#!/usr/bin/env bash
# Prints the path of a verified protoc for EasyTier's build, downloading the
# pinned release into build/tools once. Use: export PROTOC=$(scripts/protoc.sh)
# EasyTier would otherwise fetch an unverified release candidate on Windows.
set -euo pipefail

version=36.2
case "$(uname -s)-$(uname -m)" in
  MINGW*|MSYS*|CYGWIN*) asset=win64; sha=f0c128dc0d8492eceece83bb459a4c0e316764b929ffbf1aa416357fd644edd3; exe=protoc.exe ;;
  Darwin-arm64) asset=osx-aarch_64; sha=9cd98a532c5c5e0c4161314de0225de27e4c8a323917b6ea7b1b714d3ae23466; exe=protoc ;;
  Darwin-x86_64) asset=osx-x86_64; sha=228cc7add4616cc14ca5e80dee83209d44449a7aee95a914ae748fa374efb078; exe=protoc ;;
  Linux-x86_64) asset=linux-x86_64; sha=121f6c7afe1d4d0e3ea6aab9432038599250134cbf4474cb1167d2c7decd4278; exe=protoc ;;
  Linux-aarch64) asset=linux-aarch_64; sha=8b8f18bd2b30346efbc698dd5a73dd7c805f3ef8380f6dfc95c768f3f1852f6a; exe=protoc ;;
  *) echo "protoc.sh: no pinned protoc for $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$root/build/tools/protoc-$version-$asset"
if [ ! -x "$dir/bin/$exe" ]; then
  zip="$root/build/tools/protoc-$version-$asset.zip"
  mkdir -p "$root/build/tools"
  curl -fsSL -o "$zip" "https://github.com/protocolbuffers/protobuf/releases/download/v$version/protoc-$version-$asset.zip"
  got=$( (sha256sum "$zip" 2>/dev/null || shasum -a 256 "$zip") | cut -d' ' -f1)
  if [ "$got" != "$sha" ]; then
    rm -f "$zip"
    echo "protoc.sh: checksum mismatch for protoc-$version-$asset.zip" >&2
    exit 1
  fi
  rm -rf "$dir" && mkdir -p "$dir"
  unzip -q "$zip" -d "$dir"
  rm -f "$zip"
  chmod +x "$dir/bin/$exe"
fi
path="$dir/bin/$exe"
command -v cygpath > /dev/null && path=$(cygpath -w "$path")
echo "$path"
