# PicoClaw SSE → LLM → Tools → Output 全链路架构梳理

> 本文档基于 `pkg/channels/openresponses` 与 `pkg/agent` 核心代码，完整拆解从 HTTP/SSE 请求接入到 AI 推理、工具调用、流式输出的全链路细节。

---

## 1. 总览架构图

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        OpenResponses HTTP Handler                           │
│  POST /v1/responses  →  ServeHTTP  →  handleCreateResponse                   │
│                         ├─ dispatch() → MessageBus → AgentLoop               │
│                         ├─ writeSSEResponseStream (Stream=true)              │
│                         └─ writeJSONResponseWithStream (Stream=false)        │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            MessageBus (pkg/bus)                              │
│  InboundChan()  ←  PublishInbound()                                          │
│  OutboundChan() →  PublishOutbound()  →  Channel.Send()                      │
│  GetStreamer()   →  StreamDelegate → channel.BeginStream()                   │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         AgentLoop (pkg/agent/loop.go)                        │
│  Run() → 接收 InboundMessage → resolveSteeringTarget()                        │
│       → activeTurnStates.LoadOrStore (原子抢占会话)                           │
│       → 若占用 → enqueueSteeringMessage (排队)                               │
│       → 若空闲 → workerSem 限流 → goroutine → runTurnWithSteering()          │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Turn 生命周期 (loop_turn.go)                        │
│  runTurn()                                                                   │
│    ├─ ContextManager.Assemble() → history + summary                          │
│    ├─ BuildMessages() → 构造 LLM prompt (含 media 解析)                       │
│    ├─ [Streaming] callLLMStream() → Streamer.Update() 实时推送               │
│    ├─ [Non-streaming] activeProvider.Chat() / FallbackChain.Execute()        │
│    ├─ ToolCalls? → ExecuteWithContext() → 结果回注 messages                   │
│    ├─ 无 ToolCalls / gracefulTerminal → break → finalize                     │
│    └─ 保存会话 → ContextManager.Compact() (可选 summarize)                   │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         OpenResponses 输出层                                 │
│  Send() / SendMedia() → pendingStream.push() → streamEvent                  │
│  writeSSEResponseStream 读取 stream.events → SSE event 序列                  │
│  response.in_progress → output_item.added → content_part.added               │
│  → output_text.delta (流式) → output_text.done → content_part.done           │
│  → output_item.done → response.completed → [DONE]                            │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. HTTP/SSE 接入层 (`pkg/channels/openresponses`)

### 2.1 请求入口

**文件：** `handler.go:23-64` → `handleCreateResponse()`

1. 校验 `Content-Type`、鉴权 (`Bearer Token`)、限流 (`maxBodySize`)。
2. `normalizeInput(req.Input)` 将 `string` 或 `[]InputItem` 提取为纯文本。
3. 生成 `conversationID`（客户端未传则自动生成 `conv_` + UUID）。
4. 调用 `dispatch(ctx, conversationID, content)`。

### 2.2 dispatch 逻辑

**文件：** `openresponses.go:179-238`

```go
func (c *OpenResponsesChannel) dispatch(ctx, conversationID, content)
  → (*pendingStream, bool, error)
```

- **会话互斥**：`convMu` 保护 `convs` map。若该 `conversationID` 已有 `active` 状态：
  - 返回 `(nil, true, nil)`，表示**入队 steering**，HTTP 层返回简短的 queued SSE（`: queue-...` + `[DONE]`）。
- **新建流**：创建 `pendingStream(buf=64)`，标记 `active=true`，存入 `convs`。
- 构造 `bus.InboundContext` 并调用 `c.HandleInboundContext(...)`，将消息注入 `MessageBus`。

### 2.3 pendingStream 结构

**文件：** `types.go:198-244`

```go
type pendingStream struct {
    events chan streamEvent  // 缓冲 64
    done   chan struct{}
    once   sync.Once
    mu     sync.Mutex
    closed bool
}
```

事件类型 (`streamEventKind`)：

| Kind | 含义 |
|------|------|
| `text` | 完整文本（非流式 fallback） |
| `text_delta` | 增量文本 token（流式） |
| `reasoning` | 推理/思考内容 |
| `image` | 图片输出（base64 data URL） |
| `function_call` | 工具调用声明 |
| `turn_end` | 本轮结束标记 |

---

## 3. MessageBus 与 Stream 桥接 (`pkg/bus`)

### 3.1 消息总线

**文件：** `pkg/bus/bus.go`

- `PublishInbound` → `inbound` channel → `AgentLoop.Run()` 消费。
- `PublishOutbound` → `outbound` channel → 各 Channel 的 `Send()` 消费。
- `GetStreamer(ctx, channel, chatID)` 通过 `StreamDelegate` 获取 `Streamer`。

### 3.2 StreamDelegate → BeginStream

在 `OpenResponsesChannel`：

**文件：** `openresponses.go:367-424`

```go
func (c *OpenResponsesChannel) BeginStream(ctx, chatID) (channels.Streamer, error) {
    st.hasStreamer.Store(true)   // 标记启用流式
    return &openResponsesStreamer{stream: st.stream}, nil
}
```

`openResponsesStreamer` 实现：
- `Update(ctx, accumulated)`：计算 `delta = accumulated[len(lastContent):]`，推 `eventKindTextDelta`。
- `Finalize(ctx, content)`：推 `eventKindTurnEnd`。
- `Cancel(ctx)`：关闭 stream。

> **关键细节**：AgentLoop 中 `Send()` 会检查 `hasStreamer.Load()`。若流式激活，则跳过普通文本消息（避免重复发送），仅放行 `thought`、`function_call`、`turn_end`。

---

## 4. AgentLoop 主循环 (`pkg/agent/loop.go`)

### 4.1 运行模型

**文件：** `loop.go:127-254`

```go
func (al *AgentLoop) Run(ctx context.Context) error {
    for {
        select {
        case msg := <-al.bus.InboundChan():
            sessionKey, agentID, ok := al.resolveSteeringTarget(msg)
            // 1. 占位抢占：activeTurnStates.LoadOrStore(placeholder)
            // 2. 若已存在 → enqueueSteeringMessage ( steering queue )
            // 3. 若成功抢占 → go worker() { workerSem 限流 → runTurnWithSteering() }
        }
    }
}
```

**并发控制**：
- `workerSem`（大小 `MaxParallelTurns`，默认 1）限制同时处理的 turn 数。
- 每个 `sessionKey` 同时只能有一个 active turn，通过 `activeTurnStates` 原子 map 保证。

### 4.2 消息路由与处理链

**文件：** `loop_message.go:105-203`

```
processMessage()
  ├─ transcribeAudioInMessage()   // 语音转文字
  ├─ resolveMessageRoute()        // AgentRegistry 路由
  ├─ allocateRouteSession()       // session 分配
  ├─ handleCommand()              // /命令处理（如 /use skill）
  ├─ takePendingSkills()          // 强制技能注入
  └─ runAgentLoop()
```

### 4.3 runAgentLoop → runTurn

**文件：** `loop.go:466-550`

```go
func (al *AgentLoop) runAgentLoop(ctx, agent, opts) (string, error) {
    turnScope := al.newTurnEventScope(...)
    ts := newTurnState(agent, opts, turnScope)
    result, err := al.runTurn(ctx, ts)   // 核心
    // result 后续：followUps 发布 / outbound 响应
}
```

---

## 5. Turn 核心逻辑 (`pkg/agent/loop_turn.go`)

### 5.1 Turn 初始化

**文件：** `loop_turn.go:22-121`

```go
func (al *AgentLoop) runTurn(ctx, ts *turnState) (turnResult, error) {
    // 1. 注册 active turn (registerActiveTurn)
    // 2. 加载历史: ContextManager.Assemble(sessionKey, Budget, MaxTokens)
    // 3. BuildMessages(history, summary, userMessage, media...)
    // 4. resolveMediaRefs() → media:// 转为 base64/image_url
    // 5. 预算超限? → Proactive Compact()
}
```

**ContextManager 接口** (`context_manager.go`)：
- `Assemble`：按 token budget 裁剪历史，返回 `History + Summary`。
- `Compact`：主动压缩（`proactive_budget` / `llm_retry` / `summarize`）。
- `Ingest`：每轮消息后写入 CM 存储。

### 5.2 主循环 turnLoop

**文件：** `loop_turn.go:154-1504`

```
for iteration < MaxIterations || pendingMessages > 0 || gracefulInterrupt {
    // 1. 注入 steering messages (dequeueSteeringMessagesForScope)
    // 2. 检查 hardAbort / parentTurn 结束 / SubTurn 结果
    // 3. 构造 callMessages (含 gracefulTerminal hint)
    // 4. Hooks: BeforeLLM → 可修改/中断
    // 5. 【分支】Streaming vs Non-streaming LLM 调用
    // 6. Hooks: AfterLLM
    // 7. 处理 Reasoning → publishPicoReasoning / publishOpenResponsesReasoning
    // 8. 无 ToolCalls → finalContent = response.Content → break
    // 9. 有 ToolCalls → 逐个执行 → 结果回注 messages → continue turnLoop
}
```

### 5.3 LLM 调用分支详解

#### A. 流式分支 (`callLLMStream`)

**触发条件** (`loop_turn.go:377-383`)：
- `cfg.Agents.Defaults.StreamResponse == true`
- `len(activeCandidates) <= 1`（单候选，不支持 fallback 时流式）
- Provider 实现 `StreamingProvider`
- `bus.GetStreamer()` 成功获取 streamer

**实现** (`loop_turn.go:1592-1655`)：

```go
func (al *AgentLoop) callLLMStream(ctx, sp, streamer, messages, toolDefs, model, opts) {
    resp, err := sp.ChatStream(ctx, messages, toolDefs, model, opts,
        func(accumulated, reasoning string) {
            // content 阶段：Streamer.Update(accumulated) 推 delta
            // reasoning 阶段：在 content 开始前，推 thought 消息
        })
}
```

**流式数据流**：
```
Provider.ChatStream(onChunk)
  → streamer.Update(accumulated)
     → openResponsesStreamer.Update(delta)
        → pendingStream.push({kind: text_delta, content: delta})
           ← handler.writeSSEResponseStream 读取
              → writeSSEEvent("response.output_text.delta", {Delta: delta})
```

#### B. 非流式分支

**单候选**：`activeProvider.Chat(ctx, messages, toolDefs, model, opts)`

**多候选 fallback**：`fallback.Execute()` 逐个尝试候选 provider/model，直到成功。

### 5.4 重试策略

**文件：** `loop_turn.go:416-563`

| 错误类型 | 行为 |
|---------|------|
| `vision unsupported` | 去除所有 media 后重试，并剥离历史中的 media |
| `timeout` | 指数退避 (retry+1)*5s，带 context 检查 |
| `context limit` | 触发 `ContextManager.Compact()`，重建 messages 后重试 |
| `hardAbort` + `context.Canceled` | 直接 abortTurn |

### 5.5 ToolCalls 执行链路

**文件：** `loop_turn.go:714-1504`

#### 5.5.1 前置处理

1. **标准化 ToolCall**：`providers.NormalizeToolCall(tc)`
2. **构造 assistant message**：含 `Content` + `ToolCalls` 数组，追加到 `messages` 与 session。
3. **OpenResponses 透传**：若 `channel == "openresponses"`，将每个 tool call 作为 `function_call` 事件发布到 bus：
   ```go
   bus.PublishOutbound(ctx, OutboundMessage{
       Context: {Raw: {"message_kind": "function_call", "call_id": ..., "name": ..., "arguments": ...}},
   })
   ```

#### 5.5.2 Hook 干预

- `BeforeTool`：可修改参数、直接返回结果（`HookActionRespond`）、拒绝（`DenyTool`）、中断 turn。
- `ApproveTool`：审批钩子，未通过则生成拒绝的 tool result。
- `AfterTool`：可修改执行结果。

#### 5.5.3 工具执行

```go
toolResult := ts.agent.Tools.ExecuteWithContext(execCtx, toolName, toolArgs, channel, chatID, asyncCallback)
```

- `execCtx` 注入 tool 所需的 inbound/session 上下文。
- **Async 工具**：注册 `asyncCallback`，完成后通过 `PublishInbound` 以 `system` channel 注入结果（触发 follow-up turn）。

#### 5.5.4 结果处理

1. **Media 处理**：若 tool 返回 media 且 `ResponseHandled=true`，通过 `channelManager.SendMedia()` 或 `bus.PublishOutboundMedia()` 发送。
2. **ForUser 发送**：若 `!Silent && ForUser != ""`，直接 `PublishOutbound` 给用户。
3. **ForLLM 构建**：`toolResult.ContentForLLM()` → 过滤敏感数据 → 追加 `ArtifactTags`（如 `[file:...]`）。
4. **构造 tool message**：`Role: "tool"`，追加到 `messages` 与 session history。

#### 5.5.5 循环控制

- `allResponsesHandled`：若所有 tool 都自行处理了响应（如 `message` tool 已发送到聊天），且没有新的 steering，则直接结束 turn，不触发 follow-up LLM。
- **跳过剩余 tools**：若中途收到 steering 或 graceful interrupt，后续 tool 标记为 `skipped`，注入占位结果。

### 5.6 Turn 结束

**文件：** `loop_turn.go:1523-1568`

```go
// 1. 处理 steering 残余
if pendingSteering > 0 { goto turnLoop }

// 2. 空内容兜底
if finalContent == "" {
    if iteration >= MaxIterations { finalContent = toolLimitResponse }
    else { finalContent = ts.opts.DefaultResponse }
}

// 3. 保存最终 assistant 消息到 session
finalMsg := Message{Role: "assistant", Content: finalContent}
ts.agent.Sessions.AddMessage(...)
ts.agent.Sessions.Save(...)

// 4. 可选 summarize
if ts.opts.EnableSummary { al.contextManager.Compact(...) }

// 5. 返回 turnResult
return turnResult{finalContent, status, followUps}
```

---

## 6. SSE 输出协议细节

### 6.1 流式模式 (`writeSSEResponseStream`)

**文件：** `handler.go:262-808`

**事件序列（以文本输出为例）**：

```
event: response.in_progress
data: {"type":"response.in_progress","sequence_number":0,"response":{...}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","status":"in_progress","role":"assistant"}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":2,"item_id":"msg_...","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_...","output_index":0,"content_index":0,"delta":"Hello"}

... (多个 delta)

event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":N,"item_id":"msg_...","output_index":0,"content_index":0}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":N+1,...,"part":{"type":"output_text","text":"Hello world"}}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":N+2,...,"item":{"type":"message","status":"completed",...}}

event: response.completed
data: {"type":"response.completed","sequence_number":...,"response":{"status":"completed","output":[...]}}

data: [DONE]
```

**心跳机制**：每 5 秒发送 `: heartbeat-HH:MM:SS\n\n`，并刷新 `WriteDeadline`（30 秒），防止网关超时。

### 6.2 非流式模式 (`writeJSONResponseWithStream`)

- 遍历 `stream.events`，按 kind 分类收集到 `outputItems []ResponseItem`。
- `text_delta` 累积到 `textBuf`，在 `turn_end` 或 `eventKindText` 时 flush 为 `output_text` item。
- 最后输出单个 JSON `Response`。

### 6.3 OpenResponsesChannel.Send 分发

**文件：** `openresponses.go:96-173`

```go
func (c *OpenResponsesChannel) Send(ctx, msg OutboundMessage) ([]string, error) {
    // 1. 查找 conversationState
    // 2. hasStreamer=true 且不是 thought/fc/turn_end → skip (避免重复)
    // 3. 按 message_kind 分类 push streamEvent:
    //    - "turn_end"     → 删除 conv、close stream、push turn_end
    //    - "thought"      → push reasoning
    //    - "function_call"→ push function_call
    //    - 其他           → push text
}
```

---

## 7. Steering 与 Continuation 机制

**文件：** `loop_steering.go:33-107`

```
runTurnWithSteering(initialMsg)
  ├─ processMessage() → runAgentLoop() → runTurn()
  ├─ buildContinuationTarget() → (sessionKey, channel, chatID)
  └─ for pendingSteeringCount > 0 {
         Continue(sessionKey, channel, chatID)  // 复用同一会话继续
     }
  └─ PublishResponseIfNeeded(finalResponse)
  └─ PublishOutbound({message_kind: turn_end})
```

**Steering Queue**：每个 session scope 独立队列。当用户在同一会话中发送多条消息，而前一条正在处理时，后续消息进入 steering queue，在 turn 结束后被 `Continue()` 消费。

---

## 8. 关键数据结构速查

### 8.1 turnState

**文件：** `turn.go:49-110`

| 字段 | 作用 |
|------|------|
| `turnID` / `sessionKey` | 唯一标识 |
| `phase` | setup → running → tools → finalizing → completed/aborted |
| `iteration` | 当前 turn 内 LLM 调用次数 |
| `restorePointHistory` / `restorePointSummary` | abort 时回滚快照 |
| `persistedMessages` | 本轮已持久化的消息 |
| `gracefulInterrupt` / `hardAbort` | 中断控制 |
| `parentTurnState` / `pendingResults` | SubTurn 父子关系与结果通道 |
| `lastFinishReason` / `lastUsage` | Token 预算追踪 |

### 8.2 processOptions (DispatchRequest)

**文件：** `dispatch_request.go`

| 字段 | 含义 |
|------|------|
| `SessionKey` | 会话主键 |
| `UserMessage` | 当前用户输入 |
| `Channel` / `ChatID` | 目标渠道 |
| `SendResponse` | 是否通过 bus 发送响应 |
| `NoHistory` | 是否跳过历史加载（如 heartbeat） |
| `SuppressToolFeedback` | 是否抑制工具执行反馈消息 |
| `InitialSteeringMessages` | 初始注入的 steering |

---

## 9. 附录：文件职责索引

| 文件 | 职责 |
|------|------|
| `pkg/channels/openresponses/handler.go` | HTTP Handler、SSE 协议实现、JSON 响应组装 |
| `pkg/channels/openresponses/openresponses.go` | Channel 生命周期、pendingStream 管理、Send/SendMedia/BeginStream |
| `pkg/channels/openresponses/types.go` | 请求/响应/SSE 类型定义、normalizeInput |
| `pkg/agent/loop.go` | AgentLoop 主循环、Run/Stop、ReloadProviderAndConfig、runAgentLoop |
| `pkg/agent/loop_turn.go` | **核心**：runTurn、callLLMStream、ToolCalls 执行、重试、中断 |
| `pkg/agent/loop_steering.go` | runTurnWithSteering、steering 队列消费、Continuation |
| `pkg/agent/loop_message.go` | processMessage、路由解析、命令处理、系统消息 |
| `pkg/agent/loop_event.go` | 事件发射、EventBus 订阅、Hook 挂载 |
| `pkg/agent/loop_outbound.go` | 响应发布、Pico/OpenResponses reasoning 分发 |
| `pkg/agent/loop_command.go` | handleCommand、applyExplicitSkillCommand |
| `pkg/agent/loop_utils.go` | 工具函数：outbound 上下文构造、media 类型推断、日志格式化 |
| `pkg/agent/turn.go` | turnState 定义、生命周期方法、SubTurn 支持 |
| `pkg/agent/context_manager.go` | ContextManager 接口定义（Assemble/Compact/Ingest/Clear） |
| `pkg/bus/bus.go` | MessageBus：Publish/GetStreamer/Channel 管理 |

---

## 10. 近期变更点（dev-lowe-0418 分支）

根据 git status，以下文件有修改，需特别关注：

- **`pkg/agent/loop_turn.go`**：流式输出机制优化、段落模式输出修正。
- **`web/frontend/vite.config.ts`**：前端构建配置（本文档范围外）。

> 流式输出优化涉及 `callLLMStream` 中 `contentStarted` 标志与 reasoning 推送逻辑，以及 `openResponsesStreamer.Update` 的 delta 计算方式。
