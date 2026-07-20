#!/usr/bin/env bash
# build.sh — build meridian on any Linux/macOS host, including old system Go (e.g. Debian go1.19).
# Usage:
#   ./build.sh              # → ./meridian
#   ./build.sh -o /tmp/m    # custom output path
#   VERSION=v1.5.0 ./build.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

BIN_NAME="${BIN_NAME:-meridian}"
OUT="${OUT:-}"
VERSION="${VERSION:-dev}"
# Portable SDK used when system go is missing or too old (< 1.21 cannot parse modern go.mod / toolchain).
GO_SDK_VERSION="${GO_SDK_VERSION:-1.25.3}"
SDK_DIR="${ROOT}/.go-sdk"
# Two-component language version written into go.mod for the build (Go <1.21 rejects 1.x.y).
GO_MOD_LINE="${GO_MOD_LINE:-1.25}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${CYAN}[build]${NC} $*"; }
ok()    { echo -e "${GREEN}[build]${NC} $*"; }
warn()  { echo -e "${YELLOW}[build]${NC} $*"; }
fail()  { echo -e "${RED}[build]${NC} $*" >&2; exit 1; }

# parse -o out
while [ $# -gt 0 ]; do
  case "$1" in
    -o|--output) OUT="${2:-}"; shift 2 ;;
    -h|--help)
      sed -n '2,8p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *) fail "unknown arg: $1 (try --help)" ;;
  esac
done
OUT="${OUT:-$ROOT/$BIN_NAME}"

export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
export CGO_ENABLED="${CGO_ENABLED:-0}"

go_version_tuple() {
  go version 2>/dev/null | sed -n 's/.*go\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p' | head -1
}

system_go_usable() {
  command -v go >/dev/null 2>&1 || return 1
  local major minor
  read -r major minor <<EOF
$(go_version_tuple || true)
EOF
  [ -n "${major:-}" ] && [ -n "${minor:-}" ] || return 1
  # Need at least Go 1.21 for three-part go lines / GOTOOLCHAIN; we still pin go.mod to two-part.
  # Prefer 1.25+ for matching module requirement without downloading another toolchain.
  if [ "$major" -gt 1 ]; then return 0; fi
  if [ "$major" -eq 1 ] && [ "$minor" -ge 21 ]; then return 0; fi
  return 1
}

install_portable_go() {
  local os arch tarball url
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "unsupported arch: $arch" ;;
  esac
  case "$os" in
    linux|darwin) ;;
    *) fail "unsupported OS: $os" ;;
  esac

  if [ -x "$SDK_DIR/go/bin/go" ]; then
    local have
    have=$("$SDK_DIR/go/bin/go" version 2>/dev/null || true)
    if echo "$have" | grep -q "go${GO_SDK_VERSION}"; then
      ok "reuse portable Go: $have"
      export PATH="$SDK_DIR/go/bin:$PATH"
      export GOROOT="$SDK_DIR/go"
      return 0
    fi
  fi

  mkdir -p "$SDK_DIR"
  tarball="go${GO_SDK_VERSION}.${os}-${arch}.tar.gz"
  url="https://go.dev/dl/${tarball}"
  info "system Go unusable for this module; downloading portable Go ${GO_SDK_VERSION}"
  info "url: $url"
  curl -fSL "$url" -o "$SDK_DIR/${tarball}" || fail "download Go failed (check network / mirror)"
  rm -rf "$SDK_DIR/go"
  tar -C "$SDK_DIR" -xzf "$SDK_DIR/${tarball}"
  rm -f "$SDK_DIR/${tarball}"
  export PATH="$SDK_DIR/go/bin:$PATH"
  export GOROOT="$SDK_DIR/go"
  ok "portable Go ready: $(go version)"
}

ensure_go() {
  if system_go_usable; then
    ok "using system Go: $(go version | awk '{print $3}')"
    export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
    return 0
  fi
  if command -v go >/dev/null 2>&1; then
    warn "system $(go version 2>/dev/null) is too old for this repo (need ≥1.21, ideally ≥1.25)"
  else
    warn "go not found on PATH"
  fi
  install_portable_go
  export GOTOOLCHAIN=local
}

# Adjust go.mod's language line so the active (possibly old) toolchain accepts it.
# - Go >= 1.21 accepts both two- and three-component forms (1.25 / 1.25.0); keep as-is.
# - Go < 1.21 only accepts two-component (1.25); rewrite 1.25.0 → 1.25.
# We never downgraded by hand, so try the build first and only patch on the specific error.
pin_go_mod_if_needed() {
  local file="$ROOT/go.mod"
  [ -f "$file" ] || fail "missing go.mod"

  # quick check: parse go.mod via `go list` with -mod=mod so it won't require tidy
  if GOTOOLCHAIN="${GOTOOLCHAIN:-local}" GOPROXY=off go list -mod=mod ./... >/dev/null 2>&1; then
    return 0
  fi

  # capture the actual error
  local err
  err=$(GOTOOLCHAIN="${GOTOOLCHAIN:-local}" GOPROXY=off go list -mod=mod ./... 2>&1 | head -5 || true)
  if echo "$err" | grep -qE "invalid go version|must match format"; then
    local majmin
    majmin=$(go version 2>/dev/null | sed -n 's/.*go\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' | head -1)
    [ -n "$majmin" ] || majmin="$GO_MOD_LINE"
    local tmp
    tmp=$(mktemp)
    while IFS= read -r row || [ -n "$row" ]; do
      case "$row" in
        go\ *) printf 'go %s\n' "$majmin" ;;
        *) printf '%s\n' "$row" ;;
      esac
    done < "$file" > "$tmp"
    mv "$tmp" "$file"
    info "go.mod language line → go ${majmin} (was rejected: $(echo "$err" | head -1))"
  else
    # unrelated build error; surface it
    warn "go.mod parse pre-check failed:"
    printf '%s\n' "$err" | sed 's/^/  /'
  fi
}

ensure_go
pin_go_mod_if_needed

# Resolve VERSION from git when possible
if [ "$VERSION" = "dev" ] && command -v git >/dev/null 2>&1 && [ -d "$ROOT/.git" ]; then
  VERSION=$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)
fi

info "building ${OUT} (version=${VERSION}, toolchain=$(go version | awk '{print $3}'))"
# `-mod=mod` lets Go write go.sum / update sums without requiring a separate `go mod tidy`.
go build -mod=mod -trimpath -ldflags="-s -w -X main.appVersion=${VERSION}" -o "$OUT" .
ok "OK → $OUT"
ok "run: JWT_SECRET=\$(openssl rand -hex 32) $OUT"
