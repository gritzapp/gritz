#!/bin/sh
# Installs the gritz CLI from a GitHub release:
#
#   curl -fsSL https://docs.gritz.dev/install.sh | sh
#
# GRITZ_VERSION      release tag to install, e.g. v4.7.0 (default: latest)
# GRITZ_INSTALL_DIR  directory to install into (default: $HOME/.local/bin)
#
# Rerun it to upgrade.

set -eu

# Everything runs from main, so a truncated download executes nothing.
main() {
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported OS: $(uname -s)" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
	esac

	if [ -n "${GRITZ_VERSION:-}" ]; then
		base="https://github.com/gritzapp/gritz/releases/download/${GRITZ_VERSION}"
	else
		base="https://github.com/gritzapp/gritz/releases/latest/download"
	fi
	dir="${GRITZ_INSTALL_DIR:-$HOME/.local/bin}"
	archive="gritz_${os}_${arch}.tar.gz"

	if command -v sha256sum >/dev/null 2>&1; then
		sha256="sha256sum"
	elif command -v shasum >/dev/null 2>&1; then
		sha256="shasum -a 256"
	else
		fail "sha256sum or shasum is required"
	fi

	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' INT TERM

	echo "Downloading $base/$archive"
	download "$base/$archive" "$tmp/$archive"
	download "$base/checksums.txt" "$tmp/checksums.txt"

	awk -v f="$archive" '$2 == f' "$tmp/checksums.txt" >"$tmp/$archive.sha256"
	[ -s "$tmp/$archive.sha256" ] || fail "$archive is not listed in checksums.txt"
	(cd "$tmp" && $sha256 -c "$archive.sha256" >/dev/null 2>&1) ||
		fail "checksum mismatch for $archive"

	tar -xzf "$tmp/$archive" -C "$tmp" gritz
	mkdir -p "$dir"
	# Rename into place, so a running gritz keeps its old file.
	cp "$tmp/gritz" "$dir/.gritz.tmp"
	chmod 755 "$dir/.gritz.tmp"
	mv -f "$dir/.gritz.tmp" "$dir/gritz"

	echo "Installed $("$dir/gritz" --version) to $dir/gritz"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*)
		echo
		echo "$dir is not on your PATH. Add it in your shell profile:"
		echo
		echo "  export PATH=\"$dir:\$PATH\""
		;;
	esac
}

download() {
	curl -fsSL -o "$2" "$1" || fail "could not download $1"
}

fail() {
	echo "gritz install: $*" >&2
	exit 1
}

main
