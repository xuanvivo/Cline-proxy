# Cline Go Proxy

当前版本：**v1.2.0** · [发行说明](https://github.com/xuanvivo/Cline-proxy/releases) · Go 1.26+（Docker 使用 Go 1.27.3）

> 本项目基于 [YuJunZhiXue/Cline-proxy](https://github.com/YuJunZhiXue/Cline-proxy) 二次开发，**原作者：[@YuJunZhiXue](https://github.com/YuJunZhiXue)**，在此致谢。
> 本仓库在原项目基础上增加了管理后台登录页（替代浏览器 Basic Auth 弹框）、退出登录、可配置的管理路径与凭据等公网部署加固功能。

Cline API 的反向代理服务，支持多账号轮询、OpenAI 和 Anthropic Messages API 双协议、API Key 鉴权，内置中文管理后台。

## 功能

- **双协议兼容**：同时支持 `/v1/chat/completions`（OpenAI）和 `/v1/messages`（Anthropic Messages API）
- **多账号轮询**：自动在多个 Cline 账号间切换负载（支持 `round_robin` / `fill` / `random` 策略）
- **最新版本自动检测**：启动及每 6 小时检查本项目和 Cline CLI 的最新稳定版，后台支持立即检测、查看发行说明和一键应用 CLI 身份请求头；独立识别 CLI 发布标签
- **免费模型自动同步**：从 Cline 官方同步免费模型列表（每小时一次，也可手动同步），只收录不扣点数的免费模型；默认模型可在后台编辑
- **中文管理后台**：浏览器访问管理路径即可管理账号、API Key、模型配置、请求头、代理设置
- **后台登录页**：风格统一的深色登录页 + Cookie 会话（7 天有效），不再弹 Basic Auth 认证框；Basic Auth 仍保留，兼容 curl / 脚本调用；侧边栏提供退出登录
- **公网部署加固**：管理路径、用户名、密码均可通过环境变量配置；登录失败 5 次锁定 IP 15 分钟，防止爆破
- **API Key 鉴权**：保护代理端点，支持生成/删除多个 API Key
- **System Prompt 覆盖**：项目目录下放 `override.md` 则自动替换系统提示词，不存在则使用客户端自带
- **账号导入**：支持 OAuth 浏览器登录、手动 Token 输入、批量文件导入
- **持久化存储**：账号、Key 和后台设置保存在 `.cline-accounts.json`；文件无法写入（如 Docker 把它建成了目录）或内容损坏时拒绝启动并提示修复方法，不会让数据只存在内存里、重启后丢失

## 快速开始

### 直接运行

```bash
# 编译并启动（默认端口 3457）
go build -o cline-proxy .
./cline-proxy

# 查看程序版本和构建信息
./cline-proxy -version

# 指定端口
./cline-proxy -port 3457
```

启动后访问 `http://127.0.0.1:3457/admin/`，用默认账号 `admin` / `changeme` 登录后台（生产环境务必修改，见下方环境变量）。

### Docker 部署

```bash
# 首次部署前先建好这三个文件（必须）：宿主机上不存在时，Docker 会把挂载点建成同名目录，账号数据就无法保存
touch .cline-accounts.json .admin-sessions.json override.md

docker compose up -d      # 构建并启动
docker compose logs -f    # 查看日志
docker compose down       # 停止
```

新版 Compose 会在挂载源文件不存在时直接报错，避免 Docker 自动创建同名目录。容器内置健康检查，可用 `docker compose ps` 查看状态。

旧版部署如果日志提示 `.cline-accounts.json is a directory, not a file`，说明之前没建文件、Docker 建成了目录。在项目目录执行：

```bash
docker compose down
for f in .cline-accounts.json .admin-sessions.json override.md; do [ -d "$f" ] && rmdir "$f"; touch "$f"; done
docker compose up -d
```

更新代码：`git pull && docker compose up -d --build`，账号数据保存在宿主机文件里，重建容器不受影响。

需要修改宿主机端口时，在项目目录的 `.env` 文件中设置 `HOST_PORT=53457`；容器内部仍使用 `3457`，无需改动仓库中的 Compose 文件。

公网部署时，新建 `docker-compose.override.yml`（已被 `.gitignore` 排除，不会提交）覆盖默认凭据：

```yaml
services:
  cline-proxy:
    environment:
      - PORT=3457
      - ADMIN_PATH=<随机字符串，作为后台隐藏路径>
      - ADMIN_USER=<用户名>
      - ADMIN_PASS=<强密码>
```

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `3457` | 监听端口 |
| `HOST_PORT` | `3457` | Docker Compose 发布到宿主机的端口，在 `.env` 中设置 |
| `ADMIN_PATH` | `admin` | 管理后台路径（`/<ADMIN_PATH>/`），公网建议改为随机字符串 |
| `ADMIN_USER` | `admin` | 后台登录用户名 |
| `ADMIN_PASS` | `changeme` | 后台登录密码 |
| `VERSION_CHECK_ENABLED` | `true` | 是否启动定时版本检测；设为 `false` 关闭自动检测，仍可手动检测 |

## 使用指南

### 1. 登录后台

访问 `http://<host>:<port>/<ADMIN_PATH>/`，未登录会跳转到登录页。登录会话有效期 7 天，持久化在 `.admin-sessions.json`（已 gitignore），服务重启后无需重新登录；侧边栏底部可退出登录。同一 IP 连续 5 次密码错误会被锁定 15 分钟。

### 2. 添加 Cline 账号

在管理后台 **账号管理** → **导入账号**，选择以下任一方式：

- **OAuth 浏览器登录**：点击按钮弹出 WorkOS 登录窗口，完成后自动填入
- **手动输入 Token**：输入已有账号的 Access Token
- **批量文件导入**：上传包含账号数据的 JSON 文件

### 3. 配置客户端

应用（如 Claude Code、Cline）配置为使用此代理：

```
Base URL: http://127.0.0.1:3457/v1
API Key:  <在管理后台生成的 Key>
Model:    cline-free/nemotron-3.5-super-vl # 任选后台「可用模型」中的免费模型
```

OpenAI 格式（`/v1/chat/completions`）和 Anthropic 格式（`/v1/messages`）均可。

### 4. API Key 管理

在后台 **设置** → **API Keys** 中生成和管理。如果未配置任何 Key，代理允许无鉴权访问。

### 5. System Prompt 覆盖

在项目目录下创建 `override.md`，内容将替换所有客户端请求的系统提示词。删除该文件则使用客户端自带的提示词。

### 6. 请求头配置

后台 **设置** → **请求头** 可编辑转发给上游的自定义请求头（如 `x-client-type: cline-cli`）。

## 可用模型（自动同步）

模型列表自动从 Cline 官方同步，**只同步免费模型**：

- 数据来源：`https://api.cline.bot/api/v1/ai/cline/recommended-models` 的 `free` 分组，即官方 Cline 客户端模型选择器中「Free」栏目的模型
- 不同步 OpenRouter 目录中带 `:free` 后缀的模型（名字带 free，但经 Cline 调用会扣点数）
- 启动时同步一次，之后每小时自动同步；后台 **设置** → **可用模型** 可点「立即同步」；同步失败时保留上一次的列表
- `/v1/models` 返回的就是同步后的免费模型列表

截至 2026-10-23 的免费模型（仅供参考，以后台显示为准）：

| 模型 ID | 说明 |
|---------|------|
| `reflection/beam-free` | Beam |
| `cline-free/nemotron-3.5-super-vl` | Nemotron 3.5 Super VL |
| `nvidia/nemotron-3.5-lightning-free` | Nemotron 3.5 Lightning |
| `poolside/laguna-s-2.1-free` | Laguna S 2.1 |
| `cline-free/solar-mini4` | Solar Mini 4 |

代理不限制模型：客户端也可以直接指定付费模型 ID（如 `anthropic/claude-opus-5.5`），但会扣账户点数；`cline-pass/*` 需要 Cline Pass 订阅。

### 默认模型

在后台 **设置** → **代理配置** → **默认模型** 中编辑（或在模型列表中点「设为默认」），保存后立即生效并持久化。客户端请求未指定 `model` 时使用该模型；留空则自动选用免费列表中第一个 `cline-free/` 模型。

## 自动检测版本

后台 **设置 → 版本更新** 分别展示代理项目与 Cline CLI 的当前版本、最新稳定版、检测时间和错误状态。

- 启动时及每 6 小时自动读取 GitHub 公开发布信息；“立即检测”会复用 5 分钟内的结果，降低限流风险。
- 本项目按 `xuanvivo/Cline-proxy` 的最新稳定 Release 检测；Cline CLI 从 `cline/cline` 发布列表筛选 `cli-v*`，忽略桌面版、插件、SDK、草稿和预发布版，并进行数字版本比较。
- CLI 对应的 Core 版本从同一个发布标签读取。当前内置 CLI `3.0.73` / Core `0.0.95`；升级时会迁移未修改的旧默认请求头，保留自定义值。
- “应用最新 CLI 请求头”会更新 CLI 身份相关的 6 个请求头并持久化，其余请求头保留。自动检测本身不修改请求头；这项操作只更新请求元数据，不代表安装官方 CLI 或保证所有新版协议都兼容。
- 网络失败、限流或数据异常时保留最近一次成功检测结果并显示错误，不影响代理服务。版本请求使用独立 HTTP 客户端，不携带 Cline Token、API Key 或管理员凭据。
- 代理程序升级仍使用 `git pull && docker compose up -d --build`。版本检测不自动下载、执行或替换程序。

管理 API（均需后台会话或 Basic Auth）：`GET /<ADMIN_PATH>/api/version`、`POST /<ADMIN_PATH>/api/version/check`、`POST /<ADMIN_PATH>/api/version/apply-client`。

## 开发与验证

```bash
gofmt -w *.go
go vet ./...
go test -race ./...
go build -trimpath -o cline-proxy .
```

`VERSION` 是代理版本号的唯一来源，健康检查、后台和 `-version` 使用同一版本。构建时可传入 `-ldflags "-X main.buildCommit=<commit> -X main.buildTime=<UTC时间>"`；Docker 提供对应的 `BUILD_COMMIT` 和 `BUILD_TIME` 构建参数。GitHub Actions 会检查格式、静态分析、竞态测试与构建。

## 项目结构

```
├── VERSION             统一的代理版本号
├── version.go          GitHub 版本检测与 CLI 请求头更新
├── version_test.go     版本检测、鉴权、持久化与健康检查回归测试
├── main.go             入口，CLI 参数处理
├── proxy.go            HTTP 服务，API 路由，协议转换，SSE 流式处理
├── admin.go            管理后台 REST API、登录会话
├── admin_html.go       管理后台前端 HTML（嵌入 Go 二进制）
├── login_html.go       登录页 HTML（嵌入 Go 二进制）
├── auth.go             WorkOS OAuth 登录与 Token 刷新
├── pool.go             账号池管理、持久化、策略轮询
├── models.go           免费模型列表同步、默认模型选择
├── types.go            数据结构定义
├── capture.go          OAuth 信息捕获工具
├── http.go             HTTP 客户端与工具函数
├── Dockerfile          Docker 构建
├── docker-compose.yml  Docker Compose 配置
└── override.md         可选的系统提示词覆盖文件
```

## 致谢

- 原项目与原作者：[YuJunZhiXue/Cline-proxy](https://github.com/YuJunZhiXue/Cline-proxy)
- 感谢 [LINUX DO](https://linux.do) 社区
