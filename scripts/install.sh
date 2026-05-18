#!/bin/sh
set -eu

OWNER="shinpr"
REPO="galley"
MODULE="github.com/shinpr/galley/cmd/galley"
VERSION="${GALLEY_VERSION:-latest}"
BIN_DIR="${GALLEY_BIN_DIR:-${GOBIN:-}}"
MODE="release"

usage() {
  cat <<'USAGE'
Install the Galley CLI.

Usage:
  scripts/install.sh [options]

Options:
  --version <version>   Install a release version such as latest or a tag.
  --bin-dir <dir>       Install into a specific directory. Defaults to $GALLEY_BIN_DIR, $GOBIN, or ~/.local/bin.
  --release             Install a prebuilt GitHub Release asset. This is the default.
  --local               Build and install from the current checkout's ./cmd/galley.
  --go-install          Install with go install github.com/shinpr/galley/cmd/galley@<version>.
  --remote              Alias for --go-install.
  -h, --help            Show this help.

Environment:
  GALLEY_VERSION        Release or module version. Defaults to latest.
  GALLEY_BIN_DIR        Default install directory when --bin-dir is not provided.
  GOBIN                 Fallback install directory when GALLEY_BIN_DIR is not set.

The installer installs the Galley CLI. Daemon operations are available under
`galley daemon ...`.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      if [ "$#" -lt 2 ]; then
        echo "missing value for --version" >&2
        exit 2
      fi
      VERSION="$2"
      shift 2
      ;;
    --bin-dir)
      if [ "$#" -lt 2 ]; then
        echo "missing value for --bin-dir" >&2
        exit 2
      fi
      BIN_DIR="$2"
      shift 2
      ;;
    --release)
      MODE="release"
      shift
      ;;
    --local)
      MODE="local"
      shift
      ;;
    --go-install|--remote)
      MODE="go-install"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ -z "$BIN_DIR" ]; then
  BIN_DIR="$HOME/.local/bin"
fi

mkdir -p "$BIN_DIR"

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$1 is required" >&2
    exit 127
  fi
}

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux) echo "linux" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *)
      echo "unsupported OS: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      echo "unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

galley_bin_name() {
  if [ "$(detect_os)" = "windows" ]; then
    echo "galley.exe"
  else
    echo "galley"
  fi
}

resolve_latest_version() {
  require_cmd curl
  curl -fsSL "https://api.github.com/repos/$OWNER/$REPO/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
    head -n 1
}

stop_existing_daemon() {
  bin="$1"
  if [ ! -x "$bin" ]; then
    return
  fi
  status="$("$bin" daemon status --output json 2>/dev/null || true)"
  if printf '%s\n' "$status" | grep -q '"running"[[:space:]]*:[[:space:]]*true' &&
    printf '%s\n' "$status" | grep -q '"verified"[[:space:]]*:[[:space:]]*true'; then
    echo "Existing Galley daemon is running; stopping it before install..."
    if ! "$bin" daemon stop; then
      echo "could not stop existing Galley daemon; run \`$bin daemon stop\` and retry" >&2
      exit 1
    fi
  fi
}

sign_darwin_binary() {
  bin="$1"
  if [ "$(uname -s)" != "Darwin" ]; then
    return
  fi
  if ! command -v codesign >/dev/null 2>&1; then
    return
  fi
  if ! codesign --force --sign - "$bin" >/dev/null 2>&1; then
    echo "warning: failed to ad-hoc sign $bin" >&2
  fi
}

install_release() {
  require_cmd curl
  require_cmd tar

  os="$(detect_os)"
  arch="$(detect_arch)"
  version="$VERSION"
  if [ "$version" = "latest" ]; then
    version="$(resolve_latest_version)"
    if [ -z "$version" ]; then
      echo "could not resolve latest Galley release" >&2
      exit 1
    fi
  fi

  asset_version="${version#v}"
  asset="galley_${asset_version}_${os}_${arch}.tar.gz"
  url="https://github.com/$OWNER/$REPO/releases/download/$version/$asset"
  tmp_dir="$(mktemp -d)"
  archive="$tmp_dir/$asset"
  cleanup() {
    rm -rf "$tmp_dir"
  }
  trap cleanup EXIT HUP INT TERM

  echo "Downloading $url"
  curl -fL "$url" -o "$archive"
  tar -xzf "$archive" -C "$tmp_dir"

  bin_name="$(galley_bin_name)"
  src="$tmp_dir/$bin_name"
  if [ ! -f "$src" ]; then
    src="$(find "$tmp_dir" -type f -name "$bin_name" | head -n 1)"
  fi
  if [ -z "$src" ] || [ ! -f "$src" ]; then
    echo "release asset did not contain $bin_name" >&2
    exit 1
  fi

  dest="$BIN_DIR/$bin_name"
  stop_existing_daemon "$dest"
  cp "$src" "$dest"
  chmod 755 "$dest"
  GALLEY_BIN="$dest"
}

install_local() {
  require_cmd go
  GALLEY_BIN="$BIN_DIR/$(galley_bin_name)"
  stop_existing_daemon "$GALLEY_BIN"
  echo "Installing galley from local checkout into $BIN_DIR"
  GOBIN="$BIN_DIR" go install ./cmd/galley
}

install_go() {
  require_cmd go
  GALLEY_BIN="$BIN_DIR/$(galley_bin_name)"
  stop_existing_daemon "$GALLEY_BIN"
  echo "Installing galley@$VERSION into $BIN_DIR"
  GOBIN="$BIN_DIR" go install "$MODULE@$VERSION"
}

case "$MODE" in
  release)
    install_release
    ;;
  local)
    install_local
    ;;
  go-install)
    install_go
    ;;
  *)
    echo "invalid install mode: $MODE" >&2
    exit 2
    ;;
esac

if [ ! -x "$GALLEY_BIN" ]; then
  echo "install did not produce executable: $GALLEY_BIN" >&2
  exit 1
fi

sign_darwin_binary "$GALLEY_BIN"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    echo
    echo "Installed $GALLEY_BIN, but $BIN_DIR is not on PATH."
    echo "Add it to your shell profile, for example:"
    echo "  export PATH=\"$BIN_DIR:\$PATH\""
    ;;
esac

"$GALLEY_BIN" --help >/dev/null
echo "Galley installed: $GALLEY_BIN"
