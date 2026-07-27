# PicoClaw Agent 核心模块深度解析

> 目标文件：`pkg/agent`  
> 本文档面向希望深入理解 PicoClaw Agent 编排引擎的开发者。

---

## 1. 一句话定位

`pkg/agent` 是 PicoClaw 的**核心 Agent 编排引擎**。它负责把一条用户消息从接收、路由、拼 prompt、调 LLM、执行工具、压缩上下文、发回响应的完整生命周期串起来，并支持 hooks、子任务（SubTurn）、用户打断（Steering）、多 Agent 路由等高级能力。

---

## 2. 文件地图（按主题分组）

| 主题 | 关键文件 | 作用 |
|---|---|---|
| 入口与生命周期 | `agent.go`, `agent_init.go` | `AgentLoop` 的创建、`Run/Stop/Close`、worker 池 |
| Turn 协调 | `turn_coord.go`, `turn_state.go` | 单次对话 turn 的状态机与主循环 |
| Pipeline 阶段 | `pipeline_setup.go`, `pipeline_llm.go`, `pipeline_execute.go`, `pipeline_finalize.go`, `pipeline.go` | 把一次 turn 拆成 Setup → CallLLM → ExecuteTools → Finalize |
| 上下文/记忆 | `context_manager.go`, `context_seahorse.go`, `context_legacy.go`, `context.go` | 对话历史组装、压缩、summary |
| Prompt 工程 | `context.go`, `prompt.go`, `prompt_contributors.go` | system prompt 拼装、skills、memory、缓存 |
| Hook 扩展 | `hooks.go`, `hook_mount.go`, `hook_process.go` | LLM/Tool 拦截器、审批器、运行时事件观察 |
| 子任务 | `subturn.go` | SubTurn 同步/异步子 Agent 执行 |
| 用户打断 | `steering.go` | 运行中注入新消息、优雅/强制中断 |
| Agent 实例与注册表 | `instance.go`, `registry.go` | 多 Agent、workspace、tools、session 配置 |
| 命令与消息 | `agent_command.go`, `agent_message.go` | `/use`, `/stop`, `/clear`, `/btw` 等 slash 命令 |
| MCP 与进化 | `agent_mcp.go`, `evolution_bridge.go` | Model Context Protocol、自我改进事件桥 |

---

## 3. 核心对象关系

```
MessageBus (pkg/bus)
   │
   ▼
AgentLoop ──┬── AgentRegistry ──┬── AgentInstance (workspace, model, tools, sessions)
            │                    └── routing.RouteResolver
            ├── ContextManager (legacy / seahorse)
            ├── HookManager
            ├── FallbackChain (LLM 失败降级)
            ├── steeringQueue
            ├── MCP runtime
            └── evolutionBridge
```

一次用户请求会生成一个 `turnState` + `Pipeline`：
- `turnState`：本次对话的完整可变状态。
- `Pipeline`：阶段执行的依赖容器。

---

## 4. 消息生命周期

以 `agent.go:145 Run()` 为入口，结合 `turn_coord.go:17 runTurn()` 跟读。

### 4.1 接收与调度

```go
for {
    select {
    case msg := <-al.bus.InboundChan():
        // 1. 解析 sessionKey + agentID
        sessionKey, agentID, ok := al.resolveSteeringTarget(msg)
        // 2. 用 LoadOrStore 原子占坑：同 session 只准一个 turn 在跑
        placeholder := &turnState{...}
        if _, loaded := al.activeTurnStates.LoadOrStore(sessionKey, placeholder); loaded {
            // 正在跑 → 作为 steering 消息入队
            al.enqueueSteeringMessage(...)
            continue
        }
        // 3. 拿到 worker 信号量后启动 goroutine
        go func(...) { al.runTurnWithSteering(ctx, m) }(...)
    }
}
```

**关键设计**：
- `activeTurnStates sync.Map` 保证同 session 串行，避免历史消息竞争。
- `workerSem chan struct{}` 限制并发 turn 数（默认 1，配置 `MaxParallelTurns`）。
- 占位 `turnState`（`pending-{sessionKey}-{seq}`）解决 TOCTOU 竞态。

### 4.2 Turn 主循环

`runTurn` 是项目里圈复杂度最高的函数，但结构可拆成：

```go
runTurn(ctx, ts, pipeline) {
    // 1. SetupTurn：加载历史、拼 prompt、选模型
    exec, err := pipeline.SetupTurn(turnCtx, ts)

    // 2. 迭代循环
    for ... {
        // 2.1 注入 steering / SubTurn 结果
        pendingMessages ← dequeueSteering / pollSubTurnResults
        messages ← append(messages, pendingMessages)

        // 2.2 调 LLM
        ctrl, err := pipeline.CallLLM(...)
        switch ctrl {
            case ControlContinue: continue
            case ControlBreak:    pipeline.Finalize(...); return
            case ControlToolLoop: pipeline.ExecuteTools(...)
        }
    }

    // 3. 兜底 Finalize
    pipeline.Finalize(...)
}
```

### 4.3 Pipeline 四阶段

| 阶段 | 文件 | 职责 |
|---|---|---|
| **SetupTurn** | `pipeline_setup.go:16` | `ContextManager.Assemble` 取历史 → `ContextBuilder.BuildMessagesFromPrompt` 拼消息 → 解析 media → proactive 压缩 → 选 candidate/model |
| **CallLLM** | `pipeline_llm.go:24` | `BeforeLLM` hook → 调 LLM（fallback/retry/streaming）→ `AfterLLM` hook → 返回 ControlBreak/ToolLoop/Continue |
| **ExecuteTools** | `pipeline_execute.go:108` | 遍历 tool calls → `BeforeTool`/审批/执行/`AfterTool` → 处理 steering 跳过 → 返回 ToolControlContinue/Break |
| **Finalize** | `pipeline_finalize.go:16` | 保存 assistant 消息到 session → summary 压缩 → streaming 收尾 → 发布 outbound |

---

## 5. 关键子系统

### 5.1 ContextManager：可插拔的记忆策略

`context_manager.go:15` 接口：

```go
type ContextManager interface {
    Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error)
    Compact(ctx context.Context, req *CompactRequest) error
    Ingest(ctx context.Context, req *IngestRequest) error
    Clear(ctx context.Context, sessionKey string) error
}
```

实现：
- **legacy**：基于 JSONL session store + 代码内 summarization。
- **seahorse**：`context_seahorse.go:20`，用 SQLite 存消息，自带 budget-aware assemble 和 `CompactUntilUnder`。

切换点：`agent.go:336 resolveContextManager()`，默认 `legacy`。

学习重点：
- `SetupTurn` 里的 **proactive 压缩**。
- `CallLLM` 里的 **retry 压缩**（遇到 context_length_exceeded 时）。

### 5.2 Prompt 构建：ContextBuilder

核心方法：
- `BuildSystemPrompt()` / `BuildSystemPromptWithCache()`：缓存 system prompt，按文件 mtime 失效。
- `BuildMessagesFromPrompt(req PromptBuildRequest)`：把 system + history + current 拼成 `[]providers.Message`。

来源包括：
- `AGENT.md / SOUL.md / USER.md / MEMORY.md`
- skills（`skills/` 目录）
- memory（长期记忆 + 每日笔记）
- tools 定义
- 当前 turn 的 user message / media

为了同时适配 Anthropic / OpenAI / Codex，最终输出的是**一条 system message + 多轮 user/assistant/tool 消息**。

### 5.3 Hook 系统

三类接口：

```go
LLMInterceptor     // BeforeLLM / AfterLLM
ToolInterceptor    // BeforeTool / AfterTool
ToolApprover       // ApproveTool
RuntimeEventObserver // 监听运行时事件
```

`HookAction`：`continue / modify / respond / deny_tool / abort_turn / hard_abort`。

**安全设计**：
- `BeforeLLM` 的 modify 不能改 system message 和 tool 定义（`hooks.go:388 applyBeforeLLMControls`）。
- `HookActionRespond` 直接返回 tool 结果，**绕过 `ApproveTool`**，注释中明确标为 SECURITY 风险。
- 三类超时：observer 500ms、interceptor 5s、approval 60s。

### 5.4 SubTurn 子任务

核心设计：
- 子 turn 有**独立 context**（不从 parent 派生），`Critical=true` 时父 turn 结束后仍可继续。
- 深度限制 `defaultMaxSubTurnDepth = 3`，并发限制 `defaultMaxConcurrentSubTurns = 5`。
- 结果交付：
  - `Async=false`：直接返回。
  - `Async=true`：发到 parent 的 `pendingResults` channel，parent 在下一轮 LLM 前消费。
- Hard abort 会级联取消所有子 turn（`turn_state.go:798 Finish(true)`）。

入口：`SpawnSubTurn(ctx, cfg)`，从 context 取 `AgentLoop` 和 `turnState`。

### 5.5 Steering 用户打断

流程：
1. 同 session 有新消息时，进入 `steeringQueue`。
2. `ExecuteTools` 执行完单个 tool 后 dequeue steering。
3. 如有未执行的 tool calls，全部 skip 并生成 skip tool result。
4. `ControlContinue` 回到 turn 顶部，注入 steering 后继续调 LLM。

模式：
- `SteeringOneAtATime`（默认）：每次取一条。
- `SteeringAll`：一次取完。

`InterruptGraceful(hint)` 优雅中断，`HardAbort(sessionKey)` 立即取消。

---

## 6. 并发与状态模型

```
┌─────────────────────────────┐
│  AgentLoop.Run (单 goroutine) │  只负责收消息 + 占 session + 丢给 worker
└─────────────┬───────────────┘
              ▼
        worker goroutine (受 workerSem 限制)
              │
              ▼
        runTurn(ctx, ts, pipeline)
              │
              ▼
        Pipeline 四阶段（同 goroutine 顺序执行）
```

- 同 session 的 turn **严格串行**。
- 不同 session 之间并发度受 `MaxParallelTurns` 限制。
- `turnState.mu sync.RWMutex` 保护 phase/iteration/finalContent 等字段。
- `providerCancel` / `turnCancel` 用于 hard abort 级联取消。

---

## 7. 推荐学习路径

1. **入口与调度**：`agent.go:145 Run()` → `agent.go:528 runAgentLoop()` → `turn_coord.go:17 runTurn()`。
2. **单次 Turn 骨架**：`pipeline_setup.go` → `pipeline_llm.go` → `pipeline_execute.go` → `pipeline_finalize.go`。
3. **状态容器**：`turn_state.go`（重点看 `turnState` 字段、`requestHardAbort`、`Finish`）。
4. **记忆策略**：`context_manager.go` → `context_seahorse.go` → `context_legacy.go`。
5. **扩展机制**：`hooks.go` → `subturn.go` → `steering.go`。
6. **配置与实例化**：`agent_init.go` → `instance.go` → `registry.go`。

---

## 8. 值得注意的设计点

- `runTurn` 是项目里圈复杂度最高的函数，但它本质是一个**状态机协调器**。
- **ContextManager 是可插拔策略**；seahorse 版本把历史管理从 JSONL 抽到了 SQLite + LLM summarization。
- **SubTurn 的独立 context 设计**是为了让后台子 Agent 不被父 turn 的生命周期误杀。
- **Steering 不是并发执行，而是插队**：保证 session 历史顺序正确。
- **Hook 的 `respond` action 是特权操作**，可以跳过审批，使用时要格外谨慎。
