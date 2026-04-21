# OpenResponses-变种 请求与响应报文格式


## curl Test Examples

### 1. Basic non-streaming request

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "Hello, how are you?"
  }'
```


### 2. Request with conversation_id (session continuity)

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "What is the weather like?",
    "conversation_id": "conv_125"
  }'
```

### 3. SSE streaming request

```bash
curl -N -v -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "今天天气",
    "stream": true
  }'
```

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263" \
  -H "Content-Type: application/json" \
  -d '{
    "input": [
      {"type": "input_text", "content": "Explain quantum computing"}
    ],
    "conversation_id": "conv_456",
    "stream": true
  }'
```

### 4. Invalid token (should return 401)

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer wrong-token" \
  -H "Content-Type: application/json" \
  -d '{"input": "test"}'
```

### 5. Empty input (should return 400)

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263" \
  -H "Content-Type: application/json" \
  -d '{"input": "   "}'
```

---

## Session API Test Examples

### 1. List sessions

```bash
curl -X GET "http://localhost:18790/v1/responses/sessions?offset=1&limit=20" \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263"
```

### 2. Get session detail

```bash
curl -X GET http://localhost:18790/v1/responses/sessions/conv_123 \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263"
```

### 3. Delete session

```bash
curl -X DELETE http://localhost:18790/v1/responses/sessions/conv_123 \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263"
```

---






## 1. 请求报文

### 1.1 基础请求

```http
POST /v1/responses
Authorization: Bearer {token}
Content-Type: application/json
```

```json
{
  "input": "Hello, how are you?",
  "stream": true,
  "conversation_id": "{uuid}"
}
```

### 1.2 带上下文的多轮对话
1-n个
```json
{
  "input": [
    { "type": "input_text", "content": "Describe this image" },
    { "type": "input_text", "content": "Describe this image" }
  ],
  "conversation_id": "{uuid}",
  "stream": true
}
```

### 1.3 多模态输入（文本+图片）

```json
{
  "input": [
    { "type": "input_text", "content": "Describe this image" },
    { "type": "input_image", "content": "data:image/png;base64,XXXXXX" }
  ],
  "conversation_id": "{uuid}",
  "stream": true
}
```

### 1.4 带文件输入

```json
{
  "input": [
    { "type": "input_text", "content": "Analyze this document" },
    { "type": "input_file", "content": "data:application/pdf;base64,XXXXXX" }
  ],
  "stream": true
}
```

### 1.5 请求字段说明

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `input` | `string` / `array` | 是 | 用户输入，字符串或 InputItem 数组 |
| `stream` | `boolean` | 否 | 是否启用 SSE 流式返回，默认 `false` |
| `conversation_id` | `string` | 否 | 会话 ID，用于多轮对话连续性 |
| `model` | `string` | 否 | 模型标识 |
| `instructions` | `string` | 否 | 系统/开发者指令 |
| `max_output_tokens` | `integer` | 否 | 最大生成 token 数 |
| `temperature` | `number` | 否 | 采样温度 (0-2) |

---

## 2. SSE 响应报文

所有响应均采用 SSE (Server-Sent Events) 格式：

```
event: {event_type}
data: {json_payload}

```

### 2.1 事件类型速查

| # | 事件类型 | 类别 | 说明 |
|---|---------|------|------|
| 1 | `response.in_progress` | 状态机 | 响应开始处理 |
| 2 | `response.output_item.added` | 生命周期 | 新输出项创建 |
| 3 | `response.content_part.added` | 生命周期 | 新内容片段开始 |
| 4 | `response.output_text.delta` | 增量 | 文本片段 |
| 5 | `response.output_text.done` | 增量 | 文本流结束 |
| 6 | `response.reasoning_text.delta` | 增量 | 推理片段 |
| 7 | `response.reasoning_text.done` | 增量 | 推理流结束 |
| 8 | `response.function_call_arguments.delta` | 增量 | 函数参数片段 |
| 9 | `response.function_call_arguments.done` | 增量 | 函数参数流结束 |
| 10 | `response.content_part.done` | 生命周期 | 内容片段完成 |
| 11 | `response.output_item.done` | 生命周期 | 输出项完成 |
| 12 | `response.completed` | 状态机 | 响应完成 |
| 13 | `response.failed` | 状态机 | 响应失败 |
| 14 | `[DONE]` | 终止符 | 流结束标记 |

### 2.2 通用字段

所有事件共享字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `type` | `string` | 事件类型名 |
| `sequence_number` | `int` | 单调递增序列号 |

Item 相关事件附加：

| 字段 | 类型 | 说明 |
|------|------|------|
| `item_id` | `string` | 目标 item ID |
| `output_index` | `int` | 在 output 数组中的位置 |

Content 相关事件附加：

| 字段 | 类型 | 说明 |
|------|------|------|
| `content_index` | `int` | 在 item content 数组中的位置 |

Delta 事件附加：

| 字段 | 类型 | 说明 |
|------|------|------|
| `delta` | `string` | 增量内容片段 |

### 2.3 各事件详细示例

#### ① response.in_progress — 响应开始

```
event: response.in_progress
data: {"type":"response.in_progress","sequence_number":0,"response":{"id":"resp_xxx","object":"response","status":"in_progress","output":[],"conversation_id":"conv_xxx","usage":{"input_tokens":0,"output_tokens":0}}}
```

#### ② response.output_item.added — 新输出项

**Message 类型：**
```
event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_xxx","status":"in_progress","role":"assistant","content":[]}}
```

**Reasoning 类型：**
```
event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"type":"reasoning","id":"rs_xxx","status":"in_progress","content":[]}}
```

**Function Call 类型：**
```
event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":3,"output_index":2,"item":{"type":"function_call","id":"fc_xxx","status":"in_progress","name":"web_search","call_id":"call_xxx"}}
```

#### ③ response.content_part.added — 新内容片段

**Output Text：**
```
event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":4,"item_id":"msg_xxx","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}
```

**Reasoning Text：**
```
event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":5,"item_id":"rs_xxx","output_index":1,"content_index":0,"part":{"type":"reasoning_text"}}
```

**Function Call Arguments：**
```
event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":6,"item_id":"fc_xxx","output_index":2,"content_index":0,"part":{"type":"function_call_arguments"}}
```

#### ④ response.output_text.delta — 文本增量

```
event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_xxx","output_index":0,"content_index":0,"delta":"Hello! "}
```

#### ⑤ response.output_text.done — 文本结束

```
event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":8,"item_id":"msg_xxx","output_index":0,"content_index":0}
```

#### ⑥ response.reasoning_text.delta — 推理增量

```
event: response.reasoning_text.delta
data: {"type":"response.reasoning_text.delta","sequence_number":9,"item_id":"rs_xxx","output_index":1,"content_index":0,"delta":"Let me think..."}
```

#### ⑦ response.reasoning_text.done — 推理结束

```
event: response.reasoning_text.done
data: {"type":"response.reasoning_text.done","sequence_number":10,"item_id":"rs_xxx","output_index":1,"content_index":0}
```

#### ⑧ response.function_call_arguments.delta — 函数参数增量

```
event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":11,"item_id":"fc_xxx","output_index":2,"content_index":0,"delta":"{\"city\":\"Beijing\"}"}
```

#### ⑨ response.function_call_arguments.done — 函数参数结束

```
event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","sequence_number":12,"item_id":"fc_xxx","output_index":2,"content_index":0}
```

#### ⑩ response.content_part.done — 内容片段完成

**Output Text：**
```
event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":13,"item_id":"msg_xxx","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello! I'm doing well."}}
```

**Reasoning Text：**
```
event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":14,"item_id":"rs_xxx","output_index":1,"content_index":0,"part":{"type":"reasoning_text","text":"用户想要查看股价..."}}
```

**Function Call Arguments：**
```
event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":15,"item_id":"fc_xxx","output_index":2,"content_index":0,"part":{"type":"function_call_arguments","arguments":"{\"city\":\"Beijing\"}"}}
```

#### ⑪ response.output_item.done — 输出项完成

**Message：**
```
event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":16,"output_index":0,"item":{"type":"message","id":"msg_xxx","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello! I'm doing well."}]}}
```

**Reasoning：**
```
event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":17,"output_index":1,"item":{"type":"reasoning","id":"rs_xxx","status":"completed","content":[{"type":"reasoning_text","text":"用户想要查看股价..."}]}}
```

**Function Call：**
```
event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":18,"output_index":2,"item":{"type":"function_call","id":"fc_xxx","status":"completed","name":"web_search","call_id":"call_xxx","arguments":"{\"city\":\"Beijing\"}"}}
```

#### ⑫ response.completed — 响应完成

```
event: response.completed
data: {"type":"response.completed","sequence_number":19,"response":{"id":"resp_xxx","object":"response","status":"completed","output":[...],"usage":{"input_tokens":0,"output_tokens":0}}}
```

**`response` 字段包含完整 output 数组，汇总了所有已完成的 items。**

#### ⑬ response.failed — 响应失败

```
event: response.failed
data: {"type":"response.failed","sequence_number":20,"response":{"id":"resp_xxx","object":"response","status":"failed","output":[],"error":{"message":"Model processing failed","type":"model_error","code":"model_not_found"}}}
```

#### ⑭ [DONE] — 流终止

```
data: [DONE]
```

---

## 3. 事件序列示例

### 3.1 纯文本回复

```
response.in_progress
  output_item.added       (type: message)
    content_part.added    (type: output_text)
      output_text.delta   (×N)
      output_text.done
    content_part.done
  output_item.done
response.completed
[DONE]
```

### 3.2 带推理的回复

```
response.in_progress
  output_item.added       (type: reasoning)
    content_part.added    (type: reasoning_text)
      reasoning_text.delta (×N)
      reasoning_text.done
    content_part.done
  output_item.done
  output_item.added       (type: message)
    content_part.added    (type: output_text)
      output_text.delta   (×N)
      output_text.done
    content_part.done
  output_item.done
response.completed
[DONE]
```

### 3.3 带工具调用的回复

```
response.in_progress
  output_item.added       (type: reasoning)
    content_part.added    (type: reasoning_text)
      reasoning_text.delta (×N)
      reasoning_text.done
    content_part.done
  output_item.done
  output_item.added       (type: message)
    content_part.added    (type: output_text)
      output_text.delta   (×N)
      output_text.done
    content_part.done
  output_item.done
  output_item.added       (type: function_call)
    content_part.added    (type: function_call_arguments)
      function_call_arguments.delta (×N)
      function_call_arguments.done
    content_part.done
  output_item.done
response.completed
[DONE]
```

### 3.4 多轮工具调用（实际场景）

```
response.in_progress

  ├─ reasoning item
  │    content_part.added (reasoning_text)
  │      reasoning_text.delta ×N
  │      reasoning_text.done
  │    content_part.done
  │  output_item.done

  ├─ message item (推理耗时)
  │    content_part.added (output_text)
  │      output_text.delta: "🤖 LLM 推理 in Xs"
  │      output_text.done
  │    content_part.done
  │  output_item.done

  ├─ function_call item (工具调用)
  │    content_part.added (function_call_arguments)
  │      function_call_arguments.delta ×N
  │      function_call_arguments.done
  │    content_part.done
  │  output_item.done

  ├─ message item (工具执行结果)
  │    content_part.added (output_text)
  │      output_text.delta: "✅/❌ `tool_name` 工具调用 in Xms"
  │      output_text.done
  │    content_part.done
  │  output_item.done

  ├─ reasoning item (再次推理)
  │    ...

  ├─ function_call item (再次工具调用)
  │    ...

  └─ message item (最终回答)
       content_part.added (output_text)
         output_text.delta ×N
         output_text.done
       content_part.done
     output_item.done

response.completed
[DONE]
```

---

## 4. 事件嵌套规则

每个 output item 的事件遵循严格嵌套：

```
output_item.added
  content_part.added
    <content_type>.delta    (0..N 次)
    <content_type>.done
  content_part.done
output_item.done
```

跨 item 顺序规则：
- 同类型 item 在 `in_progress` 状态时，**必须先关闭当前 item** 才能开始新 item
- `response.completed` 或 `response.failed` 后紧跟 `[DONE]`

层级关系：

```
Response
  └── output_items[]          ← output_item 层级
        └── Item
              └── content[]   ← content_part 层级
                    └── Content Part
                          └── Delta
```

---

## 5. 输出项类型汇总

### 5.1 message（助手消息）

```json
{
  "type": "message",
  "id": "msg_xxx",
  "role": "assistant",
  "status": "completed",
  "content": [
    { "type": "output_text", "text": "..." }
  ]
}
```

### 5.2 reasoning（推理内容）

```json
{
  "type": "reasoning",
  "id": "rs_xxx",
  "status": "completed",
  "content": [
    { "type": "reasoning_text", "text": "..." }
  ]
}
```

### 5.3 function_call（工具调用）

```json
{
  "type": "function_call",
  "id": "fc_xxx",
  "status": "completed",
  "name": "web_search",
  "call_id": "call_xxx",
  "arguments": "{\"query\":\"...\"}"
}
```

### 5.4 output_image（图片输出）

```json
{
  "type": "message",
  "id": "msg_xxx",
  "role": "assistant",
  "status": "completed",
  "content": [
    { "type": "output_image", "content": "data:image/png;base64,XXXXXX" }
  ]
}
```

---

## 6. 状态机

### Response 级别状态

```
"queued" -> "in_progress" -> "completed"
                            -> "failed"
                            -> "incomplete"
```

### Item 级别状态

| 状态 | 说明 | 是否终结 |
|------|------|---------|
| `in_progress` | 正在生成 | 否 |
| `completed` | 成功完成 | 是 |
| `incomplete` | token 预算耗尽 | 是 |
| `failed` | 发生错误 | 是 |
