#!/bin/sh
# One-command installer for omada-autologin (Linux and macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/pritkr/omada-autologin/main/install.sh | sh
#
# Options (pass after `sh -s --` when piping):
#   --no-service    install and configure, but do not enable the daemon
#   --no-login      do not run an initial login test
#   --dir PATH      install the binary here (default: ~/.local/bin)
#   --version TAG   install a specific release (default: latest)
#
# Non-interactive config:
#   OMADA_PASSWORD=... OMADA_USERNAME=24105157001 OMADA_HOST=192.168.1.3 ... | sh
#   (OMADA_USERNAME is required unless OMADA_RANDOM_ONLY=1 is set explicitly.)
set -eu

REPO="${OMADA_REPO:-pritkr/omada-autologin}"
BIN="omada-autologin"
VERSION="${VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
NO_SERVICE="${OMADA_NO_SERVICE:-0}"
NO_LOGIN="${OMADA_NO_LOGIN:-0}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<EOF
omada-autologin installer

Usage: install.sh [options]

  --no-service    do not install/enable the background service
  --no-login      skip the initial login test
  --dir PATH      install directory (default: ~/.local/bin)
  --version TAG   release tag (default: latest)
  -h, --help      show this help

Environment for unattended setup:
  OMADA_USERNAME, OMADA_PASSWORD, OMADA_HOST
  OMADA_RANDOM_ONLY=1  explicitly send bare generated suffixes as usernames
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--no-service) NO_SERVICE=1 ;;
	--no-login) NO_LOGIN=1 ;;
	--dir)
		shift
		[ $# -gt 0 ] || die "--dir needs a path"
		INSTALL_DIR="$1"
		;;
	--version)
		shift
		[ $# -gt 0 ] || die "--version needs a tag"
		VERSION="$1"
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown option: $1 (try --help)"
		;;
	esac
	shift
done

[ "$(id -u)" -ne 0 ] || die "run as your normal user, not with sudo/root"

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "this installer supports Linux and macOS; on Windows use install.ps1" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

api="https://api.github.com/repos/$REPO/releases"
if [ "$VERSION" = latest ]; then
	VERSION=$(curl -fsSL --connect-timeout 10 "$api/latest" |
		sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$VERSION" ] || die "could not determine the latest release"
fi
case "$VERSION" in
v*) ;;
*) VERSION="v$VERSION" ;;
esac

asset="${BIN}_${VERSION}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/omada-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $BIN $VERSION ($os/$arch)"
curl -fL --retry 3 --connect-timeout 10 -o "$tmp/$asset" "$base/$asset"
curl -fL --retry 3 --connect-timeout 10 -o "$tmp/SHA256SUMS.txt" "$base/SHA256SUMS.txt"

expected=$(grep "  $asset\$" "$tmp/SHA256SUMS.txt" | awk '{print $1}' | head -n 1)
[ -n "$expected" ] || die "checksum for $asset is missing"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
else
	actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $asset"

tar -xzf "$tmp/$asset" -C "$tmp"
src="$tmp/${BIN}_${VERSION}_${os}_${arch}/$BIN"
[ -f "$src" ] || die "binary not found in the archive"
mkdir -p "$INSTALL_DIR"
if ! install -m 0755 "$src" "$INSTALL_DIR/$BIN" 2>/dev/null; then
	cp "$src" "$INSTALL_DIR/$BIN"
	chmod 0755 "$INSTALL_DIR/$BIN"
fi
BIN_PATH="$INSTALL_DIR/$BIN"
say "installed $BIN_PATH"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) say "note: add $INSTALL_DIR to PATH in your shell config" ;;
esac

if [ "$os" = linux ] && [ -r /etc/os-release ]; then
	# shellcheck disable=SC1091
	. /etc/os-release 2>/dev/null || true
	case "${ID:-} ${ID_LIKE:-}" in
	*cachyos* | *arch*) say "CachyOS/Arch detected: static binary, no runtime packages needed" ;;
	esac
fi
case "${XDG_CURRENT_DESKTOP:-}" in
*Hyprland*) say "Hyprland detected: the systemd user service follows your graphical session" ;;
esac
if [ -n "${HYPRLAND_INSTANCE_SIGNATURE:-}" ]; then
	say "Hyprland detected: the systemd user service follows your graphical session"
fi

have_tty=0
if (exec </dev/tty) 2>/dev/null; then have_tty=1; fi

configured=0
if "$BIN_PATH" status >/dev/null 2>&1; then
	configured=1
	say "existing config found; keeping it"
elif [ -n "${OMADA_PASSWORD:-}" ]; then
	if [ -z "${OMADA_USERNAME:-}" ] && [ "${OMADA_RANDOM_ONLY:-0}" != 1 ] && [ "$have_tty" != 1 ]; then
		die "OMADA_USERNAME is required for unattended setup (or set OMADA_RANDOM_ONLY=1 to send bare generated suffixes)"
	fi
	set -- --password "$OMADA_PASSWORD"
	[ -n "${OMADA_HOST:-}" ] && set -- "$@" --host "$OMADA_HOST"
	[ -n "${OMADA_USERNAME:-}" ] && set -- "$@" --username "$OMADA_USERNAME"
	[ "${OMADA_RANDOM_ONLY:-0}" = 1 ] && set -- "$@" --random-only
	if [ "$have_tty" = 1 ]; then
		# The curl|sh pipeline's stdin is the exhausted pipe, so prompts
		# must read the controlling terminal explicitly.
		"$BIN_PATH" init "$@" </dev/tty
	else
		"$BIN_PATH" init "$@"
	fi
	configured=1
elif [ "$have_tty" = 1 ]; then
	"$BIN_PATH" init </dev/tty
	configured=1
else
	say "no terminal available; run: $BIN_PATH init"
fi

if [ "$configured" = 1 ] && [ "$NO_LOGIN" != 1 ]; then
	say "testing login..."
	if ! "$BIN_PATH" once --verbose; then
		say "warning: first login did not succeed; check the credentials and portal host"
	fi
fi

service_ok=1
if [ "$configured" = 1 ] && [ "$NO_SERVICE" != 1 ]; then
	say "installing background service..."
	if ! "$BIN_PATH" install; then
		service_ok=0
		say "warning: could not install the background service"
		if [ "$os" = linux ]; then
			say "start it from Hyprland instead by adding this to ~/.config/hypr/hyprland.conf:"
			say "  exec-once = $BIN_PATH run"
			say "or run it manually now: $BIN_PATH run"
		else
			say "start it manually with: $BIN_PATH run"
		fi
	fi
fi

say "done"
if [ "$configured" = 1 ]; then
	say "check: $BIN_PATH status"
fi
if [ "$configured" = 1 ] && [ "$NO_SERVICE" != 1 ] && [ "$service_ok" = 1 ]; then
	if [ "$os" = linux ]; then
		say "logs:  journalctl --user -u omada-autologin -f"
	else
		say "logs:  tail -f \"$HOME/Library/Caches/omada-autologin/omada-autologin.log\""
	fi
fi
