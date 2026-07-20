<div align="center">

# Meridian (emby-reverse)

轻量级 Emby 反向代理管理面板
单文件 Go 后端 + 嵌入式 SPA 前端，开箱即用

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![SQLite](https://img.shields.io/badge/SQLite-embedded-003B57?logo=sqlite&logoColor=white)](https://pkg.go.dev/modernc.org/sqlite)
[![CI](https://github.com/binaryu/emby-reverse/actions/workflows/ci.yml/badge.svg)](https://github.com/binaryu/emby-reverse/actions/workflows/ci.yml)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)](https://github.com/binaryu/emby-reverse/pkgs/container/emby-reverse)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

</div>

## 界面预览

| 仪表盘 | 站点管理 | 故障诊断 |
|:---:|:---:|:---:|
| ![仪表盘](docs/dashboard.png) | ![站点管理](docs/sites.png) | ![故障诊断](docs/diagnostics.png) |

## 这是什么

Meridian 是一个专为 Emby 媒体服务器设计的反向代理管理面板（Emby reverse proxy management panel）。它解决的核心问题是：**当你需要在一台机器上管理多个 Emby 反代站点时，不想手写 Nginx 配置，不想逐个维护 UA 伪装规则，也不想自己实现流量计量和限速。**

Meridian 把这些事情打包成一个单二进制程序，带管理界面，带实时监控，开箱可用。

## 核心特性

| 功能 | 说明 |
|------|------|
| **多站点反代** | 每个站点独立监听端口，独立配置上游地址 |
| **双上游分流** | 网页/API 和播放/转码流量可分别指向不同上游 |
| **UA 伪装** | 3 种预设（Infuse / Web / 客户端），HTTP + WebSocket 统一改写 |
| **EMOS 身份头** | 按站点配置 `EMOS-PROXY-ID` / `EMOS-PROXY-NAME`，并自动传递 `X-Forwarded-For` |
| **视频 Range / 206** | 透传 Range，流式转发分片（含 HTTP 206 Partial Content） |
| **图片 / Ping 缓存** | 可选缓存 `/Items/*/Images/*` 与 `/System/Ping`（含 `/emby` 前缀） |
| **Progress 节流** | 可选限频 `/Sessions/Playing/Progress`，减轻上游压力 |
| **流量管控** | 按站点统计流量、设置限速、设置配额 |
| **WebSocket 代理** | 完整支持 Emby 的 WebSocket 通信 |
| **SSE 实时推送** | 仪表盘数据通过 Server-Sent Events 实时更新 |
| **故障诊断** | 回源健康检测、上游 TLS 证书检查、请求头预览 |
| **JWT 认证** | Bearer Token 认证，密码 bcrypt 存储 |
| **单二进制部署** | 前端嵌入二进制，SQLite 持久化，无外部依赖 |

---

## 快速部署

### Linux / macOS — 交互式安装（适用于已发布版本）

一行命令，自动检测平台、下载最新版、配置 systemd 服务：

```bash
bash <(curl -sL https://raw.githubusercontent.com/binaryu/emby-reverse/master/install.sh)
```

脚本支持三个操作：**安装**、**更新**（再跑一次即可）、**卸载**。也可以直接指定动作：

```bash
# 直接安装/更新
bash <(curl -sL https://raw.githubusercontent.com/binaryu/emby-reverse/master/install.sh) install

# 直接卸载
bash <(curl -sL https://raw.githubusercontent.com/binaryu/emby-reverse/master/install.sh) uninstall
```

### Docker

```bash
docker run -d --name meridian \
  -p 9090:9090 -p 8001-8010:8001-8010 \
  -v meridian-data:/app/data \
  -e JWT_SECRET=$(openssl rand -hex 32) \
  ghcr.io/binaryu/emby-reverse:latest
```

> `8001-8010` 是反代站点监听端口范围，按实际需要调整。
>
> 官方镜像会在推送 `v*` 标签时由 GitHub Actions 构建并推送到 GHCR。若仓库尚未发布版本，或 GHCR 中暂时没有可用镜像，请改用源码构建。

### Windows

```powershell
Invoke-WebRequest -Uri "https://github.com/binaryu/emby-reverse/releases/latest/download/meridian-windows-amd64.exe" -OutFile "meridian.exe"
$env:JWT_SECRET = -join ((1..32) | ForEach-Object { '{0:x2}' -f (Get-Random -Max 256) })
.\meridian.exe
```

> Windows 二进制下载同样依赖 GitHub Releases。没有已发布版本时，请使用源码构建。

### Linux — 手动安装（从 Releases 下载二进制）

不想用一键脚本时，可以手动下载二进制、配置 systemd 服务常驻。

```bash
# 1. 下载对应架构的二进制（amd64 为例；arm64 请改 meridian-linux-arm64）
curl -L -o meridian \
  https://github.com/binaryu/emby-reverse/releases/latest/download/meridian-linux-amd64
chmod +x meridian
sudo mv meridian /usr/local/bin/meridian

# 2. 创建数据目录与固定 JWT_SECRET
sudo mkdir -p /opt/meridian
sudo tee /opt/meridian/.env > /dev/null <<EOF
JWT_SECRET=$(openssl rand -hex 32)
PORT=9090
DB_PATH=/opt/meridian/meridian.db
EOF
sudo chmod 600 /opt/meridian/.env

# 3. 注册 systemd 服务
sudo tee /etc/systemd/system/meridian.service > /dev/null <<'EOF'
[Unit]
Description=Meridian — Emby reverse proxy management panel
After=network.target

[Service]
Type=simple
EnvironmentFile=/opt/meridian/.env
ExecStart=/usr/local/bin/meridian
WorkingDirectory=/opt/meridian
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now meridian
sudo systemctl status meridian --no-pager
```

启动后访问 `http://<服务器IP>:9090`，首次打开会引导设置管理员密码。

服务管理：

```bash
sudo systemctl restart meridian    # 重启（升级二进制后）
sudo systemctl stop meridian       # 停止
sudo systemctl status meridian     # 查看状态
journalctl -u meridian -f          # 跟踪日志
```

升级：覆盖 `/usr/local/bin/meridian` 后 `sudo systemctl restart meridian` 即可。数据目录 `/opt/meridian/`（含 SQLite 数据库与 `.env`）保持不变。

> 放行防火墙：默认 9090（管理面板）+ 你给每个站点配的 `listen_port`（如 8001-8010）。
> `JWT_SECRET` 必须固定保存，否则每次重启所有会话失效。

### 从源码构建

Debian / 旧系统自带的 Go（如 1.19）**不能**直接 `go build` 本项目。请用仓库自带的 `build.sh`（会在需要时自动下载便携 Go 1.25.x，不污染系统）：

```bash
git clone https://github.com/binaryu/emby-reverse.git && cd emby-reverse
./build.sh
JWT_SECRET=$(openssl rand -hex 32) ./meridian
```

> `build.sh` 会把 `go.mod` 里的语言版本写成两位形式 `go 1.25`（避免 `1.25.0` 被 Go < 1.21 直接判非法），并用 `CGO_ENABLED=0` 产出静态二进制。
>
> 若本机已是 Go ≥ 1.25，也可以：`CGO_ENABLED=0 go build -o meridian .`

部署完成后访问 `http://你的IP:9090`，首次打开会引导设置管理员密码。

---

## 配置

### 命令行参数

```bash
./meridian                          # 默认 :9090，数据库在当前目录
./meridian --port 8080              # 自定义端口
./meridian --db /data/meridian.db   # 自定义数据库路径
```

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `9090` | 管理面板监听端口 |
| `DB_PATH` | `meridian.db` | SQLite 数据库路径 |
| `JWT_SECRET` | 进程启动时随机生成 | JWT 签名密钥。**生产环境必须显式设置**，否则每次重启后会话全部失效 |

### Docker Compose

```yaml
services:
  meridian:
    image: ghcr.io/binaryu/emby-reverse:latest
    restart: unless-stopped
    ports:
      - "9090:9090"
      - "8001-8010:8001-8010"
    volumes:
      - meridian-data:/app/data
    environment:
      - JWT_SECRET=your-secret-here  # 替换为一个固定随机字符串

volumes:
  meridian-data:
```

---

## 技术架构

```
┌─────────────────────────────────────────────┐
│                 Meridian                      │
│                                              │
│  ┌──────────┐   ┌──────────────────────────┐ │
│  │ 管理面板  │   │     反代引擎 (per-site)   │ │
│  │ :9090    │   │  :8001  :8002  :800N     │ │
│  │          │   │                          │ │
│  │ REST API │   │  HTTP ──► target_url     │ │
│  │ SSE 推送 │   │  WS   ──► target_url     │ │
│  │ 静态文件  │   │  播放  ──► playback_target_url │ │
│  └──────────┘   └──────────────────────────┘ │
│       │                     │                │
│  ┌──────────────────────────────────────┐    │
│  │            SQLite (嵌入式)            │    │
│  └──────────────────────────────────────┘    │
└─────────────────────────────────────────────┘
```

| 组件 | 技术选型 |
|------|---------|
| 后端 | 单文件 Go（`main.go`），标准库 `net/http` |
| 前端 | 原生 HTML/CSS/JS SPA，hash 路由，`embed.FS` 嵌入 |
| 数据库 | `modernc.org/sqlite`（纯 Go，无 CGO） |
| 认证 | 自实现 HMAC-SHA256 JWT |

### 项目结构

```
emby-reverse/
├── main.go              # 全部后端逻辑（API、反代引擎、诊断、认证）
├── main_test.go
├── web/
│   ├── embed.go          # Go embed 入口
│   └── static/
│       ├── index.html    # SPA 入口
│       ├── css/          # 样式
│       └── js/           # 前端逻辑（按页面拆分）
├── Dockerfile            # 多阶段构建
├── go.mod / go.sum
└── .github/workflows/
    ├── ci.yml            # Push / PR 校验：测试 + 编译
    └── release.yml       # Tag 发布：多平台构建 + Docker 推送 + Release
```

---

## 双上游配置

每个站点可以配置两个上游地址：

| 字段 | 用途 | 示例 |
|------|------|------|
| **回源地址**（`target_url`） | 网页、API、元数据 | `https://emby.example.com` |
| **播放地址**（`playback_target_url`） | 播放、转码、直链下载 | `https://cdn.example.com` |

播放地址为可选项。不设置时所有请求走同一上游。

设置后以下路径会路由到播放上游：
`/Videos/`、`/emby/Videos/`、`/Audio/`、`/emby/Audio/`、`/LiveTV/`、`/emby/LiveTV/`、`/Items/.../Download`

**典型场景**：Emby 主服务器负责 API 和元数据，CDN 或专用媒体服务器负责大文件分发。

---

## 诊断功能说明

| 检测项 | 检测对象 | 含义 | 不代表什么 |
|--------|---------|------|-----------|
| **主回源健康** | 上游 `target_url` | 网络层可达性与探针结果（多探针路径，401/403/404 仍算在线；元数据接口不可用时会回退到目标根路径探针） | 不是端到端的完整业务可用性证明 |
| **播放回源健康** | `playback_target_url` 的实际生效上游 | 基于播放类路径的轻量探针结果（默认使用轻量请求，不做完整媒体拉流） | 不代表媒体链路一定可正常播放 |
| **主回源 TLS** | 主回源 HTTPS 站点证书 | 证书有效期、颁发机构展示 | 不是 Meridian 自己监听端口的证书 |
| **播放回源 TLS** | 播放回源 HTTPS 站点证书 | 仅在播放回源为独立 HTTPS 上游时单独展示 | 不负责自动签发或续期 |
| **请求头配置** | 本地 UA 配置 | 代理将发送给上游的 UA / Client 值 | 不是远端回显验证 |
| **代理状态** | 本地反代进程 | 是否运行、监听端口 | — |

当 `playback_target_url` 为空时，诊断页会明确标记“播放回源回退到主回源”；当它与 `target_url` 相同时，诊断页会复用主回源结果而不重复展示完全相同的诊断块。
播放回源健康会额外展示当前轻量探针的方法、目标 URL 和返回状态，帮助区分“播放路径可达”与“完整播放成功”这两个不同概念。

---

## 运维要点

- **JWT 密钥**：未设置 `JWT_SECRET` 时每次启动生成随机密钥，重启后会话全部失效
- **流量持久化**：每 60 秒刷入 SQLite，异常退出可能丢失最近一分钟计量
- **操作原子性**：站点创建/启停/更新如反代绑定失败，会回滚数据库并返回错误
- **优雅关闭**：收到 `SIGINT`/`SIGTERM` 后先 flush 流量再退出

---

## 验证 & CI/CD

```bash
go test ./...           # 运行测试
go build -o meridian .  # 编译
```

日常 push / pull request 会自动触发：
- `go test ./...`
- `go build -o meridian .`

推送 `v*` 标签时自动触发：
- 多平台构建（linux/amd64、linux/arm64、windows/amd64、darwin/arm64）
- 创建 GitHub Release 并上传二进制
- 构建并推送 Docker 镜像到 `ghcr.io`

---

## V1 定位

Meridian `v1` 明确定位为一个**单管理员、轻量、可直接落地**的 Emby reverse proxy management panel。

- 保留：登录、站点 CRUD、启停、UA 改写、流量统计、双上游、结构化诊断
- 不做：多用户、角色权限、审计日志、Telegram / Webhook 通知
- 目标：先把单文件 Go + 嵌入式 SPA 的简单面板体验收口，而不是提前引入更重的管理系统

## 升级现有实例

升级时建议优先保持这两样东西不变：

- `JWT_SECRET`
- SQLite 数据库文件及其同目录的 `-wal` / `-shm`

推荐步骤：

1. 停止正在运行的 Meridian 服务。
2. 备份当前二进制、数据库文件和 `JWT_SECRET` 所在的环境配置。
3. 替换为新版本二进制或新镜像。
4. 用原来的 `JWT_SECRET` 和数据库重新启动。
5. 登录面板后检查站点列表、端口监听和诊断页。

如果升级后临时忘记保留 `JWT_SECRET`，历史 JWT 会全部失效，表现为所有登录状态需要重新建立。

## 备份与恢复

最小备份集：

- `meridian.db`
- `meridian.db-wal`
- `meridian.db-shm`
- 保存 `JWT_SECRET` 的 `.env`、systemd 环境文件或容器环境配置

恢复步骤：

1. 停止 Meridian。
2. 还原数据库文件到原路径。
3. 还原原来的 `JWT_SECRET`。
4. 启动 Meridian。
5. 验证管理员登录、站点配置和关键代理端口。

如果你使用 Docker，恢复时同样要保留挂载卷里的数据库文件，并继续使用原来的 `JWT_SECRET`。

---

## Roadmap

以下功能尚未实现，列在这里作为未来方向：

- [ ] 多用户 + 角色权限
- [ ] 审计日志
- [ ] Telegram / Webhook 通知

## 限制与注意事项

- 当前只支持单管理员，不支持多用户或角色划分
- 没有审计日志，操作不可追溯
- 没有内置通知能力（无 Telegram / Webhook 集成）
- TLS 诊断是只读展示，不管证书签发和续期
- UA 诊断是本地配置预览，不验证远端实际收到的请求头

## 开发须知

- 后端代码保持在 `main.go`，不拆分文件
- 前端使用 hash 路由（`#/dashboard`、`#/sites`、`#/diagnostics`）
- API 认证使用 JWT Bearer Token
- SQLite 驱动名为 `sqlite`（不是 `sqlite3`）
- 静态资源通过 `go:embed` 嵌入二进制

## 参与贡献

请参阅 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 安全问题

请参阅 [SECURITY.md](SECURITY.md)。

## License

MIT
