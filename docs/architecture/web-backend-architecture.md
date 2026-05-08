# PicoClaw Web Backend 架构详解

> 本文档面向希望深入理解 PicoClaw Web Launcher 后端实现的学习者。
> 对应目录：`web/backend/` —— 一个用纯 Go 编写的、嵌入 React SPA 的 Web 控制台与 Gateway 进程管理器。

---

## 1. 项目定位与职责边界

Web Backend 是 PicoClaw 的**图形化管理入口**，它本身不是 Agent 的核心，而是核心（`picoclaw gateway`）的"启动器 + 看板"。

| 职责 | 说明 |
|------|------|
| **Web UI 托管** | 将 Vite 构建的 React SPA 通过 `embed.FS` 打包进 Go 二进制，提供单页应用服务 |
| **Gateway 进程管理** | 启动、停止、重启、attach `picoclaw gateway` 子进程，监控其健康状态 |
| **配置管理** | 提供 REST API 读写 `config.json`，支持完整替换（PUT）和增量补丁（PATCH） |
| **聊天通道代理** | 将前端的 WebSocket (`/pico/ws`) 和 OpenResponses HTTP (`/api/openresponses/chat`) 反向代理到 Gateway |
| **会话历史浏览** | 读取本地 JSONL 会话文件，向前端提供对话记录 |
| **系统托盘** | GUI 模式下提供系统托盘图标和菜单（依赖 CGO） |
| **Dashboard 认证** | 基于 bcrypt 的密码登录，Cookie 会话管理 |

**关键边界**：Web Backend 不直接调用 LLM，不执行 Agent 循环。这些由它管理的 Gateway 子进程完成。

---

## 2. 目录结构总览

```
web/backend/
├── main.go                          # 入口：参数解析、初始化、启动服务器
├── app_runtime.go                   # 运行时：优雅关闭、打开浏览器
├── embed.go                         # 前端静态资源嵌入（go:embed dist）
├── systray.go                       # 系统托盘（CGO 构建）
├── systray_stub_nocgo.go            # 无 CGO 时的托盘存根
├── i18n.go                          # 国际化文案
│
├── api/
│   ├── router.go                    # Handler 定义与路由总表
│   ├── config.go                    # 配置 CRUD + 验证 + JSON Merge Patch
│   ├── gateway.go                   # Gateway 子进程生命周期管理（核心）
│   ├── pico.go                      # Pico 通道配置、WebSocket 反向代理
│   ├── openresponses.go             # OpenResponses 通道配置、HTTP 反向代理
│   ├── session.go                   # 会话历史读取（JSONL 解析）
│   ├── auth.go                      # Dashboard 密码登录 API
│   ├── oauth.go                     # OAuth 登录流程
│   ├── models.go                    # 模型列表 API
│   ├── channels.go                  # 频道目录 API
│   ├── skills.go / tools.go         # 技能与工具 API
│   ├── ui.go                        # UI 辅助 API
│   ├── startup.go                   # 开机自启设置
│   ├── launcher_config.go           # 启动器参数（端口/公网）API
│   ├── update.go                    # 自更新检查
│   ├── version.go                   # 版本信息
│   ├── weixin.go / wecom.go         # 微信/企业微信扫码登录
│   └── *_test.go                    # 各模块单元测试
│
├── middleware/
│   ├── middleware.go                # Recoverer / Logger / JSONContentType
│   ├── launcher_dashboard_auth.go   # Dashboard Cookie 认证 + 本地自动登录
│   ├── access_control.go            # CIDR IP 白名单
│   └── referrer_policy.go           # Referrer-Policy 响应头
│
├── launcherconfig/
│   ├── config.go                    # launcher-config.json 读写
│   ├── migration.go                 # 旧 token 迁移到密码登录
│   └── password_store.go            # JSON 文件密码存储（降级方案）
│
├── dashboardauth/
│   ├── store.go                     # SQLite + bcrypt 密码存储（首选方案）
│   ├── sql.go                       # SQL schema 和查询语句
│   ├── platform.go                  # 平台判断
│   └── store_unsupported.go         # 不支持平台的存根（返回 ErrUnsupportedPlatform）
│
├── model/
│   └── status.go                    # 简单状态响应结构体
│
├── utils/
│   ├── banner.go                    # ASCII 启动横幅
│   ├── onboard.go                   # 自动运行 picoclaw onboard
│   └── runtime.go                   # 打开浏览器、获取本地 IP、查找二进制
│
├── dist/                            # Vite 构建产物（由 embed.go 嵌入）
└── winres/                          # Windows 资源文件
```

---

## 3. 核心架构分层详解

### 3.1 入口层 — `main.go`

这是整个应用的**初始化编排器**，按顺序完成以下工作：

```
1. 解析 CLI  flags
   ├── -port    监听端口（默认 18800）
   ├── -host    显式绑定主机（覆盖 -public）
   ├── -public  监听所有接口（默认仅 localhost）
   ├── -console 终端模式（无 GUI）
   ├── -no-browser  禁止自动打开浏览器
   └── -d / -debug  调试日志

2. 初始化日志系统
   ├── panic log 文件
   ├── 若 GUI 模式：禁用控制台输出，写入 launcher.log
   └── 若 debug 模式：日志级别设为 DEBUG

3. 解析并确认配置路径
   └── 若不存在 → 调用 utils.EnsureOnboarded() 自动初始化

4. 加载 launcher-config.json（启动器专属配置）
   └── 决定最终生效的 port / public 值

5. 打开网络监听器
   └── 通过 pkg/netbind 支持多地址族（IPv4/IPv6）、显式 host、通配符绑定

6. 初始化认证系统
   ├── 生成随机 session cookie 值（32 字节 base64）
   ├── 打开 dashboardauth SQLite 存储
   ├── 若 SQLite 不可用 → 降级到 launcherconfig.PasswordStore（JSON 文件）
   └── 迁移旧版 launcher_token 到密码登录

7. 判断首次启动状态
   ├── 未初始化密码 → needsInitialSetup = true（跳转 /launcher-setup）
   └── 已初始化 + loopback + 非 no-browser → 生成一次性本地自动登录链接

8. 组装 HTTP 处理链
   ├── 注册 /api/auth/* 路由（公开）
   ├── 注册 API 业务路由（需认证）
   ├── 注册嵌入前端路由
   └── 包裹中间件栈

9. 启动服务器（多监听器 goroutine）

10. 启动 Gateway（后台 goroutine，延迟 1 秒）
    └── apiHandler.TryAutoStartGateway()

11. 进入主循环
    ├── Console 模式：等待 SIGINT/SIGTERM
    └── GUI 模式：启动系统托盘
```

**关键设计**：启动器与 Gateway 是**父子进程关系**。Launcher 负责生命周期，Gateway 负责业务。Launcher 通过 PID 文件和 health 端点感知 Gateway 状态。

---

### 3.2 API 层 — `api/`

所有 HTTP 端点由 `Handler` 结构体统一管理。

#### Handler 结构体 (`router.go`)

```go
type Handler struct {
    configPath           string   // 主配置文件路径
    serverPort           int      // 当前监听端口
    serverPublic         bool     // 是否公网监听
    serverPublicExplicit bool     // -public 是否显式传入
    serverHostInput      string   // 显式绑定的 host 输入
    serverHostExplicit   bool     // -host 是否显式传入
    serverCIDRs          []string // 允许的 CIDR
    debug                bool
    oauthMu              sync.Mutex
    oauthFlows           map[string]*oauthFlow   // 进行中的 OAuth 流程
    oauthState           map[string]string       // OAuth state 防 CSRF
    weixinMu             sync.Mutex
    weixinFlows          map[string]*weixinFlow  // 微信扫码登录流程
    wecomMu              sync.Mutex
    wecomFlows           map[string]*wecomFlow   // 企业微信扫码登录流程
}
```

`RegisterRoutes` 按功能模块分组注册，路由总表如下：

| 模块 | 路由文件 | 核心端点 |
|------|---------|---------|
| 配置 | `config.go` | `GET/PUT/PATCH /api/config` |
| Pico 通道 | `pico.go` | `GET /api/pico/info`, `GET /pico/ws` (WebSocket 代理) |
| OpenResponses | `openresponses.go` | `POST /api/openresponses/chat` (HTTP 代理) |
| Gateway | `gateway.go` | `POST /api/gateway/start|stop|restart`, `GET /api/gateway/status|logs` |
| 会话 | `session.go` | `GET /api/sessions`, `GET/DELETE /api/sessions/{id}` |
| 认证 | `auth.go` | `POST /api/auth/login|logout|setup`, `GET /api/auth/status` |
| 模型 | `models.go` | 模型列表 CRUD |
| 频道 | `channels.go` | 频道目录和配置 |
| 技能/工具 | `skills.go`, `tools.go` | 技能发现、安装、工具调用 |

#### 配置管理 — `config.go`

配置 API 是整个系统最复杂的部分之一，因为它要处理**安全字段的恢复**。

**PUT** (`handleUpdateConfig`)：
1. 读取请求体 JSON（上限 1MB）
2. `normalizeChannelArrayFields`：将前端可能传成逗号分隔字符串的数组字段规范化
3. 反序列化为 `config.Config` 结构体
4. `SecurityCopyFrom`：从磁盘现有配置复制安全凭证（避免 JSON 序列化时丢失）
5. `applyConfigSecretsFromMap`：通过反射将原始 JSON 中的 SecureString 字段写回结构体
6. `validateConfig`：业务规则验证（如启用 Telegram 必须填 token）
7. 保存到磁盘

**PATCH** (`handlePatchConfig`)：
- 使用 **JSON Merge Patch (RFC 7396)** 语义
- `null` 值表示删除键
- 嵌套对象递归合并
- 之后走与 PUT 相同的安全字段恢复和验证流程

```go
func mergeMap(dst, src map[string]any) {
    for key, srcVal := range src {
        if srcVal == nil {
            delete(dst, key)          // RFC 7396: null = delete
            continue
        }
        srcMap, srcIsMap := srcVal.(map[string]any)
        dstMap, dstIsMap := dst[key].(map[string]any)
        if srcIsMap && dstIsMap {
            mergeMap(dstMap, srcMap)  // 递归合并
        } else {
            dst[key] = srcVal         // 覆盖
        }
    }
}
```

#### Gateway 管理 — `gateway.go`

这是 Web Backend 的核心业务模块。它维护一个**全局状态变量** `gateway`（包级 var），用于追踪子进程：

```go
var gateway = struct {
    mu                  sync.Mutex
    cmd                 *exec.Cmd          // 当前管理的进程
    owned               bool               // true=我启动的, false=attach 的
    bootDefaultModel    string             // 启动时的默认模型
    bootConfigSignature string             // 启动时的配置签名（用于检测变更）
    runtimeStatus       string             // stopped|starting|restarting|running|error
    startupDeadline     time.Time          // 启动超时截止时间
    logs                *LogBuffer         // 内存日志缓冲区（200 行）
    pidData             *ppid.PidFileData  // 从 PID 文件读取的数据
    picoToken           string             // 缓存的 Pico 通道 token（用于代理）
    openResponsesToken  string             // 缓存的 OpenResponses token
}{
    runtimeStatus: "stopped",
    logs:          NewLogBuffer(200),
}
```

**Gateway 状态检测链路**：

```
1. 读取 PID 文件 → 获取 PID + port + host + version
2. 验证 PID 有效性
   ├── 平台支持时：检查进程命令行是否包含 "gateway"
   └── 平台不支持时：回退到 health 端点探测
3. 若 PID 有效 → attach（状态变为 running）
4. 若 PID 无效或不存在 → 报告 stopped
```

**启动流程** (`startGatewayLocked`)：

```
1. 加载配置，获取默认模型名
2. 若传了 existingPid > 0 → attach 到已有进程
3. 否则启动新进程
   ├── 查找 picoclaw 可执行文件路径
   ├── 构造命令: picoclaw gateway -E [-d]
   ├── 设置环境变量: PICOCLAW_CONFIG=xxx, PICOCLAW_GATEWAY_HOST=xxx
   ├── 创建 stdout/stderr pipe
   ├── 清空旧日志缓冲区
   ├── 确保 Pico/OpenResponses 通道已配置（自动生成 token）
   └── cmd.Start()
4. 保存 cmd 引用，状态设为 starting
5. 后台 goroutine 读取 stdout/stderr → LogBuffer
6. 后台 goroutine 执行 cmd.Wait() → 进程退出时清理状态
7. 后台 goroutine 轮询 pidFile / health（最多 15 秒）
   ├── 发现 pidFile 且 PID 匹配 → 状态 running，缓存 token
   └── health 可达但无 pidFile → 也标记 running（降级）
```

**配置签名检测**：

当用户修改配置后，Web UI 会提示 "Gateway restart required"。这通过比较**启动时的配置签名**与**当前配置签名**实现：

```go
func computeConfigSignature(cfg *config.Config) string {
    // 包含：默认模型名 + 所有启用的工具列表
    // 例如: "model:gpt-4;tools:read_file,write_file,exec,web"
}
```

#### Pico 通道代理 — `pico.go`

前端通过 WebSocket 与 Agent 实时聊天，但 Gateway 运行在另一个端口（默认 18790）。Pico 代理将它们桥接起来：

```
前端 ──WebSocket──> Launcher :18800/pico/ws
                        │
                        ▼
               ReverseProxy (httputil)
                        │
                        ▼
               Gateway :18790/pico/ws
```

代理时注入 `Sec-Websocket-Protocol: token.<picoToken>` header，Gateway 据此认证。

Token 管理：
- `GET /api/pico/info`：返回 ws_url、enabled、configured（不暴露 token 明文）
- `POST /api/pico/token`：重新生成随机 token 并保存
- `POST /api/pico/setup`：自动启用 Pico 通道并生成 token

#### 会话历史 — `session.go`

PicoClaw 的会话以 **JSONL**（每行一个 JSON）格式存储在 `workspace/sessions/` 下。

会话文件布局：
```
sessions/
├── <session_key>.jsonl      # 消息历史（每行一个 providers.Message）
└── <session_key>.meta.json  # 元数据（摘要、创建时间、skip 计数等）
```

`session.go` 的职责：
1. **扫描目录**：发现所有 Pico 相关的会话（通过 key 前缀 `agent:main:pico:direct:pico:` 或 scope 元数据）
2. **读取消息**：解析 JSONL，过滤不可见消息（如 tool 调用内部消息、空思考消息）
3. **构建预览**：提取第一条用户消息作为会话标题预览
4. **分页返回**：支持 offset/limit 分页

消息过滤规则（`visibleSessionMessages`）：
- `tool` 角色消息：隐藏
- `assistant` 仅包含 reasoning 无内容：隐藏（瞬态思考）
- 助手消息内容与 tool 摘要重复：去重
- `send_file` 工具：若文件是图片，内联为 base64 data URL

---

### 3.3 中间件层 — `middleware/`

中间件采用**洋葱模型**，按顺序包裹 Handler：

```go
handler := middleware.Recoverer(
    middleware.Logger(
        middleware.ReferrerPolicyNoReferrer(
            middleware.JSONContentType(
                dashAuth          // LauncherDashboardAuth
            )
        )
    )
)
// 注意：IPAllowlist 在 dashAuth 之前包裹
```

#### Logger (`middleware.go`)

```go
func Logger(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
        next.ServeHTTP(rec, w)
        logger.DebugC("http", "METHOD PATH STATUS DURATION")
    })
}
```

使用自定义 `responseRecorder` 捕获状态码，同时实现了 `http.Flusher`、`http.Hijacker`、`Unwrap` 接口，确保 WebSocket 升级和底层功能不被破坏。

#### Recoverer

`defer recover()` 捕获 panic，返回 JSON 格式的 500 错误，打印调用栈。

#### LauncherDashboardAuth (`launcher_dashboard_auth.go`)

认证检查逻辑：

```go
func LauncherDashboardAuth(cfg LauncherDashboardAuthConfig, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := canonicalAuthPath(r.URL.Path)

        // 1. 本地一次性自动登录
        if p == "/launcher-auto-login" {
            handleLauncherLocalAutoLogin(w, r, cfg)
            return
        }

        // 2. 公开路径放行
        if isPublicLauncherDashboardPath(r.Method, p) {
            next.ServeHTTP(w, r)
            return
        }

        // 3. 验证 Cookie
        if validLauncherDashboardAuth(r, cfg) {
            next.ServeHTTP(w, r)
            return
        }

        // 4. 拒绝
        rejectLauncherDashboardAuth(w, r, p)
    })
}
```

**公开路径包括**：
- 静态资源：`/assets/*`, `/favicon.ico`, `/site.webmanifest`...
- SPA 登录页：`/launcher-login`, `/launcher-setup`
- 认证 API：`/api/auth/login|logout|status|setup`

**本地自动登录**：首次启动时，若用户从本机浏览器访问，Launcher 生成一个带 `nonce` 的一次性 URL（有效期 5 分钟）。用户点击后自动获得 session cookie 并重定向到首页。这避免了首次使用时必须手动输入密码的摩擦。

#### IPAllowlist (`access_control.go`)

基于 CIDR 的网络层访问控制：
- 空列表 = 不限制
- Loopback 地址（127.0.0.1, ::1）永远允许
- 非 loopback 请求必须匹配配置的 CIDR 范围
- `/api/*` 返回 JSON 格式 403，其他返回纯文本 403

---

### 3.4 配置与认证存储

#### launcherconfig — 启动器专属配置

文件：`launcher-config.json`，与 `config.json` 放在同一目录。

```go
type Config struct {
    Port                  int      `json:"port"`
    Public                bool     `json:"public"`
    AllowedCIDRs          []string `json:"allowed_cidrs,omitempty"`
    DashboardPasswordHash string   `json:"dashboard_password_hash,omitempty"`
    LegacyLauncherToken   string   `json:"launcher_token,omitempty"` // 只读，用于迁移
}
```

配置优先级：
```
显式 CLI flag > launcher-config.json > 默认值
```

例如：用户用 `-port 8080` 启动，即使 `launcher-config.json` 里写了 18800，也是 8080 生效。

#### dashboardauth — SQLite 密码存储（首选）

```go
type Store struct {
    db   *sql.DB
    path string
}
```

- 使用 `modernc.org/sqlite`（纯 Go 实现，无需 CGO）
- 单表单行：`id=1, hash TEXT`
- bcrypt cost = 12
- 明文密码**永不落盘**
- 构建标签限制：`!mipsle && !netbsd && !(freebsd && arm)` —— 这些平台不支持时编译为存根

#### 认证存储降级链

```
1. 尝试 dashboardauth.New(home) → SQLite Store
   └── 成功 → 使用 SQLite
   └── 失败且是 ErrUnsupportedPlatform → 降级
2. 使用 launcherconfig.NewPasswordStore(path, cfg) → JSON 文件存储
   └── 直接存 bcrypt hash 在 launcher-config.json 的 dashboard_password_hash 字段
3. 若两者都失败 → 认证系统失效，相关 API 返回 503
```

---

### 3.5 前端嵌入层 — `embed.go`

```go
//go:embed all:dist
var frontendFS embed.FS
```

Go 1.16+ 的 `embed` 指令将 `dist/` 目录整体打包进二进制。

路由处理策略：

```go
mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // 1. API 路径不 fallback，正确返回 404
    if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
        http.NotFound(w, r)
        return
    }

    // 2. 存在的静态文件直接服务
    if _, statErr := fs.Stat(subFS, cleanPath); statErr == nil {
        fileServer.ServeHTTP(w, r)
        return
    }

    // 3. 带扩展名的缺失资源 → 404（避免 SPA fallback 掩盖真实 404）
    if strings.Contains(path.Base(cleanPath), ".") {
        fileServer.ServeHTTP(w, r)  // http.FileServer 会返回 404
        return
    }

    // 4. 其余路径 fallback 到 index.html（SPA 路由）
    indexReq := r.Clone(r.Context())
    indexReq.URL.Path = "/"
    fileServer.ServeHTTP(w, indexReq)
}))
```

**细节**：手动注册 SVG MIME 类型。Go 标准库的 `mime.TypeByExtension(".svg")` 返回 `"image/svg"`（不符合 RFC 6838），需纠正为 `"image/svg+xml"`。

---

### 3.6 运行时层

#### 优雅关闭 — `app_runtime.go`

```go
func shutdownApp() {
    // 1. 先关闭 API handler（断开所有 SSE/WebSocket 连接）
    if apiHandler != nil {
        apiHandler.Shutdown()
    }

    // 2. 关闭所有 HTTP 服务器
    for _, srv := range servers {
        srv.SetKeepAlivesEnabled(false)
        ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
        err := srv.Shutdown(ctx)
        cancel()
        // context.DeadlineExceeded 是预期行为（长连接未断开）
    }
}
```

关闭顺序很重要：先断连接，再停服务器，避免客户端收到连接重置。

#### 系统托盘 — `systray.go`

基于 `fyne.io/systray` 库，提供：
- 图标和 Tooltip
- 菜单：打开控制台 / 关于（版本/GitHub/文档）/ 重启 Gateway / 退出
- 点击"打开控制台"调用 `openBrowser()`
- 点击"重启 Gateway"调用 `apiHandler.RestartGateway()`

构建约束：`//go:build !android && ((!darwin && !freebsd) || cgo)`

---

### 3.7 工具与辅助

| 文件 | 职责 |
|------|------|
| `utils/onboard.go` | 若 `config.json` 不存在，自动执行 `picoclaw onboard` 初始化 |
| `utils/runtime.go` | 跨平台打开浏览器、获取本地 IPv4/IPv6 地址、查找 picoclaw 可执行文件 |
| `utils/banner.go` | 控制台启动时的 ASCII 艺术横幅 |
| `model/status.go` | 极简的状态响应结构体 |

---

## 4. 请求生命周期

以**前端保存配置**为例，追踪一个请求的完整旅程：

```
┌─────────────────────────────────────────────────────────────┐
│ 1. 浏览器发送 PATCH /api/config                              │
│    Body: {"agents":{"defaults":{"model":"gpt-4o"}}}         │
└──────────────────┬──────────────────────────────────────────┘
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 2. net/http 接收请求，匹配路由                                │
└──────────────────┬──────────────────────────────────────────┘
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 3. 中间件栈（洋葱模型，从外到内）                            │
│    Recoverer ──► Logger ──► ReferrerPolicy ──►              │
│    JSONContentType ──► LauncherDashboardAuth                │
│         │                                                   │
│         ▼                                                   │
│    检查 Cookie: picoclaw_launcher_auth == 期望值？           │
│         └── 不匹配 → 返回 401 {"error":"unauthorized"}       │
│         └── 匹配 → 继续                                      │
└──────────────────┬──────────────────────────────────────────┘
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 4. IPAllowlist（若配置了 CIDR）                              │
│    RemoteAddr 是 loopback？ → 放行                           │
│    RemoteAddr 在 CIDR 内？   → 放行                           │
│    否则 → 返回 403                                          │
└──────────────────┬──────────────────────────────────────────┘
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 5. Handler.handlePatchConfig                                │
│    a. 读取 Body（上限 1MB）                                  │
│    b. JSON Merge Patch：合并到现有配置                       │
│    c. normalizeChannelArrayFields：规范化数组字段            │
│    d. SecurityCopyFrom：恢复安全凭证                         │
│    e. applyConfigSecretsFromMap：反射恢复 SecureString       │
│    f. validateConfig：业务规则验证                           │
│    g. config.SaveConfig：写入磁盘                            │
│    h. applyRuntimeLogLevel：若 log_level 变更，实时生效      │
└──────────────────┬──────────────────────────────────────────┘
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 6. 返回 200 {"status":"ok"}                                 │
│    JSONContentType 中间件已设置 Content-Type: application/json│
└─────────────────────────────────────────────────────────────┘
```

---

## 5. Gateway 子进程管理详解

### 5.1 为什么需要进程管理？

PicoClaw 的核心 Agent 逻辑在 `pkg/agent/` 中，由 `picoclaw gateway` 子命令启动。Web Backend 不直接链接这些代码，而是通过**子进程隔离**实现：
- Launcher 可以独立重启而不影响 Gateway
- Gateway 崩溃不会拖垮 Web UI
- 用户可以在 Web UI 中控制 Gateway 启停

### 5.2 进程状态机

```
                    ┌─────────────┐
                    │   stopped   │
                    └──────┬──────┘
                           │ TryAutoStart / API start
                           ▼
                    ┌─────────────┐     15s 内未就绪
                    │  starting   │────────────────►┌─────────┐
                    └──────┬──────┘                 │  error  │
                           │ pidFile 检测到         └────┬────┘
                           │ 或 health 200              │
                           ▼                            │
                    ┌─────────────┐                     │
         ┌─────────│   running   │◄────────────────────┘
         │         └──────┬──────┘    API restart
         │                │
         │                │ API stop / 进程退出
         │                ▼
         │         ┌─────────────┐
         └────────►│   stopped   │
                   └─────────────┘
```

### 5.3 PID 文件协议

Gateway 启动后会写入 `~/.picoclaw/picoclaw.pid.json`：

```json
{
  "pid": 12345,
  "port": 18790,
  "host": "127.0.0.1",
  "version": "1.2.3",
  "token": "xxx"
}
```

Launcher 通过读取此文件感知 Gateway 的存在，无需一直持有进程引用。

### 5.4 Attach vs Own

| 模式 | 触发条件 | 行为 |
|------|---------|------|
| **Own** | Launcher 自己 `exec.Command` 启动的 | 停止时会发送 SIGTERM/SIGKILL；进程退出自动清理状态 |
| **Attach** | 发现已有 PID 文件，进程正在运行 | 停止时**不**杀进程（只是解除追踪）；用于用户手动启动 gateway 的场景 |

### 5.5 日志缓冲区

`LogBuffer` 是一个线程安全的循环缓冲区：
- 容量：200 行
- 每次 Gateway 启动生成新的 `runID`（自增整数）
- 前端通过 `GET /api/gateway/logs?log_offset=0&log_run_id=1` 增量拉取
- 若 `runID` 变化（Gateway 重启），前端重置 offset

---

## 6. 认证与授权系统

### 6.1 认证流程

```
首次启动
   │
   ▼
┌─────────────────────┐
│ 密码是否已初始化？   │
└─────────────────────┘
   │ 否                    │ 是
   ▼                       ▼
浏览器打开              检查是否是 loopback
/launcher-setup         │
   │                    │ 是              │ 否
   ▼                    ▼                ▼
用户设置密码      生成一次性 auto-login  跳转 /launcher-login
                        │                用户输入密码
                        ▼                      │
               访问 /launcher-auto-login       ▼
               ?nonce=xxx               POST /api/auth/login
                        │                      │
                        ▼                      ▼
               验证 nonce → 设置 Cookie  验证 bcrypt → 设置 Cookie
                        │                      │
                        └──────────┬───────────┘
                                   ▼
                            后续请求携带
                            Cookie: picoclaw_launcher_auth=xxx
```

### 6.2 安全设计要点

1. **bcrypt cost = 12**：平衡安全性和性能
2. **常量时间比较**：`subtle.ConstantTimeCompare` 防止时序攻击
3. **登录限流**：基于 IP 的速率限制，防止暴力破解
4. **HttpOnly Cookie**：前端 JS 无法读取，防 XSS 窃取
5. **Secure 标志**：TLS 或反向代理时自动启用
6. **SameSite=Lax**：防 CSRF
7. **密码最小 8 字符**：setup 时强制校验

---

## 7. 配置管理设计

### 7.1 双文件配置

| 文件 | 内容 | 安全级别 |
|------|------|---------|
| `config.json` | 所有非敏感配置：模型、频道、工具开关 | 普通 |
| `.security.yml` | API Key、Token、Secret | 敏感（建议严格权限） |

### 7.2 SecureString 机制

配置结构体中的敏感字段使用 `config.SecureString` 类型：
- JSON 序列化时输出 `""`（或省略）
- 从 `.security.yml` 读取真实值
- 反序列化后通过 `SecurityCopyFrom` 恢复

### 7.3 配置热重载

Web Backend 修改配置后：
1. **Gateway 不自动重启** —— 通过 `gateway_restart_required` 标志提示用户
2. **日志级别实时生效** —— `applyRuntimeLogLevel()` 立即调用
3. **Token 缓存刷新** —— Pico/OpenResponses token 修改后，下次代理请求自动读取新配置

---

## 8. 构建标签与平台适配

Web Backend 使用 Go 构建标签实现多平台适配：

| 文件 | 构建标签 | 说明 |
|------|---------|------|
| `systray.go` | `!android && ((!darwin && !freebsd) \|\| cgo)` | 需要 CGO 的平台才编译系统托盘 |
| `systray_stub_nocgo.go` | `android \|\| (darwin && !cgo) \|\| (freebsd && !cgo)` | 无 CGO 时的空实现 |
| `dashboardauth/store.go` | `!mipsle && !netbsd && !(freebsd && arm)` | 这些平台不支持 modernc.org/sqlite |
| `dashboardauth/store_unsupported.go` | `mipsle \|\| netbsd \|\| (freebsd && arm)` | 返回 ErrUnsupportedPlatform |
| `systray_icon_windows.go` | `windows` | Windows 图标资源加载 |
| `systray_icon_nonwindows.go` | `!windows` | 其他平台图标加载 |

---

## 9. 学习路径建议

如果你希望按**由浅入深**的顺序阅读代码，建议如下：

### 第一阶段：整体脉络（30 分钟）
1. `main.go` —— 理解启动流程和初始化顺序
2. `api/router.go` —— 看路由总表，知道有哪些功能模块
3. `embed.go` —— 理解前端是如何被打包进 Go 二进制的

### 第二阶段：HTTP 层（1 小时）
4. `middleware/middleware.go` —— 基础中间件（Logger、Recoverer）
5. `middleware/launcher_dashboard_auth.go` —— 认证逻辑
6. `middleware/access_control.go` —— IP 白名单
7. `api/auth.go` —— 登录/登出/设置密码的 API 实现

### 第三阶段：核心业务（2 小时）
8. `api/config.go` —— 配置 CRUD，重点理解 JSON Merge Patch 和 SecureString 恢复
9. `api/gateway.go` —— **最核心的模块**，理解进程管理、状态机、PID 文件协议
10. `api/pico.go` —— WebSocket 代理和 token 管理
11. `api/session.go` —— JSONL 会话文件的读取和消息过滤

### 第四阶段：支撑模块（1 小时）
12. `launcherconfig/config.go` —— 启动器配置模型
13. `dashboardauth/store.go` —— SQLite 密码存储
14. `app_runtime.go` —— 优雅关闭
15. `systray.go` —— 系统托盘（可选，非核心）

### 第五阶段：测试与边界
16. 阅读各 `*_test.go` 文件，理解模块的测试策略
17. 注意各文件的 `//go:build` 标签，理解跨平台适配思路

---

## 10. 常见问题速查

**Q: Web Backend 和 Gateway 是什么关系？**
> Launcher（Web Backend）是 Gateway 的"启动器 + 看板"。Gateway 是实际的 AI Agent 服务。Launcher 通过子进程管理 Gateway，并通过反向代理将前端流量转发给 Gateway。

**Q: 为什么配置修改后 Gateway 不自动重启？**
> 这是有意设计。Gateway 重启会中断正在进行的对话。Web UI 通过 `gateway_restart_required` 标志提示用户手动重启，由用户决定时机。

**Q: 密码存在哪里？**
> 首选 `~/.picoclaw/dashboard-auth.db`（SQLite + bcrypt）。若平台不支持 SQLite，降级到 `launcher-config.json` 的 `dashboard_password_hash` 字段。

**Q: 前端如何与 Gateway 通信？**
> 前端不直接连接 Gateway。WebSocket 连接到 Launcher 的 `/pico/ws`，Launcher 通过 `httputil.ReverseProxy` 转发到 Gateway 的 WebSocket 端点，并自动注入认证 token。

**Q: 会话历史存在哪里？**
> `~/.picoclaw/workspace/sessions/` 下的 JSONL 文件。每条消息一行 JSON。Web Backend 直接读取这些文件，不经过 Gateway。

---

*文档版本：对应 PicoClaw dev-lowe-4023 分支代码*
