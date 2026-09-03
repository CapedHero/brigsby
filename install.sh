#!/bin/sh
# Brigsby installer: download a prebuilt release binary, verify its published
# SHA-256 checksum, and install it to a user-writable directory. No sudo.
#
# On macOS, `brew install CapedHero/brigsby/brigsby` is the recommended path.
# This script exists for Linux, containers, and CI.
#
# Inspect before running:
#   curl -fsSL https://raw.githubusercontent.com/CapedHero/brigsby/main/install.sh -o install.sh
#   less install.sh
#   sh install.sh
#
# Environment overrides:
#   BRIGSBY_VERSION       release tag to install, e.g. 0.0.4 (default: latest)
#   BRIGSBY_INSTALL_DIR   install directory (default: $HOME/.local/bin)
#   BRIGSBY_REPO          GitHub owner/repo (default: CapedHero/brigsby)
#   BRIGSBY_SKIP_ATTEST   set to any value to skip `gh attestation verify`
#   BRIGSBY_BASE_URL      advanced: base URL or local directory holding the
#                         release assets, for mirrors and offline installs

set -eu

REPO="${BRIGSBY_REPO:-CapedHero/brigsby}"
VERSION="${BRIGSBY_VERSION:-latest}"
INSTALL_DIR="${BRIGSBY_INSTALL_DIR:-$HOME/.local/bin}"

info() { printf 'brigsby: %s\n' "$1"; }
warn() { printf 'brigsby: warning: %s\n' "$1" >&2; }
die() {
	printf 'brigsby: error: %s\n' "$1" >&2
	exit 1
}

detect_os() {
	case "$(uname -s)" in
	Linux) echo linux ;;
	Darwin) echo darwin ;;
	*) die "unsupported operating system: $(uname -s) (use the Homebrew or Go toolchain path)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

fetch() {
	# fetch <url-or-path> <destination>
	case "$1" in
	https://*)
		if command -v curl >/dev/null 2>&1; then
			curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --retry-delay 1 \
				-o "$2" "$1"
		elif command -v wget >/dev/null 2>&1; then
			wget -q --https-only -O "$2" "$1"
		else
			die "need curl or wget on PATH"
		fi
		;;
	file://*)
		cp "${1#file://}" "$2"
		;;
	http://*)
		die "refusing insecure http:// source: $1"
		;;
	*)
		cp "$1" "$2"
		;;
	esac
}

sha256_of() {
	# sha256_of <file> -> lowercase hex digest on stdout
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | sed 's/.*= *//'
	else
		die "need sha256sum, shasum, or openssl to verify the download"
	fi
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
ASSET="brigsby_${OS}_${ARCH}.tar.gz"

if [ "$VERSION" = latest ]; then
	BASE_URL="https://github.com/${REPO}/releases/latest/download"
	LABEL="latest"
else
	BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION#v}"
	LABEL="v${VERSION#v}"
fi
BASE_URL="${BRIGSBY_BASE_URL:-$BASE_URL}"
BASE_URL="${BASE_URL%/}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

info "downloading ${ASSET} (${LABEL})"
fetch "${BASE_URL}/${ASSET}" "${TMP}/${ASSET}"
fetch "${BASE_URL}/checksums.txt" "${TMP}/checksums.txt"

expected="$(grep " ${ASSET}\$" "${TMP}/checksums.txt" | cut -d' ' -f1 || true)"
[ -n "$expected" ] || die "no checksum for ${ASSET} in checksums.txt"
actual="$(sha256_of "${TMP}/${ASSET}")"
if [ "$expected" != "$actual" ]; then
	die "checksum mismatch for ${ASSET}: expected ${expected}, got ${actual}"
fi
info "checksum verified"

if [ -z "${BRIGSBY_SKIP_ATTEST:-}" ] && command -v gh >/dev/null 2>&1; then
	if gh attestation verify "${TMP}/${ASSET}" --repo "$REPO" >/dev/null 2>&1; then
		info "build provenance verified"
	else
		warn "could not verify build provenance with 'gh attestation verify'; continuing (set BRIGSBY_SKIP_ATTEST=1 to silence)"
	fi
elif [ -z "${BRIGSBY_SKIP_ATTEST:-}" ]; then
	warn "'gh' not found; skipping build-provenance check"
fi

tar -xzf "${TMP}/${ASSET}" -C "$TMP"
[ -f "${TMP}/brigsby" ] || die "archive did not contain a brigsby binary"

mkdir -p "$INSTALL_DIR"
cp "${TMP}/brigsby" "${INSTALL_DIR}/brigsby"
chmod 0755 "${INSTALL_DIR}/brigsby"
info "installed to ${INSTALL_DIR}/brigsby"

case ":${PATH}:" in
*":${INSTALL_DIR}:"*) ;;
*) warn "${INSTALL_DIR} is not on PATH; add it to your shell profile" ;;
esac

"${INSTALL_DIR}/brigsby" --version
