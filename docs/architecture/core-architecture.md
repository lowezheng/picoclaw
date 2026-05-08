# PicoClaw 核心架构文档

> 本文档从源码层面系统解构 PicoClaw 的核心组件架构，面向希望深入理解系统实现的学习者。

---

## 目录

1. [整体架构概览](#1-整体架构概览)
2. [端到端数据流](#2-端到端数据流)
3. [Agent Loop — 对话引擎核心](#3-agent-loop--对话引擎核心)
4. [Provider 层 — LLM 接入抽象](#4-provider-层--llm-接入抽象)
5. [Channel 层 — 多平台消息接入](#5-channel-层--多平台消息接入)
6. [Routing 层 — 消息路由与模型选择](#6-routing-层--消息路由与模型选择)
7. [Tools 层 — 工具系统](#7-tools-层--工具系统)
8. [Gateway — 服务入口与生命周期](#8-gateway--服务入口与生命周期)
9. [支撑模块](#9-支撑模块)
10. [关键设计模式总结](#10-关键设计模式总结)

---

## 1. 整体架构概览

PicoClaw 采用**分层 + 管道（Pipeline）**的架构设计，各层之间通过清晰的接口解耦。

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              外部平台 / 用户                                  │
│  Telegram  Discord  Slack  WhatsApp  WeChat  微信  飞书  ...                │
└───────────────────────────────┬─────────────────────────────────────────────┘
                                │ 平台协议
                                ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Channel 层 (pkg/channels/)                                                  │
│  ├── 20+ 平台实现 (telegram/discord/slack/...)                              │
│  ├── BaseChannel (通用行为：白名单、群组触发、typing/reaction/placeholder)    │
│  └── Manager (生命周期、worker 队列、消息分发、热重载)                        │
└───────────────────────────────┬─────────────────────────────────────────────┘
                                │ bus.InboundMessage / bus.OutboundMessage
                                ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Routing 层 (pkg/routing/)                                                   │
│  ├── RouteResolver (Dispatch Rules → AgentID + SessionPolicy)               │
│  └── Router + Classifier (Primary/Light 模型智能路由)                        │
└───────────────────────────────┬─────────────────────────────────────────────┘
                                │ ResolvedRoute + SessionPolicy
                                ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Agent Loop (pkg/agent/) — 系统心脏                                         │
│  ├── AgentLoop.Run()     (主事件循环)                                        │
│  ├── Turn 协调器         (runTurn: LLM → Tool → Steering → SubTurn)        │
│  ├── Pipeline            (Setup → CallLLM → ExecuteTools → Finalize)       │
│  ├── Hooks 系统          (BeforeLLM/AfterLLM/BeforeTool/AfterTool/Approve) │
│  ├── Steering 队列       (运行中注入用户消息)                                │
│  └── SubTurn 机制        (嵌套子 Agent 回合)                                 │
└───────────────────────────────┬─────────────────────────────────────────────┘
                                │ LLMRequest (messages, tools, model)
                                ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Provider 层 (pkg/providers/)                                                │
│  ├── 统一接口：LLMProvider / StreamingProvider / ThinkingCapable            │
│  ├── 30+ 协议支持 (OpenAI/Anthropic/Azure/Bedrock/Gemini/CLI/...)           │
│  ├── 工厂模式：CreateProviderFromConfig                                      │
│  └── 弹性设计：FallbackChain + CooldownTracker + ErrorClassifier            │
└───────────────────────────────┬─────────────────────────────────────────────┘
                                │ LLMResponse (content, tool_calls, usage)
                                ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Tools 层 (pkg/tools/)                                                       │
│  ├── ToolRegistry (注册/执行/TTL/版本控制)                                   │
│  ├── 核心工具：exec / read_file / write_file / web_search / web_fetch       │
│  ├── 高级工具：spawn / subagent / cron / skills_install / mcp               │
│  └── ExecTool (最复杂：同步/后台/PTY/会话管理/安全守卫)                       │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│  支撑模块                                                                    │
│  ├── Session  (pkg/session/)    — 作用域分配、Alias 兼容、Identity Link     │
│  ├── Memory   (pkg/memory/)     — JSONL 追加存储、逻辑删除、Summarization   │
│  ├── Config   (pkg/config/)     — 配置解析、版本迁移、安全隔离               │
│  ├── Skills   (pkg/skills/)     — SKILL.md 加载、ClawHub/GitHub Registry    │
│  ├── MCP      (pkg/mcp/)        — Model Context Protocol 客户端            │
│  └── Cron     (pkg/cron/)       — 定时任务调度引擎                         │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│  Gateway (pkg/gateway/) — 服务入口                                          │
│  ├── Run() 编排全生命周期 (配置加载 → 服务启动 → 信号监听 → 优雅关闭)        │
│  ├── 共享 HTTP 服务器 (webhook 接收 + 健康检查)                              │
│  └── 热重载 (文件监控 + HTTP /reload 端点)                                   │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. 端到端数据流

一条消息从用户发出到收到回复的完整流转：

```
[用户发送消息] ──▶ [Telegram Bot API]
                       │
                       ▼
              [TelegramChannel]
              ├── 解析 Update，提取 senderID/chatID/content
              ├── 群组检查：@提及？前缀匹配？
              ├── 白名单检查 (IsAllowedSender)
              └── 自动触发 typing + reaction(👀) + placeholder("Thinking...")
                       │
                       ▼
              bus.PublishInbound()
                       │
                       ▼
              [AgentLoop.Run] 主 goroutine
              ├── 从 bus.InboundChan() 读取
              ├── resolveSteeringTarget → sessionKey + agentID
              ├── activeTurnStates.LoadOrStore (原子抢占，防止并发)
              └── 启动 worker goroutine (受 workerSem 限流)
                       │
                       ▼
              [processMessage]
              ├── 音频转录（如有语音消息）
              ├── routing.ResolveRoute() → AgentID + SessionPolicy
              ├── session.AllocateRouteSession() → Allocation (Scope + Key)
              └── 构建 processOptions
                       │
                       ▼
              [runAgentLoop] → [runTurn] 协调器
                       │
                       ▼
              [Pipeline.SetupTurn]
              ├── 从 SessionStore 读取 history + summary
              ├── ContextBuilder.BuildMessages() (system prompt + history + current)
              └── Router.SelectModel() → Primary or Light model
                       │
                       ▼
              [Pipeline.CallLLM]
              ├── BeforeLLM Hook
              ├── FallbackChain.Execute() (尝试 candidates，失败自动切换)
              ├── 流式输出 (onChunk 回调实时更新 placeholder)
              └── AfterLLM Hook
                       │
                       ▼
              无 ToolCalls? ──Yes──▶ [Pipeline.Finalize]
              │                           ├── 保存 assistant 回复到 session
              │                           ├── 触发 summarization（如需要）
              │                           └── bus.PublishOutbound()
              │                                │
              │                                ▼
              │                       [ChannelManager.dispatchOutbound]
              │                                │
              │                                ▼
              │                       channelWorker.runWorker
              │                       ├── preSend：停止 typing、撤销 reaction
              │                       ├── 编辑/删除 placeholder
              │                       ├── 消息拆分（marker-based + length-based）
              │                       └── sendWithRetry → Channel.Send()
              │                                │
              │                                ▼
              │                       [TelegramChannel.Send] → Telegram Bot API
              │                                │
              │                                ▼
              │                       [用户收到回复]
              │
              No
              │
              ▼
              [Pipeline.ExecuteTools]
              ├── 遍历每个 ToolCall
              ├── BeforeTool Hook → ApproveTool Hook → 工具执行
              ├── AfterTool Hook
              ├── steering 检查（有新消息？跳过剩余工具）
              └── 继续下一轮迭代 (ControlContinue)
```

---

## 3. Agent Loop — 对话引擎核心

`pkg/agent/` 是整个系统的心脏，负责编排完整的对话回合（Turn）。

### 3.1 核心结构体

```go
type AgentLoop struct {
    bus            interfaces.MessageBus       // 消息总线
    registry       *AgentRegistry              // Agent 注册表
    eventBus       *EventBus                   // 内部事件广播
    hooks          *HookManager                // Hook 生命周期管理
    contextManager ContextManager              // 上下文管理（可插拔）
    fallback       *providers.FallbackChain    // Provider 故障转移
    steering       *steeringQueue              // 运行中消息注入队列
    workerSem      chan struct{}              // 并发 worker 信号量
    activeTurnStates sync.Map                  // sessionKey → *turnState
}
```

### 3.2 Turn 状态机

一个 Turn 经历明确的状态转换：

```
          ┌─────────┐
          │  Setup  │  ← SetupTurn：加载历史、构建消息、选择模型
          └────┬────┘
               │
               ▼
          ┌─────────┐
          │ Running │  ← 每次 LLM 调用前
          └────┬────┘
               │
         ┌─────┴─────┐
         │           │
         ▼           ▼
    ┌─────────┐  ┌─────────┐
    │  Tools  │  │Finalizing│  ← ExecuteTools 执行期间 / Finalize 保存结果
    └────┬────┘  └────┬────┘
         │            │
         │            ▼
         │       ┌───────────┐
         │       │ Completed │
         │       └───────────┘
         │
         ▼
    ┌─────────┐
    │ Aborted │  ← HardAbort 取消 context，回滚 session
    └─────────┘
```

### 3.3 Pipeline 四阶段

`runTurn` 将 Turn 拆解为四个清晰的 Pipeline 阶段：

| 阶段 | 文件 | 核心职责 |
|------|------|----------|
| **Setup** | `pipeline_setup.go` | 加载 session 历史、构建消息列表、选择候选模型（Primary/Light） |
| **CallLLM** | `pipeline_llm.go` | Hook 触发、Fallback 链、流式输出、重试（vision/timeout/context） |
| **ExecuteTools** | `pipeline_execute.go` | 工具循环：BeforeTool → ApproveTool → Execute → AfterTool → steering 检查 |
| **Finalize** | `pipeline_finalize.go` | 保存 assistant 回复、触发摘要、发布 outbound 消息 |

### 3.4 Steering 机制

Steering 允许**在 Turn 运行期间注入用户消息**，打断当前工具链：

```go
type steeringQueue struct {
    queues map[string][]providers.Message  // scope → messages
    mode   SteeringMode                    // "one-at-a-time" | "all"
}
```

**注入时机**：
1. **Turn 迭代开始前**
2. **每个工具执行后** — 若检测到 steering，剩余工具调用被跳过，生成占位 tool 结果

这实现了"用户在 AI 执行工具时发送新消息，AI 立即响应新消息"的 UX。

### 3.5 SubTurn 嵌套机制

SubTurn 是 Agent 的**递归执行能力**，允许工具（如 `spawn`）启动独立的子 Agent 回合：

```
Parent Turn (depth=0)
    ├── LLM Call
    ├── Tool: spawn()
    │       └── SubTurn (depth=1, ephemeral session)
    │               ├── 独立 child context（可 Critical：父结束后继续）
    │               ├── 内存-only session（最多 50 条，不持久化）
    │               ├── Token Budget 级联扣减（atomic.Int64 共享）
    │               └── 深度限制 maxDepth=3
    │
    ├── 轮询 SubTurn 结果 (pendingResults channel)
    └── 继续 Parent Turn
```

### 3.6 Hooks 系统

HookManager 支持四种类型的 Hook，按优先级排序执行：

| Hook 类型 | 接口 | 触发点 | 超时 |
|-----------|------|--------|------|
| **EventObserver** | `OnEvent(ctx, evt)` | 异步事件订阅 | 500ms |
| **LLMInterceptor** | `BeforeLLM/AfterLLM` | LLM 调用前后 | 5s |
| **ToolInterceptor** | `BeforeTool/AfterTool` | 工具执行前后 | 5s |
| **ToolApprover** | `ApproveTool(ctx, req)` | 工具审批 | 60s |

**Hook 决策**：`Continue` / `Modify` / `Respond` / `DenyTool` / `AbortTurn` / `HardAbort`

---

## 4. Provider 层 — LLM 接入抽象

`pkg/providers/` 将 30+ LLM 协议统一为清晰的接口。

### 4.1 核心接口

```go
type LLMProvider interface {
    Chat(ctx, messages, tools, model, options) (*LLMResponse, error)
    GetDefaultModel() string
}

type StreamingProvider interface {
    ChatStream(ctx, messages, tools, model, options, onChunk) (*LLMResponse, error)
}

type ThinkingCapable interface {
    SupportsThinking() bool  // Anthropic Extended Thinking
}
```

### 4.2 统一协议类型

所有 Provider 共享 `protocoltypes` 中的统一数据模型：

```go
type Message struct {
    Role             string
    Content          string
    Media            []string       // 多模态 data URL
    SystemParts      []ContentBlock // Anthropic cache_control
    ToolCalls        []ToolCall
    ToolCallID       string
}

type LLMResponse struct {
    Content       string
    ToolCalls     []ToolCall
    FinishReason  string
    Usage         *UsageInfo
    ReasoningContent string
}
```

### 4.3 工厂模式

`CreateProviderFromConfig` 通过大型 switch 将配置映射到具体实现：

```
配置 protocol: "openai" ──▶ openai_compat.Provider (HTTP, 被 30+ 协议复用)
配置 protocol: "anthropic" ──▶ anthropic.Provider (官方 SDK)
配置 protocol: "gemini" ──▶ httpapi.GeminiProvider (原生 API)
配置 protocol: "bedrock" ──▶ bedrock.Provider (AWS SDK)
配置 protocol: "claude-cli" ──▶ cli.ClaudeCliProvider (子进程)
```

### 4.4 弹性设计

```
FallbackChain
    ├── CooldownTracker        (指数退避冷却：1min → 5min → 25min → 1h)
    ├── ErrorClassifier        (将错误分类为 auth/rate_limit/billing/network/...)
    └── RateLimiter            (Token-bucket RPM 限流)
```

---

## 5. Channel 层 — 多平台消息接入

`pkg/channels/` 实现 20+ 聊天平台的统一接入。

### 5.1 核心接口

```go
type Channel interface {
    Name() string
    Start(ctx) error
    Stop(ctx) error
    Send(ctx, bus.OutboundMessage) ([]string, error)
    IsAllowedSender(sender bus.SenderInfo) bool
}
```

### 5.2 BaseChannel 基类

所有具体 channel 嵌入 `*channels.BaseChannel`，获得通用能力：

- **白名单**：支持 `"*"` 通配符、`"id|username"` 复合 ID、跨平台 identity link
- **群组触发**：@提及 → 始终响应；`mention_only` 配置；前缀匹配
- **自动 UX**：typing 指示器 + 消息反应(👀) + placeholder("Thinking...")

### 5.3 Manager 消息分发

```
Manager
    ├── channels: map[name]Channel          // 所有 channel 实例
    ├── workers:  map[name]*channelWorker    // 每个 channel 2 个 worker goroutine
    │   ├── runWorker:     OutboundMessage (文本，带 rate limiter + retry)
    │   └── runMediaWorker: OutboundMediaMessage (媒体附件)
    │
    ├── dispatchOutbound: 从 bus.OutboundChan() 读取，路由到对应 worker
    └── dynamicServeMux:  共享 HTTP 服务器，运行时动态注册 webhook 路由
```

**消息预处理（preSend）**：停止 typing → 撤销 reaction → 编辑/删除 placeholder → 发送实际内容

### 5.4 热重载

```go
func (m *Manager) Reload(cfg *config.Config) {
    // 1. 计算新旧配置 MD5 哈希
    // 2. 比较得出 added / removed channels
    // 3. 停止 removed，启动 added
    // 4. 更新 dynamicServeMux 路由
}
```

---

## 6. Routing 层 — 消息路由与模型选择

`pkg/routing/` 负责两件事：消息应该交给哪个 Agent，以及用哪个模型处理。

### 6.1 消息路由（RouteResolver）

```go
type ResolvedRoute struct {
    AgentID       string        // 目标 Agent
    SessionPolicy SessionPolicy // 会话维度策略
    MatchedBy     string        // 匹配来源（如 "dispatch.rule:myrule"）
}

type SessionPolicy struct {
    Dimensions    []string              // space, chat, topic, sender
    IdentityLinks map[string][]string   // 跨平台身份关联
}
```

**路由决策**：遍历 `config.Agents.Dispatch.Rules`，检查 `When` 选择器是否匹配 dispatchView（Channel/Account/Space/Chat/Topic/Sender/Mentioned）。

**Identity Links**：允许将多个平台 ID 映射到同一规范身份：
```json
{
  "alice": ["telegram:12345", "discord:67890"]
}
```

### 6.2 智能模型路由（Router + Classifier）

根据消息复杂度自动选择主模型（heavy）或轻量模型（light）：

```go
type Router struct {
    cfg        RouterConfig   // LightModel + Threshold (默认 0.35)
    classifier Classifier     // RuleClassifier
}
```

**评分规则**：

| 信号 | 权重 | 说明 |
|------|------|------|
| 有附件 | **1.00**（硬门控） | 多模态必须走主模型 |
| 消息长度 >200 tokens | +0.35 | 长消息 |
| 含代码块 | +0.40 | 代码任务 |
| 近期工具调用 >3 | +0.25 | 密集工具链 |

**约束**：一个 turn 内所有 LLM 调用使用**相同模型层级**，防止多步工具链中途切换。

---

## 7. Tools 层 — 工具系统

`pkg/tools/` 提供 Agent 与外部世界交互的能力。

### 7.1 Tool 接口

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() map[string]any
    Execute(ctx context.Context, args map[string]any) *ToolResult
}
```

### 7.2 ToolRegistry

```go
type ToolRegistry struct {
    tools   map[string]*ToolEntry   // name → {Tool, IsCore, TTL}
    version atomic.Uint64           // 原子版本号，缓存失效
}
```

**关键能力**：
- **核心工具**：始终可见（`IsCore=true`）
- **动态可见性**：非核心工具通过 `PromoteTools(names, ttl)` 临时提升，TTL 递减实现自动隐藏
- **Clone**：子代理获得父工具的快照，防止递归 spawn

### 7.3 ExecTool（最复杂工具）

1142 行的 `ExecTool` 是工具系统的复杂度顶点：

```
ExecTool
├── 同步执行（runSync）    — 阻塞等待，超时 kill
├── 后台执行（runBackground）— 返回 sessionId，支持 poll/read/write/kill
│   ├── 普通模式：stdout/stderr pipe + stdin pipe
│   └── PTY 模式：伪终端，支持交互式程序（vim, htop 等）
├── 安全守卫（guardCommand）— 4 层防护：
│   ├── 远程通道限制（防止 webhook 触发危险命令）
│   ├── Deny 模式（40+ 正则拦截 rm -rf, curl \| sh 等）
│   ├── 工作空间限制（禁止 ../ 路径遍历）
│   └── TOCTOU 防护（执行前重新解析符号链接）
└── Send-Keys — 丰富的按键映射（方向键、功能键、Ctrl/Alt/Shift 组合）
```

### 7.4 spawn / subagent 工具

| 工具 | 模式 | 说明 |
|------|------|------|
| `spawn` | 异步 | 启动独立 SubTurn，结果通过 channel 异步投递 |
| `subagent` | 同步 | 启动 SubTurn，阻塞等待结果返回 |
| `spawn_status` | 查询 | 查询异步 spawn 的执行状态 |

---

## 8. Gateway — 服务入口与生命周期

`pkg/gateway/` 是系统的单一入口点。

### 8.1 生命周期编排

```
Run()
├── 1. 日志初始化（panic 捕获 + 文件日志）
├── 2. 配置加载与校验
├── 3. 创建网关监听器（支持多网卡绑定）
├── 4. 创建 LLM Provider
├── 5. 创建 AgentLoop
├── 6. setupAndStartServices() — 核心服务编排
│   ├── CronService
│   ├── HeartbeatService
│   ├── MediaStore（文件存储 + TTL 清理）
│   ├── ChannelManager
│   ├── HealthServer (/health, /ready, /reload)
│   └── VoiceAgent（可选）
├── 7. 启动 AgentLoop
├── 8. 信号监听 + 热重载循环
│   ├── SIGINT/SIGTERM → 优雅关闭
│   ├── 配置文件变更 → 自动重载
│   └── POST /reload → 手动重载
└── 9. shutdownGateway（优雅关闭）
```

### 8.2 共享 HTTP 服务器

Gateway 提供一个端口同时服务：
- **Webhook 端点**：`/webhook/telegram`, `/webhook/line`, ...
- **健康检查**：`/health`, `/ready`
- **热重载**：`/reload`

`dynamicServeMux` 支持运行时动态注册/注销 handler，无需重启服务器。

---

## 9. 支撑模块

### 9.1 Session (`pkg/session/`) — 作用域分配

```
AllocateRouteSession(AgentID, InboundContext, SessionPolicy)
    ├── buildSessionScope()     — 基础维度 + 动态维度
    ├── BuildOpaqueSessionKey() — SHA256 生成 sk_v1_xxx
    └── buildLegacySessionAliases() — 兼容旧版 agent:id:... 格式
```

**Opaque Key**：`sk_v1_` + SHA256(scope)，隐藏内部结构，防止客户端伪造。

**Alias 兼容**：新旧 key 格式并存，系统自动提升 alias 历史到 canonical session。

### 9.2 Memory (`pkg/memory/`) — JSONL 存储

每个 session 对应两个文件：
- `{key}.jsonl`：追加式消息存储
- `{key}.meta.json`：元数据（summary、skip 偏移、aliases）

**核心设计**：
- **逻辑删除（Truncate）**：更新 `skip` 偏移，跳过旧消息，避免 Unmarshal 开销
- **物理压缩（Compact）**：重写有效消息到新文件，先写 meta 再写 jsonl，崩溃安全
- **并发控制**：64 个固定 shard 的 `sync.Mutex` 数组（FNV hash 分片）
- **持久化保证**：每次追加后 `fsync()`，meta 原子写入（temp + fsync + rename）

### 9.3 Config (`pkg/config/`) — 配置管理

- **Model-centric**：`model_list` 替代旧版 `providers`，每个模型独立配置
- **多 Key 扩展**：单个模型配多个 API key 时，自动展开为虚拟模型（`model__key_1`），实现 key 级故障转移
- **安全隔离**：敏感字段存储于 `.security.yml`，支持 `enc://` 加密、`file://` 文件引用
- **版本迁移**：V0→V1→V2→V3 迁移链，自动备份原配置

### 9.4 Skills (`pkg/skills/`) — 技能系统

三级加载优先级：
1. **Workspace**：`{workspace}/skills/`
2. **Global**：`~/.picoclaw/skills/`
3. **Builtin**：内置技能

每个 skill 是一个目录，必须包含 `SKILL.md`（YAML frontmatter + Markdown 正文）。

Registry 支持并发搜索（ClawHub 官方市场 + GitHub Code Search）。

### 9.5 MCP (`pkg/mcp/`) — Model Context Protocol

- **stdio transport**：启动子进程，通过 stdin/stdout JSON-RPC，支持进程隔离
- **sse/http transport**：Streamable HTTP，SSE 双向流或纯 HTTP 模式
- **Deferred 模式**：MCP 工具初始为 hidden，需通过搜索工具动态发现后提升

### 9.6 Cron (`pkg/cron/`) — 定时任务

支持三种调度：
- `at`：一次性（绝对时间戳）
- `every`：间隔（毫秒）
- `cron`：标准 cron 表达式

调度循环使用 `timer.Reset(delay)` 精确睡眠到最近触发点，`wakeChan` 打断时立即重新计算。

---

## 10. 关键设计模式总结

### 10.1 接口隔离与可插拔

```go
ContextManager  ——  Legacy / Seahorse 两种实现
SessionStore    ——  JSONLBackend / MemoryStore
MessageBus      ——  内存 channel 实现（可替换）
LLMProvider     ——  30+ 具体 Provider
Channel         ——  20+ 具体平台
```

### 10.2 分层工厂模式

```
协议名 ──▶ factory_provider.go ──▶ 具体 Provider 包
         │                         ├── openai_compat (HTTP, 最通用)
         │                         ├── anthropic (官方 SDK)
         │                         ├── cli (子进程)
         │                         └── ...
         └── protocolMetaByName 注册 30+ 协议的默认 endpoint
```

### 10.3 弹性模式组合

```
ErrorClassifier 分类错误类型
      │
      ▼
CooldownTracker  指数退避冷却
      │
      ▼
RateLimiter      Token-bucket 限流
      │
      ▼
FallbackChain    按优先级尝试 candidates
```

### 10.4 并发控制层次

| 层次 | 机制 | 作用 |
|------|------|------|
| Session 级 | `activeTurnStates.LoadOrStore` | 同 session 串行 |
| Worker 级 | `workerSem` 信号量 | 限制总并发 turn 数 |
| SubTurn 级 | `concurrencySem` | 每个 parent 限制子 turn 并发 |
| Channel 级 | `golang.org/x/time/rate` | 每个 channel 出站限流 |
| MCP 级 | `sync.WaitGroup` | 等待 inflight 调用完成 |

### 10.5 崩溃安全设计

| 组件 | 策略 |
|------|------|
| JSONL 存储 | 追加写 + fsync，不修改已有数据 |
| Meta 文件 | `fileutil.WriteFileAtomic`（temp + fsync + rename） |
| 压缩 | 先写 meta（skip=0），再写 jsonl |
| 配置迁移 | 自动备份原配置（`.20060102.bak`） |
| 热重载 | 原子替换，旧资源延迟关闭 |

---

## 附录：核心文件索引

| 组件 | 关键文件 | 行数（约） |
|------|----------|-----------|
| Agent Loop | `pkg/agent/agent.go` | ~400 |
| Turn 协调器 | `pkg/agent/turn_coord.go` | ~300 |
| Pipeline | `pkg/agent/pipeline_*.go` | ~1200 |
| Hooks | `pkg/agent/hooks.go` | ~400 |
| SubTurn | `pkg/agent/subturn.go` | ~300 |
| Steering | `pkg/agent/steering.go` | ~150 |
| ContextBuilder | `pkg/agent/context.go` | ~400 |
| Provider 工厂 | `pkg/providers/factory_provider.go` | ~600 |
| openai_compat | `pkg/providers/openai_compat/provider.go` | ~500 |
| Fallback | `pkg/providers/fallback.go` | ~200 |
| Cooldown | `pkg/providers/cooldown.go` | ~200 |
| Channel 基类 | `pkg/channels/base.go` | ~400 |
| Channel Manager | `pkg/channels/manager.go` | ~600 |
| RouteResolver | `pkg/routing/route.go` | ~300 |
| Router | `pkg/routing/router.go` | ~150 |
| ToolRegistry | `pkg/tools/registry.go` | ~400 |
| ExecTool | `pkg/tools/shell.go` | ~1140 |
| Gateway | `pkg/gateway/gateway.go` | ~400 |
| Session | `pkg/session/allocator.go` | ~200 |
| JSONL Store | `pkg/memory/jsonl.go` | ~400 |
