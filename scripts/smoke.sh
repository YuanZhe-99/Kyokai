#!/bin/bash
# End-to-end check of the No privileges engine with the real core:
#
#   scripts/smoke.sh user
#
# Builds the core and kyokai-svc for this machine into a private folder,
# starts the engine on its own socket and state directory, and checks the
# local API (status, password, regenerate), that a second engine is
# refused, that shutdown works, and that the Device ID survives a restart.
set -euo pipefail
cd "$(dirname "$0")/.."
[ "${1:-}" = user ] || { echo "usage: scripts/smoke.sh user" >&2; exit 2; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
case "$(go env GOOS)" in
windows) lib=kyokai_core.dll ext=.exe ;;
darwin) lib=libkyokai_core.dylib ext="" ;;
*) echo "unsupported platform" >&2; exit 1 ;;
esac
# For the host's own target triple, like scripts/build.sh: CI builds it once.
rt=$(rustc -vV | sed -n 's/^host: //p')
PROTOC=${PROTOC:-$(scripts/protoc.sh)} cargo build --release --manifest-path core/Cargo.toml --target "$rt"
cp "core/target/$rt/release/$lib" "$work/"
CGO_ENABLED=0 go build -o "$work/kyokai-svc$ext" ./cmd/kyokai-svc

native() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else echo "$1"; fi; }
# Its own profile, so the OS credential store entry is never the real one.
export KYOKAI_PROFILE="smoke-$$" KYOKAI_SOCKET
KYOKAI_SOCKET=$(native "$work/e.sock")
state=$(native "$work/state")
svc="$work/kyokai-svc$ext"

fail() { echo "FAIL: $*" >&2; exit 1; }
start() {
	"$svc" run --host user --state-dir "$state" &
	for _ in $(seq 1 50); do "$svc" ctl status >/dev/null 2>&1 && return; sleep 0.2; done
	fail "the engine did not answer"
}

start
id1=$("$svc" ctl status | sed -n 's/^device ID //p')
[[ "$id1" =~ ^[1-9][0-9]{8}$ ]] || fail "bad Device ID '$id1'"
pw1=$("$svc" ctl password)
[ ${#pw1} -eq 8 ] || fail "temporary password has ${#pw1} characters"
"$svc" ctl regenerate >/dev/null
[ "$("$svc" ctl password)" != "$pw1" ] || fail "regenerate kept the password"
if "$svc" run --host user --state-dir "$state" 2>/dev/null; then fail "a second engine started"; fi
"$svc" ctl shutdown >/dev/null
wait
"$svc" ctl status >/dev/null 2>&1 && fail "the engine still answers after shutdown"

start
id2=$("$svc" ctl status | sed -n 's/^device ID //p')
"$svc" ctl shutdown >/dev/null
wait
[ "$id1" = "$id2" ] || fail "Device ID changed across a restart: $id1 → $id2"
echo "smoke user: ok (Device ID $id1)"
