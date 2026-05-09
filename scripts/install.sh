#!/bin/sh
set -eu

MODULE="github.com/shinpr/galley/cmd/galley"
VERSION="${GALLEY_VERSION:-latest}"
BIN_DIR="${GOBIN:-}"
MODE="auto"

usage() {
  cat <<'USAGE'
Install the Galley CLI.

Usage:
  scripts/install.sh [options]

Options:
  --version <version>   Install a module version such as latest, v0.1.0, or a commit.
  --bin-dir <dir>       Install into a specific directory by setting GOBIN for go install.
  --local               Install from the current checkout's ./cmd/galley.
  --remote              Install github.com/shinpr/galley/cmd/galley@<version>.
  -h, --help            Show this help.

Environment:
  GALLEY_VERSION        Default version for remote install. Defaults to latest.
  GOBIN                 Default install directory when --bin-dir is not provided.

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
    --local)
      MODE="local"
      shift
      ;;
    --remote)
      MODE="remote"
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

if ! command -v go >/dev/null 2>&1; then
  echo "go is required to install Galley" >&2
  exit 127
fi

if [ -n "$BIN_DIR" ]; then
  mkdir -p "$BIN_DIR"
  export GOBIN="$BIN_DIR"
else
  GOBIN="$(go env GOBIN)"
  if [ -z "$GOBIN" ]; then
    GOPATH="$(go env GOPATH)"
    GOBIN="$GOPATH/bin"
  fi
fi

if [ "$MODE" = "auto" ]; then
  if [ -f "go.mod" ] && grep -q '^module github.com/shinpr/galley$' go.mod && [ -d "cmd/galley" ]; then
    MODE="local"
  else
    MODE="remote"
  fi
fi

case "$MODE" in
  local)
    echo "Installing galley from local checkout into $GOBIN"
    GOBIN="$GOBIN" go install ./cmd/galley
    ;;
  remote)
    echo "Installing galley@$VERSION into $GOBIN"
    GOBIN="$GOBIN" go install "$MODULE@$VERSION"
    ;;
  *)
    echo "invalid install mode: $MODE" >&2
    exit 2
    ;;
esac

GALLEY_BIN="$GOBIN/galley"
if [ ! -x "$GALLEY_BIN" ]; then
  echo "install did not produce executable: $GALLEY_BIN" >&2
  exit 1
fi

case ":$PATH:" in
  *":$GOBIN:"*) ;;
  *)
    echo
    echo "Installed $GALLEY_BIN, but $GOBIN is not on PATH."
    echo "Add it to your shell profile, for example:"
    echo "  export PATH=\"$GOBIN:\$PATH\""
    ;;
esac

"$GALLEY_BIN" --help >/dev/null
echo "Galley installed: $GALLEY_BIN"
