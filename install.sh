#!/usr/bin/env bash
set -euo pipefail

# Meridian — Emby reverse proxy management panel
# Interactive installer / updater / uninstaller
# Usage: bash <(curl -sL https://raw.githubusercontent.com/binaryu/emby-reverse/master/install.sh)

REPO="binaryu/emby-reverse"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="/opt/meridian"
SERVICE_FILE="/etc/systemd/system/meridian.service"
BIN_NAME="meridian"
CLONE_URL="https://github.com/${REPO}.git"
# Module requires go 1.25+ (two-component form for older parsers). Bootstrap uses this exact SDK.
GO_BOOTSTRAP_VERSION="1.25.3"

export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"

# ─── Colors ───
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

info()  { echo -e "${CYAN}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail()  { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# ─── Detect platform ───
detect_platform() {
    local os arch suffix
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m)

    case "$os" in
        linux)  os="linux" ;;
        darwin) os="darwin" ;;
        *)      fail "不支持的操作系统: $os" ;;
    esac

    case "$arch" in
        x86_64|amd64)   arch="amd64" ;;
        aarch64|arm64)  arch="arm64" ;;
        *)              fail "不支持的架构: $arch" ;;
    esac

    suffix="${os}-${arch}"
    echo "$suffix"
}

# ─── Version helpers ───
# Prefer GitHub Releases; fall back to latest tag name (assets may still be missing).
get_latest_version() {
    local json tag
    json=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || true)
    tag=$(printf '%s' "$json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"//;s/".*//')
    if [ -n "$tag" ]; then
        echo "$tag"
        return 0
    fi

    json=$(curl -fsSL "https://api.github.com/repos/${REPO}/tags?per_page=1" 2>/dev/null || true)
    tag=$(printf '%s' "$json" | grep -o '"name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"name"[[:space:]]*:[[:space:]]*"//;s/".*//')
    if [ -n "$tag" ]; then
        echo "$tag"
        return 0
    fi
    return 1
}

asset_url_exists() {
    local url="$1"
    # -f fails on HTTP errors; -I HEAD only. Some mirrors reject HEAD — fall back to range GET.
    if curl -fsI -o /dev/null -L "$url" 2>/dev/null; then
        return 0
    fi
    curl -fsL -o /dev/null -r 0-0 "$url" 2>/dev/null
}

# ─── Get current installed version ───
get_current_version() {
    if command -v "$BIN_NAME" &>/dev/null; then
        if "$BIN_NAME" --help 2>&1 | head -1 | grep -q .; then
            echo "已安装 ($("$BIN_NAME" -h 2>/dev/null | head -1 || echo ok))"
        else
            echo "已安装"
        fi
    else
        echo ""
    fi
}

download_binary() {
    local version="$1" suffix="$2" url
    url="https://github.com/${REPO}/releases/download/${version}/${BIN_NAME}-${suffix}"
    info "下载 $url ..."
    if ! curl -fSL -o "/tmp/${BIN_NAME}" "$url"; then
        return 1
    fi
    chmod +x "/tmp/${BIN_NAME}"
    return 0
}

go_version_tuple() {
    # prints "major minor" from `go version` output, e.g. "1 25"
    local ver
    ver=$(go version 2>/dev/null | sed -n 's/.*go\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p' | head -1)
    if [ -z "$ver" ]; then
        return 1
    fi
    echo "$ver"
}

go_can_build_module() {
    # True if current go accepts a two-component go 1.25 line and is >= 1.21 (toolchain auto) or >= 1.25.
    command -v go >/dev/null 2>&1 || return 1
    local major minor
    read -r major minor <<EOF
$(go_version_tuple || true)
EOF
    [ -n "${major:-}" ] && [ -n "${minor:-}" ] || return 1
    # Go < 1.21 cannot download toolchains and rejects many modern go.mod forms.
    if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 21 ]; }; then
        return 1
    fi
    # Prefer system go if already new enough to satisfy go 1.25 without download.
    if [ "$major" -gt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -ge 25 ]; }; then
        return 0
    fi
    # 1.21–1.24: rely on GOTOOLCHAIN=auto (needs network the first time).
    return 0
}

bootstrap_go() {
    # Download a portable Go SDK into $1/go and prepend to PATH.
    local dest="$1"
    local os arch tarball url
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) fail "无法为架构 $arch 下载便携 Go" ;;
    esac
    case "$os" in
        linux|darwin) ;;
        *) fail "无法为系统 $os 下载便携 Go" ;;
    esac

    tarball="go${GO_BOOTSTRAP_VERSION}.${os}-${arch}.tar.gz"
    url="https://go.dev/dl/${tarball}"
    info "本机 Go 过旧或不可用，下载便携工具链 ${GO_BOOTSTRAP_VERSION} ..."
    info "来源: $url"
    curl -fSL "$url" -o "$dest/${tarball}" || fail "下载 Go 失败。请手动安装 Go >= 1.25：https://go.dev/dl/"
    tar -C "$dest" -xzf "$dest/${tarball}" || fail "解压 Go 失败"
    export PATH="$dest/go/bin:$PATH"
    export GOROOT="$dest/go"
    # Keep auto so a still-too-new go.mod can pull a matching toolchain if needed.
    export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
    ok "便携 Go 就绪: $(go version)"
}

ensure_go() {
    if go_can_build_module; then
        ok "使用本机 Go: $(go version | awk '{print $3}')"
        export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
        return 0
    fi
    if command -v go >/dev/null 2>&1; then
        warn "本机 $(go version 2>/dev/null || echo 'go') 无法可靠编译本项目（需要 Go >= 1.25，或 1.21+ 并允许自动下载工具链）"
    else
        warn "未检测到 go 命令"
    fi
    bootstrap_go "$1"
}

# Rewrite go.mod language version to the running toolchain (major.minor).
# Avoids: old tags with go 1.26.x + portable go1.25.x + GOTOOLCHAIN=local failures.
pin_go_mod_to_toolchain() {
    local file="$1"
    local majmin
    [ -f "$file" ] || return 0
    majmin=$(go version 2>/dev/null | sed -n 's/.*go\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' | head -1)
    if [ -z "$majmin" ]; then
        warn "无法解析当前 go 版本，跳过 go.mod 对齐"
        return 0
    fi
    # Prefer two-component form for maximum parser compatibility.
    if grep -qE '^go[[:space:]]+' "$file"; then
        if sed -i.bak -E "s/^go[[:space:]]+[0-9]+(\\.[0-9]+){1,2}$/go ${majmin}/" "$file" 2>/dev/null \
            || sed -i '' -E "s/^go[[:space:]]+[0-9]+(\\.[0-9]+){1,2}$/go ${majmin}/" "$file" 2>/dev/null; then
            rm -f "${file}.bak"
            info "已将 go.mod 语言版本对齐为: go ${majmin}"
        else
            # last-resort pure shell rewrite
            local tmpf
            tmpf=$(mktemp)
            while IFS= read -r line || [ -n "$line" ]; do
                case "$line" in
                    go\ *) echo "go ${majmin}" ;;
                    *) echo "$line" ;;
                esac
            done < "$file" > "$tmpf"
            mv "$tmpf" "$file"
            info "已将 go.mod 语言版本对齐为: go ${majmin}"
        fi
    fi
}

install_from_source() {
    local version="${1:-dev}"
    local tmp src_sha build_ver

    if ! command -v git >/dev/null 2>&1; then
        fail "源码安装需要 git，请先安装 git 后重试。"
    fi

    info "未找到可用 Release 资产，改用源码编译安装..."
    tmp=$(mktemp -d)
    # shellcheck disable=SC2064
    trap "rm -rf '$tmp'" RETURN

    ensure_go "$tmp"

    # Always clone the default branch for source fallback.
    # Tag names from the API (e.g. v1.4.1) often predate go.mod fixes / fork features
    # and still have no release binaries — building that tag is the wrong tree.
    info "克隆 ${CLONE_URL} (默认分支) ..."
    git clone --depth 1 "$CLONE_URL" "$tmp/src" || fail "克隆仓库失败: ${CLONE_URL}"

    src_sha=$(git -C "$tmp/src" rev-parse --short HEAD 2>/dev/null || echo unknown)
    if [ "$version" = "dev" ] || [ -z "$version" ]; then
        build_ver="source-${src_sha}"
    else
        build_ver="${version}-source-${src_sha}"
    fi
    info "源码 revision: ${src_sha}"

    pin_go_mod_to_toolchain "$tmp/src/go.mod"

    info "编译中 (go build)..."
    (
        cd "$tmp/src"
        # auto: if go.mod still needs a newer toolchain, Go can fetch it.
        export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
        CGO_ENABLED=0 go build -ldflags="-s -w -X main.appVersion=${build_ver}" -o "/tmp/${BIN_NAME}" .
    ) || fail "源码编译失败。

排查：
  1) 确认可访问 https://go.dev/dl/ 与 proxy.golang.org
  2) 或本机安装 Go >= 1.25 后重试
  3) 或在 GitHub 打 tag 发布 Release 后走二进制安装
仓库: https://github.com/${REPO}"
    chmod +x "/tmp/${BIN_NAME}"
    ok "源码编译完成 (${build_ver})"
    # surface build label to caller via global used by place_binary if needed
    SOURCE_BUILD_VERSION="$build_ver"
}

place_binary_and_service() {
    local version="$1"

    info "安装到 ${INSTALL_DIR}/${BIN_NAME} ..."
    if [ ! -w "$INSTALL_DIR" ] 2>/dev/null; then
        sudo mv "/tmp/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
    else
        mv "/tmp/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
    fi
    ok "二进制已安装"

    # Create data directory
    if [ ! -d "$DATA_DIR" ]; then
        sudo mkdir -p "$DATA_DIR"
        ok "数据目录已创建: $DATA_DIR"
    fi

    # Generate JWT secret if not exists
    local env_file="${DATA_DIR}/.env"
    if [ ! -f "$env_file" ]; then
        local secret
        secret=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
        sudo bash -c "cat > '$env_file'" <<ENVEOF
JWT_SECRET=${secret}
PORT=9090
DB_PATH=${DATA_DIR}/meridian.db
ENVEOF
        sudo chmod 600 "$env_file"
        ok "配置文件已生成: $env_file"
    else
        info "配置文件已存在，跳过: $env_file"
    fi

    # Create systemd service
    if [ -d /run/systemd/system ]; then
        info "配置 systemd 服务..."
        sudo bash -c "cat > '$SERVICE_FILE'" <<SVCEOF
[Unit]
Description=Meridian — Emby reverse proxy management panel
After=network.target

[Service]
Type=simple
EnvironmentFile=${DATA_DIR}/.env
ExecStart=${INSTALL_DIR}/${BIN_NAME}
WorkingDirectory=${DATA_DIR}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
SVCEOF
        sudo systemctl daemon-reload
        sudo systemctl enable meridian
        ok "systemd 服务已配置"

        echo ""
        read -rp "$(echo -e "${CYAN}是否立即启动 Meridian？[Y/n]:${NC} ")" start_now || start_now=Y
        if [[ "${start_now:-Y}" != "n" && "${start_now:-Y}" != "N" ]]; then
            sudo systemctl restart meridian
            ok "Meridian 已启动"
        fi
    else
        warn "未检测到 systemd，跳过服务配置"
        echo -e "  手动启动: ${BOLD}set -a; source ${DATA_DIR}/.env; set +a; ${INSTALL_DIR}/${BIN_NAME}${NC}"
    fi

    echo ""
    echo -e "${GREEN}════════════════════════════════════════${NC}"
    echo -e "${GREEN}  Meridian ${version} 安装完成${NC}"
    echo -e "${GREEN}════════════════════════════════════════${NC}"
    echo -e "  面板地址:  ${BOLD}http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'localhost'):9090${NC}"
    echo -e "  配置文件:  ${DATA_DIR}/.env"
    echo -e "  数据目录:  ${DATA_DIR}"
    echo -e "  服务管理:  systemctl {start|stop|restart|status} meridian"
    echo -e "  仓库地址:  https://github.com/${REPO}"
    echo ""
}

# ─── Install / Update ───
do_install() {
    local suffix version url

    info "检测平台..."
    suffix=$(detect_platform)
    ok "平台: $suffix"

    info "获取最新版本..."
    version=""
    if version=$(get_latest_version); then
        ok "最新版本: $version"
    else
        warn "无法从 GitHub 解析版本号，将尝试源码安装"
        version="dev"
    fi

    url="https://github.com/${REPO}/releases/download/${version}/${BIN_NAME}-${suffix}"
    if [ "$version" != "dev" ] && asset_url_exists "$url"; then
        if download_binary "$version" "$suffix"; then
            place_binary_and_service "$version"
            return 0
        fi
        warn "Release 二进制下载失败，改用源码安装"
    else
        if [ "$version" != "dev" ]; then
            warn "未找到 Release 资产: ${BIN_NAME}-${suffix}"
            warn "（仓库有 tag 但尚未发布 Release，或资产名不匹配）"
        fi
    fi

    SOURCE_BUILD_VERSION=""
    install_from_source "$version"
    place_binary_and_service "${SOURCE_BUILD_VERSION:-$version}"
}

# ─── Uninstall ───
do_uninstall() {
    echo ""
    warn "即将卸载 Meridian，以下内容将被移除："
    echo "  - ${INSTALL_DIR}/${BIN_NAME}"
    echo "  - ${SERVICE_FILE}"
    echo ""
    echo -e "  数据目录 ${DATA_DIR} ${YELLOW}不会删除${NC}（含数据库与 .env）"
    echo ""
    read -rp "确认卸载？[y/N]: " confirm
    if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
        info "已取消"
        exit 0
    fi

    if [ -f "$SERVICE_FILE" ]; then
        sudo systemctl stop meridian 2>/dev/null || true
        sudo systemctl disable meridian 2>/dev/null || true
        sudo rm -f "$SERVICE_FILE"
        sudo systemctl daemon-reload
        ok "systemd 服务已移除"
    fi

    if [ -f "${INSTALL_DIR}/${BIN_NAME}" ]; then
        sudo rm -f "${INSTALL_DIR}/${BIN_NAME}"
        ok "二进制已移除"
    fi

    echo ""
    ok "Meridian 已卸载"
    info "如需清理数据: sudo rm -rf ${DATA_DIR}"
}

# ─── Main menu ───
main() {
    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════╗${NC}"
    echo -e "${BOLD}║     Meridian 安装管理工具             ║${NC}"
    echo -e "${BOLD}║     Emby reverse proxy panel         ║${NC}"
    echo -e "${BOLD}║     github.com/${REPO}${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════╝${NC}"
    echo ""

    local current
    current=$(get_current_version)
    if [ -n "$current" ]; then
        echo -e "  当前状态: ${GREEN}${current}${NC}"
    else
        echo -e "  当前状态: ${YELLOW}未安装${NC}"
    fi
    echo ""
    echo "  1) 安装 / 更新"
    echo "  2) 卸载"
    echo "  0) 退出"
    echo ""

    read -rp "请选择 [0-2]: " choice
    case "$choice" in
        1) do_install ;;
        2) do_uninstall ;;
        0) exit 0 ;;
        *) fail "无效选项" ;;
    esac
}

# Allow direct action via argument: install.sh install / uninstall
case "${1:-}" in
    install|update) do_install ;;
    uninstall|remove) do_uninstall ;;
    *) main ;;
esac
