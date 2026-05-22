# OpenResponses Channel — 精确开发规范

> 本文档用于在 picoclaw 代码更新后重新开发 OpenResponses 通道。所有字段、行为、映射规则必须严格遵循。

---

## 1. 对外 HTTP API 规范

### 1.1 端点

| Method | Path | 功能 |
|--------|------|------|
| `POST` | `/v1/responses` | 创建响应（核心） |
| `GET`  | `/v1/responses/sessions` | 列会话 |
| `GET`  | `/v1/responses/sessions/{id}` | 获取会话详情 |
| `DELETE` | `/v1/responses/sessions/{id}` | 删除会话 |

- `WebhookPath()` 返回值：如果 `EndpointPath` 不以 `/` 结尾，则补 `/`，即 `/v1/responses/`。
- `ServeHTTP` 路由规则：
  - `path == base || path == base+"/"` → `serveCreateResponse`
  - `path == sessionsBase || path == sessionsBase+"/"` → `handleListSessions`
  - `strings.HasPrefix(path, sessionsBase+"/")` → `handleSessionDetail`（内部再分 GET/DELETE）
  - 其他 → 404

### 1.2 鉴权

- Header: `Authorization: Bearer {token}`
- Token 来源：`config.Token.String()`（`SecureString` 类型）
- Token 为空字符串时，鉴权直接失败

### 1.3 错误响应格式

```json
{
  "error": {
    "type": "server_error" | "rate_limit_exceeded" | "not_found" | "invalid_request",
    "code": "xxx",
    "message": "human readable"
  }
}
```

| HTTP Status | type | 触发场景 |
|-------------|------|---------|
| 503 | `server_error` | Channel 未运行 (`!c.IsRunning()`) |
| 401 | `invalid_request` | Token 缺失或错误 |
| 404 | `not_found` | 端点不存在 |
| 405 | `invalid_request` | Method 不允许 |
| 400 | `invalid_request` | Content-Type 不是 `application/json` |
| 413 | `invalid_request` | Body 超过 `maxBodySize` |
| 400 | `invalid_request` | JSON 解析失败 |
| 400 | `invalid_request` | Input 为空（仅空白字符且无图片） |
| 429 | `rate_limit_exceeded` | `dispatch()` 返回错误（理论上目前不会触发） |

### 1.4 POST /v1/responses 请求体

```go
type CreateResponseRequest struct {
    Model              string        `json:"model,omitempty"`              // 可选
    Input              any           `json:"input"`                        // 必填：string 或 []ContentPart
    Content            []ContentPart `json:"content,omitempty"`            // 未使用
    Instructions       string        `json:"instructions,omitempty"`       // 可选
    PreviousResponseID string        `json:"previous_response_id,omitempty"` // 透传至 Response
    ConversationID     string        `json:"conversation_id,omitempty"`    // 可选，未传则自动生成 `conv_` + UUID
    Stream             bool          `json:"stream,omitempty"`             // 默认 false
    Tools              []Tool        `json:"tools,omitempty"`              // 可选
    ToolChoice         any           `json:"tool_choice,omitempty"`        // 可选
    Temperature        *float64      `json:"temperature,omitempty"`        // 可选
    MaxOutputTokens    int           `json:"max_output_tokens,omitempty"`  // 可选
    Truncation         string        `json:"truncation,omitempty"`         // 可选
}
```

```go
type ContentPart struct {
    Type    string `json:"type"`    // "input_text" | "input_image" | "input_file"
    Content string `json:"content"` // 统一值字段，图片/文件为 base64 data URL
}
```

**Input 解析规则** (`extractRequestContent`)：
- `nil` → `("", nil)`
- `string` → `(strings.TrimSpace(s), nil)`
- `[]ContentPart` 或 `[]any`（内部元素为 `map[string]any` 或 `ContentPart`）→ 提取 `type` 和 `content` 字段
  - `input_text`：非空则加入文本，多个用 `"\n"` 拼接
  - `input_image` / `input_file`：非空则加入 media 数组

**文件处理规则** (`handleCreateResponse`)：
- 遍历 media 数组，区分图片和非图片：
  - `isImageDataURL(m)`：前缀 `data:image/` → 加入 `imageMedia`
  - 其他 → `saveDataURLToTemp(m)` 保存到 `media.TempDir()`，路径加入 `filePaths`
- 如果有 `filePaths`，在 content 后追加中文提示：
  ```
  "\n\n用户上传了以下文件，如需读取请使用 read_file 工具：\n- {path1}\n- {path2}\n"
  ```
- 最终校验：`strings.TrimSpace(content) == "" && len(imageMedia) == 0` → 400 错误

**ConversationID 生成规则**：
- 客户端传入则 `strings.TrimSpace(req.ConversationID)`
- 未传入则 `"conv_" + uuid.New().String()`

---

### 1.5 非流式响应 (Stream=false)

**响应格式**：单个 JSON `Response` 对象

```go
type Response struct {
    ID                 string         `json:"id"`                  // "resp_" + conversationID
    Object             string         `json:"object"`              // 固定 "response"
    CreatedAt          int64          `json:"created_at"`          // time.Now().Unix()
    Status             string         `json:"status"`              // 固定 "completed"
    Model              string         `json:"model,omitempty"`
    Output             []ResponseItem `json:"output"`
    ConversationID     string         `json:"conversation_id,omitempty"`
    PreviousResponseID string         `json:"previous_response_id,omitempty"`
    Usage              Usage          `json:"usage"`               // {0, 0}
}
```

**内部事件收集规则** (`writeJSONResponseWithStream`)：
- `textBuf` 累积 `eventKindTextDelta`，在 `eventKindTurnEnd` 时 flush
- `eventKindText`：直接创建 message item（`output_text`）
- `eventKindReasoning`：flush textBuf 后创建 reasoning item（`reasoning_text`）
- `eventKindImage`：flush textBuf 后创建 message item（`output_image`）
- `eventKindFunctionCall`：flush textBuf 后创建 function_call item
- Item ID 格式：`"msg_" + conversationID + "_" + msgSeq`（msgSeq 从 0 递增）

**空输出兜底**：如果 `outputItems` 为空，返回一个空的 message item（`output_text: ""`）

---

### 1.6 SSE 流式响应 (Stream=true)

**HTTP Headers**：
```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
```

**如果 `w` 不支持 `http.Flusher`，降级为 JSON 响应**（调用 `writeJSONResponseWithStream`）

**心跳机制**：
- 每 5 秒发送 `: heartbeat-HH:MM:SS\n\n`
- 每次 flush 时通过 `http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))` 延长写超时

**事件格式**：
```
event: {event_type}\ndata: {json_payload}\n\n
```

---

## 2. SSE 事件规范（14 种事件类型）

### 2.1 事件类型与字段

所有事件共享字段：`type` (string), `sequence_number` (int, 从 0 递增)

| 事件类型 | 附加字段 | 说明 |
|---------|---------|------|
| `response.in_progress` | `response` (Response 对象) | 首个事件，status="in_progress"，output=[]，usage={0,0} |
| `response.output_item.added` | `output_index`, `item` | item.status="in_progress"，content=[] |
| `response.content_part.added` | `item_id`, `output_index`, `content_index`, `part` | part 为初始空值 |
| `response.output_text.delta` | `item_id`, `output_index`, `content_index`, `delta` | 文本增量片段 |
| `response.output_text.done` | `item_id`, `output_index`, `content_index` | 无 delta |
| `response.reasoning_text.delta` | `item_id`, `output_index`, `content_index`, `delta` | 推理增量片段 |
| `response.reasoning_text.done` | `item_id`, `output_index`, `content_index` | 无 delta |
| `response.function_call_arguments.delta` | `item_id`, `output_index`, `content_index`, `delta` | 参数增量片段 |
| `response.function_call_arguments.done` | `item_id`, `output_index`, `content_index` | 无 delta |
| `response.content_part.done` | `item_id`, `output_index`, `content_index`, `part` | part 包含最终内容 |
| `response.output_item.done` | `output_index`, `item` | item.status="completed"，包含完整 content |
| `response.completed` | `response` (Response 对象) | status="completed"，output 包含所有 items（但 output_image 的 content 被 strip 为空） |
| `response.failed` | `response` (包含 error 对象) | 未在代码中实际使用 |
| `[DONE]` | 无 | 终止标记，格式为 `data: [DONE]\n\n` |

### 2.2 事件序列规则

**每个 output_item 的完整生命周期**：

```
response.output_item.added
  response.content_part.added
    [xxx].delta (0..N 次)
    [xxx].done
  response.content_part.done
response.output_item.done
```

**内部事件 → SSE 的精确映射**：

| 内部事件 | 触发条件 | SSE 序列 |
|---------|---------|---------|
| `text_delta` (首个) | `!hasActiveTextItem` | output_item.added(message) → content_part.added(output_text,"") → output_text.delta |
| `text_delta` (后续) | `hasActiveTextItem` | output_text.delta |
| `text` | 总是非流式 | 完整序列：added → part.added → delta → done → part.done → item.done |
| `reasoning` (首个) | `!hasActiveReasoningItem` | 先 closeActiveTextItem()，然后 output_item.added(reasoning) → content_part.added(reasoning_text,"") → reasoning_text.delta |
| `reasoning` (后续) | `hasActiveReasoningItem` | reasoning_text.delta |
| `image` | 总是 | 先 closeActiveTextItem() + closeActiveReasoningItem()，然后完整序列（无 delta/done，直接 part.done） |
| `function_call` | 总是 | 先 closeActiveTextItem() + closeActiveReasoningItem()，然后完整序列：added → part.added(arguments,"") → delta → done → part.done → item.done |
| `turn_end` | — | closeActiveTextItem() + closeActiveReasoningItem()，不发送任何 SSE 事件 |

**关键互斥规则**：
- 收到 `text_delta` 前，如果 `hasActiveReasoningItem`，先 `closeActiveReasoningItem()`
- 收到 `reasoning` 前，如果 `hasActiveTextItem`，先 `closeActiveTextItem()`
- 收到 `image` / `function_call` / `text` 前，同时关闭 text 和 reasoning

**response.completed 的 output strip 规则**：
```go
func stripContentsFromItem(item ResponseItem) ResponseItem {
    for _, c := range item.Content {
        if c.Type == "output_image" {
            c.Content = ""  // 清空 base64 data URL，节省带宽
        }
    }
}
```

---

## 3. 内部事件系统规范

### 3.1 `pendingStream` 队列

```go
type pendingStream struct {
    events chan streamEvent  // 缓冲大小固定为 64
    done   chan struct{}
    once   sync.Once
    mu     sync.Mutex
    closed bool
}
```

**行为**：
- `push()`: 如果 `closed=true`，返回 false；如果 channel 满，返回 false（打印 WARN 日志，事件被丢弃）
- `close()`: `sync.Once` 保证只执行一次，设置 `closed=true`，`close(events)`，`close(done)`

### 3.2 `streamEvent` 结构

```go
type streamEvent struct {
    kind      streamEventKind  // 6 种
    content   string           // text_delta, text, reasoning 的内容
    imageURL  string           // image: base64 data URL
    caption   string           // image: 未使用（保留字段）
    callID    string           // function_call: 调用 ID
    name      string           // function_call: 函数名
    arguments string           // function_call: JSON 参数字符串
}
```

### 3.3 `conversationState` 结构

```go
type conversationState struct {
    stream      *pendingStream
    done        chan struct{}
    active      atomic.Bool   // dispatch 时设为 true
    hasStreamer atomic.Bool   // BeginStream 成功时设为 true
}
```

---

## 4. Channel 接口实现规范

### 4.1 必须实现的接口

| 接口 | 所在包 | 说明 |
|------|--------|------|
| `Channel` (`Start`, `Stop`, `Send`) | `pkg/channels` | 基础通道接口 |
| `WebhookHandler` (`WebhookPath`, `ServeHTTP`) | `pkg/channels` | HTTP 端点挂载 |
| `StreamingCapable` (`BeginStream`) | `pkg/channels` | 流式支持 |
| `MediaSender` (`SendMedia`) | `pkg/channels` | 媒体发送 |

### 4.2 `Start(ctx)`

1. `logger.InfoC("openresponses", "Starting OpenResponses channel")`
2. `c.ctx, c.cancel = context.WithCancel(ctx)`
3. `c.SetRunning(true)`
4. `logger.InfoC("openresponses", "OpenResponses channel started")`

### 4.3 `Stop(ctx)`

1. `logger.InfoC("openresponses", "Stopping OpenResponses channel")`
2. `c.SetRunning(false)`
3. 遍历 `convs`，对每个 `st` 调用 `st.stream.close()`
4. `clear(c.convs)`
5. `c.cancel()`
6. `logger.InfoC("openresponses", "OpenResponses channel stopped")`

### 4.4 `Send(ctx, msg)` — 核心分发逻辑

**前置检查**：
- `!c.IsRunning()` → 返回 `channels.ErrNotRunning`
- `msg.ChatID == ""` → 返回 `(nil, nil)`（静默丢弃）
- 在 `convs` 中找不到对应 `conversationID` → WARN 日志，返回 `(nil, nil)`

**Streamer 激活时的过滤逻辑** (`hasStreamer.Load() == true`)：

以下 `message_kind` **允许通过**：
- `"function_call"`
- `"turn_end"`
- `"tool_timing"`
- `"llm_timing"`
- `"error"`

其他所有 message_kind **被跳过**（避免与流式文本重复）

**message_kind → streamEvent 映射**：

| message_kind | streamEvent | 附加操作 |
|-------------|-------------|---------|
| `"turn_end"` | `eventKindTurnEnd` | 1. push turn_end<br>2. `delete(c.convs, conversationID)`<br>3. `close(st.done)`<br>4. `st.stream.close()`<br>5. 返回 |
| `"thought"` | `eventKindReasoning` | content = msg.Content |
| `"function_call"` | `eventKindFunctionCall` | callID=raw["call_id"], name=raw["name"], arguments=raw["arguments"] |
| 其他 | `eventKindText` | content = msg.Content |

### 4.5 `SendMedia(ctx, msg)`

**前置检查**：
- `!c.IsRunning()` → `channels.ErrNotRunning`
- `msg.ChatID == ""` → `(nil, nil)`
- 找不到 conversation → `(nil, nil)`
- `c.GetMediaStore() == nil` → WARN 日志，`(nil, nil)`

**处理规则**：遍历 `msg.Parts`
- 跳过 `part.Type != "image"`
- 跳过 `!strings.HasPrefix(part.Ref, "media://")`
- `store.ResolveWithMeta(part.Ref)` 获取本地路径和 meta
- `meta.ContentType` 为空则用 `mimeFromExt(part.Filename)` 推断
- 跳过 `!strings.HasPrefix(mime, "image/")`
- `encodeFileToDataURL(localPath, mime)` → base64 data URL
- push `streamEvent{kind: eventKindImage, imageURL: dataURL, caption: part.Caption}`
- 成功则 `sentIDs = append(sentIDs, part.Ref)`

### 4.6 `BeginStream(ctx, chatID)`

1. 在 `convs` 中查找 `chatID`
2. 找到则 `st.hasStreamer.Store(true)`
3. 未找到则返回错误：`fmt.Errorf("no active conversation for %s", chatID)`
4. 返回 `&openResponsesStreamer{channel: c, convID: chatID, stream: st.stream}`

### 4.7 `openResponsesStreamer` 行为

```go
type openResponsesStreamer struct {
    channel       *OpenResponsesChannel
    convID        string
    stream        *pendingStream
    lastContent   string   // 上次推送的累积文本长度
    lastReasoning string   // 上次推送的累积推理长度
}
```

| 方法 | 行为 |
|------|------|
| `Update(ctx, accumulated)` | 如果 `len(accumulated) <= len(lastContent)` 直接返回；`delta = accumulated[len(lastContent):]`；`lastContent = accumulated`；push `eventKindTextDelta{content: delta}` |
| `UpdateReasoning(ctx, accumulatedReasoning)` | 同上，计算 delta，push `eventKindReasoning{content: delta}` |
| `UpdateToolCall(ctx, callID, name, arguments)` | 直接 push `eventKindFunctionCall{callID, name, arguments}`（不累积） |
| `Finalize(ctx, content)` | push `eventKindTurnEnd` |
| `Cancel(ctx)` | `stream.close()` |

---

## 5. `dispatch()` 会话管理规范

```go
func (c *OpenResponsesChannel) dispatch(ctx, conversationID, content string, media []string) (*pendingStream, bool, error)
```

**返回值**：`(stream, queued, err)`

**并发互斥**：使用 `convMu` 保护 `convs` map

**Case 1: 该 conversationID 已有 active 请求**
- 构造 `bus.SenderInfo{Platform:"openresponses", PlatformID:"user", CanonicalID: identity.BuildCanonicalID("openresponses", "user")}`
- 构造 `bus.InboundContext{Channel:c.Name(), ChatID:conversationID, ChatType:"direct", SenderID:sender.CanonicalID, MessageID:conversationID, Raw:{"conversation_id": conversationID}}`
- 调用 `c.HandleInboundContext(ctx, conversationID, content, media, inboundCtx, sender)` → 消息进入 steering 队列
- 返回 `(nil, true, nil)`

**Case 2: 新建请求**
- 创建 `pendingStream(bufSize=64)`
- 创建 `conversationState{stream: s, done: make(chan struct{})}`，设置 `active=true`
- 存入 `convs`
- 构造与 Case 1 相同的 `SenderInfo` 和 `InboundContext`
- 调用 `c.HandleInboundContext(...)`
- 返回 `(s, false, nil)`

**queued 处理**：HTTP handler 收到 `queued=true` 时：
- 发送 SSE header
- 写入 `: queue-{YYYY-MM-DD}\n\n`
- 写入 `data: [DONE]\n\n`
- Flush 并返回

---

## 6. Agent 层与 OpenResponses 的交互规范

### 6.1 Agent 发布的 message_kind（OutboundMessage.Context.Raw["message_kind"]）

Agent 层通过 `bus.PublishOutbound` 向 OpenResponses 通道发送消息。OpenResponses 的 `Send()` 方法根据 `message_kind` 进行分发。

**Agent 发布的 message_kind 列表**：

| message_kind | 发布位置 | 说明 |
|-------------|---------|------|
| `"thought"` | `publishOpenResponsesReasoning()` | LLM reasoning 内容 |
| `"function_call"` | `pipeline_llm.go` tool-call path | 工具调用声明 |
| `"turn_end"` | steering/continuation 结束 | 标记一轮结束 |
| `"llm_timing"` | `pipeline_llm.go` AfterLLM | LLM 推理耗时（如 `🤖 LLM 推理 in 1.234s`） |
| `"tool_timing"` | `pipeline_execute.go` | 工具执行耗时 |
| `"tool_feedback"` | `pipeline_execute.go` | 工具执行反馈 |
| `"error"` | `maybePublishError()` | 错误消息 |

### 6.2 Agent 层对 openresponses 的特殊处理

**`pipeline_llm.go` 中的特殊分支**：

1. **Reasoning 分发**（约 line 433-444）：
   ```go
   if ts.channel == "pico" {
       go al.publishPicoReasoning(turnCtx, reasoningContent, ts.chatID)
   } else if ts.channel == "openresponses" {
       go al.publishOpenResponsesReasoning(turnCtx, reasoningContent, ts.chatID)
   } else {
       go al.handleReasoning(turnCtx, reasoningContent, ts.channel, al.targetReasoningChannelID(ts.channel))
   }
   ```
   - `publishOpenResponsesReasoning`：发布 `message_kind="thought"`，channel="openresponses"

2. **Function Call 透传**（约 line 599-630）：
   - **流式模式**：若 `streamer != nil`，调用 `streamer.UpdateToolCall(ctx, tc.ID, tc.Name, argsJSON)`
   - **非流式模式**：若 `channel == "openresponses"`，通过 `bus.PublishOutbound` 发送 `message_kind="function_call"`，附带 `call_id`、`name`、`arguments`

3. **LLM Timing 发送**（约 line 391-419）：
   - 若 `IsToolFeedbackEnabled() && channel != "" && !SuppressToolFeedback`
   - 发送 `message_kind="llm_timing"`

### 6.3 `PublishResponseIfNeeded`

- 发送纯文本响应（无 message_kind，在 `Send()` 中走 default 分支 → `eventKindText`）
- 如果 `message` 工具已经向同一会话发送过内容，则跳过（避免重复）

---

## 7. Session API 规范

### 7.1 GET /v1/responses/sessions

**Query 参数**：`offset` (默认 0), `limit` (默认 20)

**响应格式**：`[]sessionListItem`

```go
type sessionListItem struct {
    ID           string `json:"id"`
    Title        string `json:"title"`         // 最后一条 user 消息前 60 个 rune
    Preview      string `json:"preview"`       // 同 Title
    MessageCount int    `json:"message_count"` // visibleSessionMessages 的数量
    Created      string `json:"created"`       // RFC3339
    Updated      string `json:"updated"`       // RFC3339
}
```

**数据来源**：
- `sessionsDir()` = `filepath.Join(workspace, "sessions")`
- `memory.NewJSONLStore(dir)`
- 遍历所有 session key，通过 `sessionRefFromMeta(meta)` 提取 openresponses session ID
- 去重（按 session ID）
- 过滤空会话（`len(msgs) == 0 && summary == ""`）
- 按 `Updated` 降序排列
- 分页

### 7.2 GET /v1/responses/sessions/{id}

**响应格式**：
```json
{
  "id": "conv_xxx",
  "messages": [
    {"role": "user", "content": "...", "media": ["..."]},
    {"role": "assistant", "content": "..."}
  ],
  "summary": "...",
  "created": "2026-01-01T00:00:00Z",
  "updated": "2026-01-01T00:00:00Z"
}
```

### 7.3 DELETE /v1/responses/sessions/{id}

- 查找对应的 session key
- 删除 `{key}.jsonl` 和 `{key}.meta.json`
- 成功：204 No Content
- 不存在：404

### 7.4 Session ID 提取规则

```go
func extractOpenresponsesSessionIDFromScope(scope session.SessionScope) (string, bool) {
    if scope.Channel != "openresponses" { return "", false }
    
    // 方式 1: scope.Values["chat"] == "direct:{sessionID}"
    chatValue := scope.Values["chat"]
    if strings.HasPrefix(chatValue, "direct:") {
        return strings.TrimPrefix(chatValue, "direct:"), true
    }
    
    // 方式 2: scope.Values["sender"] == {sessionID}
    senderID := scope.Values["sender"]
    if senderID != "" { return senderID, true }
    
    return "", false
}
```

### 7.5 可见消息过滤规则 (`visibleSessionMessages`)

**user 消息**：`Content != "" || len(Media) > 0` 则可见

**assistant 消息**：
1. 跳过 `transientThought`：`Content == "" && ReasoningContent != "" && len(ToolCalls) == 0 && len(Media) == 0`
2. 提取工具调用摘要消息（`visibleAssistantToolSummaryMessages`）：每个 tool call 生成一条 assistant 消息，格式为 `FormatToolFeedbackMessage(name, truncate(args))`
   - `send_file` 工具特殊处理：如果参数中的 path 是图片，内联 base64 data URL
3. 提取 `message` 工具内容（`visibleAssistantToolMessages`）
4. 跳过 `internalOnly`：`Content == "Requested output delivered via tool attachment."`
5. 跳过不可见消息（`Content == "" && len(Media) == 0`）

---

## 8. 配置规范

### 8.1 `OpenResponsesSettings`

```go
type OpenResponsesSettings struct {
    Token          SecureString `json:"token,omitzero"          yaml:"token,omitempty"`
    EndpointPath   string       `json:"endpoint_path,omitempty" yaml:"-"`
    RequestTimeout int          `json:"request_timeout,omitempty" yaml:"-"`
    MaxBodySize    int64        `json:"max_body_size,omitempty" yaml:"-"`
}
```

### 8.2 配置项默认值

| 字段 | 默认值 |
|------|--------|
| `EndpointPath` | `"/v1/responses"` |
| `MaxBodySize` | `1024 * 1024` (1 MB) |
| `RequestTimeout` | 未在代码中使用（保留字段） |

### 8.3 `BaseChannel` 创建参数

```go
channels.NewBaseChannel(
    bc.Name(),      // channel 名称
    cfg,            // OpenResponsesSettings
    messageBus,     // *bus.MessageBus
    bc.AllowFrom,   // 允许的来源
    channels.WithMaxMessageLength(0),  // 0 = 无限制
)
```

---

## 9. 实现检查清单

### 9.1 必须精确还原的行为

- [ ] `pendingStream` 缓冲区大小固定为 **64**
- [ ] `respID` 格式固定为 `"resp_" + conversationID`
- [ ] `msgID` 格式固定为 `"msg_" + conversationID`
- [ ] Item ID 格式固定为 `msgID + "_" + msgSeq`
- [ ] `turn_end` 时 **必须** 执行：`delete(convs)` + `close(done)` + `stream.close()`
- [ ] `hasStreamer=true` 时的过滤白名单：**function_call, turn_end, tool_timing, llm_timing, error**
- [ ] `response.completed` 中的 `output_image` content 必须 strip 为空字符串
- [ ] 心跳间隔 **5 秒**，格式 `: heartbeat-HH:MM:SS\n\n`
- [ ] WriteDeadline 延长 **30 秒**
- [ ] `saveDataURLToTemp` 使用 `media.TempDir()` 作为目录
- [ ] `isImageDataURL` 检查前缀 `data:image/`
- [ ] `extractRequestContent` 中 `input_text` 多个用 `"\n"` 拼接
- [ ] 文件上传注入的中文提示文本必须完全一致
- [ ] Session API 的 `MessageCount` 计算使用 `visibleSessionMessages` 而非原始消息数
- [ ] `sessionListItem` 的 Title/Preview 取自**最后一条 user 消息**，截取 **60 rune**

### 9.2 Agent 层需要保留的特殊分支

- [ ] `ts.channel == "openresponses"` 时调用 `publishOpenResponsesReasoning()`（发布 thought）
- [ ] `ts.channel == "openresponses"` + 非流式时，通过 bus 透传 `function_call` 事件
- [ ] `message` 工具的 `HasSentTo` 检查影响 `PublishResponseIfNeeded`

---

## 10. 文件 MIME 映射表

```go
func extFromMime(mime string) string {
    "application/pdf"     → ".pdf"
    "text/plain"          → ".txt"
    "text/html"           → ".html"
    "text/markdown"       → ".md"
    "application/json"    → ".json"
    "application/xml"/"text/xml" → ".xml"
    "application/javascript"/"text/javascript" → ".js"
    "text/css"            → ".css"
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document" → ".docx"
    "application/msword"  → ".doc"
    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" → ".xlsx"
    "application/vnd.ms-excel" → ".xls"
    "image/jpeg"          → ".jpg"
    "image/png"           → ".png"
    "image/gif"           → ".gif"
    "image/webp"          → ".webp"
    default               → "." + mime 中 "/" 后的部分（去掉 "x-" 前缀）或 ".bin"
}
```

---

## 11. 代码目录结构（新代码中应保持一致）

```
pkg/channels/openresponses/
├── init.go              # RegisterFactory
├── types.go             # 请求/响应/SSE 类型 + pendingStream
├── openresponses.go     # Channel 结构体 + Send/SendMedia/BeginStream/dispatch
├── handler.go           # HTTP Handler + SSE/JSON 响应组装
├── session_handler.go   # Session REST API
├── session_reader.go    # Session 数据读取/过滤
├── handler_test.go      # Handler 单元测试
├── openresponses_test.go
├── openresponses_e2e_test.go
├── README.md            # 协议文档
├── SSE_LLM.md           # 全链路架构文档
└── MESSAGE_FORMAT.md    # 请求/响应报文格式文档
```

---

*文档版本: 2026-05-23 | 基于 dev-lowe-4023 分支*
