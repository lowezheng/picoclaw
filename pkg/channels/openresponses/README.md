# OpenResponses Channel

A PicoClaw channel that exposes an HTTP API compatible with the [OpenResponses specification](https://www.openresponses.org/specification).

## Configuration

Add to your `config.json`:

```json
{
  "channel_list": {
    "openresponses": {
      "enabled": true,
      "type": "openresponses",
      "allow_from": ["*"],
      "settings": {
        "endpoint_path": "/v1/responses",
        "request_timeout": 60,
        "max_body_size": 1048576
      }
    }
  }
}
```

Add the secret token to `.security.yml`:

```yaml
channel_list:
  openresponses:
    settings:
      token: "24cdcc7e2ed4ab5f67ed12301685a412"
```


## OpenResponses Specification Reference

This section documents the official [OpenResponses specification](https://www.openresponses.org/specification) for reference when implementing or extending this channel.

### API Endpoint

| Method | Path | Content-Type |
|---|---|---|
| `POST` | `/v1/responses` | `application/json` |

### Request Object

| Field | Type | Required | Description |
|---|---|---|---|
| `model` | `string` | Yes | Model identifier |
| `input` | `string` \| `array` | Yes | Single prompt string, or array of InputItem objects |
| `instructions` | `string` | No | System/developer instructions |
| `tools` | `array` | No | Array of tool definitions |
| `tool_choice` | `string` \| `object` | No | `"auto"` / `"required"` / `"none"` / `{type: "function", name: string}` |
| `allowed_tools` | `array` | No | Subset of tools permitted for this request |
| `parallel_tool_calls` | `boolean` | No | Allow parallel tool invocations |
| `max_tool_calls` | `integer` | No | Limit on tool call rounds |
| `max_output_tokens` | `integer` | No | Maximum tokens to generate |
| `temperature` | `number` (0-2) | No | Sampling temperature |
| `top_p` | `number` (0-1) | No | Nucleus sampling |
| `stream` | `boolean` | No | Enable SSE streaming |
| `background` | `boolean` | No | Return immediately, process async |
| `store` | `boolean` | No | Persist the response server-side |
| `previous_response_id` | `string` | No | Continue a prior conversation statefully |
| `truncation` | `string` | No | `"auto"` or `"disabled"` |
| `service_tier` | `string` | No | Priority hint: `"standard"` / `"priority"` / `"batch"` |
| `reasoning` | `object` | No | Reasoning config: `{ effort, summary }` |
| `text.format` | `object` | No | Structured output JSON schema (replaces `response_format`) |
| `metadata` | `object` | No | Custom key-value metadata |

### Response Object

```json
{
  "id": "resp_abc123",
  "object": "response",
  "model": "gpt-4o",
  "status": "completed",
  "output": [ /* OutputItem[] */ ],
  "usage": {
    "input_tokens": 14,
    "output_tokens": 18
  }
}
```

| Field | Type | Description |
|---|---|---|
| `id` | `string` | Unique response identifier |
| `object` | `string` | Always `"response"` |
| `model` | `string` | Model that generated the response |
| `status` | `string` | State machine value |
| `output` | `array` | Array of output items |
| `usage` | `object` | Token consumption: `{ input_tokens, output_tokens }` |
| `error` | `object` | Error object (only when `status: "failed"`) |

### Input Items

Items are the atomic unit of context. They are **bidirectional** (can be sent as inputs or returned as outputs).

#### `message` (user input)

```json
{
  "input": "Why do developers prefer dark mode?",
  "stream": true,
  "conversation_id": "{uuid}"
}
```


Content can be a string or an array of content parts for multimodal input:

```json
{
  "stream": true,
  "conversation_id": "{uuid}",
  "content": [
    { "type": "input_text", "content": "Describe this image" },
    { "type": "input_image", "content": "data:xxxx/pdf;base64,xxxx" }
  ]
}
```

### Output Items

All items share these required fields: `id`, `type`, `status`.

#### `message`

```json
{
  "type": "message",
  "id": "msg_xxx",
  "role": "assistant",
  "status": "completed",
  "content": [ /* ContentPart[] */ ]
}
```

#### `function_call`

```json
{
  "type": "function_call",
  "id": "fc_xxx",
  "status": "completed",
  "name": "get_weather",
  "call_id": "call_xxx",
  "arguments": "{\"city\":\"Beijing\"}"
}
```
#### `function_call_output` / `tool_result` (tool results returned to model)

```json
{
  "type": "function_call_output",
  "call_id": "call_xxx",
  "output": "25 degrees and sunny"
}
```
#### `reasoning`

```json
{
  "type": "reasoning",
  "id": "rs_xxx",
  "status": "completed",
  "content": [{ "type": "output_text", "text": "..." }],
  "encrypted_content": null,
  "summary": [{ "type": "summary_text", "text": "..." }]
}
```

> Extension rule: New item types must be prefixed with the implementor slug, e.g. `openai:web_search_call`.

### Content Part Types

#### `output_text` (model output)

```json
{
  "type": "output_text",
  "text": "Hello, world!",
  "annotations": []
}
```

#### `output_image` (model output — reserved/future)

```json
{
  "type": "output_image",
  "image_url": "https://..."
}
```
```json
{
  "type": "output_image",
  "image_url": "data:image/png;base64,XXXXXX"
}
```

> Spec extension: `output_image` is not yet widely implemented but is documented as a future content part type.

#### `input_text` (user input)

```json
{
  "type": "input_text",
  "text": "你好"
}
```

#### `input_image` (user input)

```json
{
  "type": "input_image",
  "image_url": "https://..."
}
```

#### `input_file` (user input)

```json
{
  "type": "input_file",
  "file_id": "assistant-123456789"
}
```

Also supports `file_data` (base64) and `file_url`.

#### `input_audio` (user input)

```json
{
  "type": "input_audio",
  "audio_data": "base64-encoded-audio",
  "format": "mp3"
}
```

#### `summary_text` (reasoning summary)

```json
{
  "type": "summary_text",
  "text": "用户正在询问天气"
}
```

#### `refusal` (model refusal)

```json
{
  "type": "refusal",
  "refusal": "I cannot answer this question."
}
```

#### `function_call_arguments` (streaming content part)

Used within a `function_call` item during SSE streaming. The content part type is `function_call_arguments`, producing `response.function_call_arguments.delta` and `.done` events.

```json
{
  "type": "function_call_arguments",
  "arguments": "{\"city\":\"Beijing\"}"
}
```

### Function Tool Schema

```json
{
  "type": "function",
  "name": "get_weather",
  "description": "Get the weather for a given city",
  "parameters": {
    "type": "object",
    "properties": {
      "city": {
        "type": "string",
        "description": "The city name"
      }
    },
    "required": ["city"]
  }
}
```

### Error Response

```json
{
  "error": {
    "message": "human-readable description",
    "type": "invalid_request",
    "param": "field_name",
    "code": "model_not_found"
  }
}
```

| `type` | HTTP Status | Description |
|---|---|---|
| `server_error` | 500 | Unexpected server condition |
| `invalid_request` | 400 | Malformed/invalid request |
| `not_found` | 404 | Resource does not exist |
| `model_error` | 500 | Model failed processing |
| `too_many_requests` | 429 | Rate limit exceeded |

### Status State Machine

#### Response-level states

```
"queued" -> "in_progress" -> "completed"
                            -> "failed"
                            -> "incomplete"
```

#### Item-level states

| State | Description | Terminal? |
|---|---|---|
| `in_progress` | Currently emitting tokens | No |
| `completed` | Finished successfully | Yes |
| `incomplete` | Token budget exhausted | Yes |
| `failed` | Error occurred | Yes |

Rule: If an item ends in a terminal state, it must be the last item emitted, and the containing response must also be in an `incomplete` state.

---

## SSE Streaming Events — Frontend Integration Guide

OpenResponses uses **semantic events** (not raw text deltas) for streaming. This section provides a complete event type reference for frontend SSE consumers.

### Quick Reference

| # | Event Type | Category | Description |
|---|---|---|---|
| 1 | `response.in_progress` | State Machine | Response begins processing |
| 2 | `response.output_item.added` | Lifecycle | New output item created |
| 3 | `response.content_part.added` | Lifecycle | New content part started |
| 4 | `response.output_text.delta` | Delta | Text fragment added |
| 5 | `response.output_text.done` | Delta | Text stream ended |
| 6 | `response.reasoning_text.delta` | Delta | Reasoning fragment added |
| 7 | `response.reasoning_text.done` | Delta | Reasoning stream ended |
| 8 | `response.function_call_arguments.delta` | Delta | Function args fragment added |
| 9 | `response.function_call_arguments.done` | Delta | Function args stream ended |
| 10 | `response.content_part.done` | Lifecycle | Content part finalized |
| 11 | `response.output_item.done` | Lifecycle | Output item finalized |
| 12 | `response.completed` | State Machine | Response finished successfully |
| 13 | `response.failed` | State Machine | Response encountered an error |
| 14 | `[DONE]` | Terminator | Stream ended |

### Common Event Fields

All events share these fields:

| Field | Type | Description |
|---|---|---|
| `type` | `string` | Event type name (matches SSE `event:` field) |
| `sequence_number` | `int` | Monotonically increasing sequence |

Item-targeting events add:

| Field | Type | Description |
|---|---|---|
| `item_id` | `string` | Target item ID |
| `output_index` | `int` | Position in response `output` array |

Content-level events add:

| Field | Type | Description |
|---|---|---|
| `content_index` | `int` | Position within item's `content` array |

Delta events add:

| Field | Type | Description |
|---|---|---|
| `delta` | `string` | Incremental content fragment |

---

### Event Type Specifications

#### 1. `response.in_progress`

Response moves from `queued` to `in_progress`.

```json
{
  "type": "response.in_progress",
  "sequence_number": 0,
  "response": {
    "id": "resp_xxx",
    "object": "response",
    "status": "in_progress",
    "output": [],
    "usage": { "input_tokens": 0, "output_tokens": 0 }
  }
}
```

| Field | Type | Description |
|---|---|---|
| `response` | `Response` | Partial response with `status: "in_progress"` |

---

#### 2. `response.output_item.added`

New output item created (message / reasoning / function_call).

```json
{
  "type": "response.output_item.added",
  "sequence_number": 1,
  "output_index": 0,
  "item": {
    "type": "message",
    "id": "msg_xxx",
    "status": "in_progress",
    "role": "assistant",
    "content": []
  }
}
```

| Field | Type | Description |
|---|---|---|
| `output_index` | `int` | Position in `output` array |
| `item` | `ResponseItem` | The new item (status: `in_progress`) |

---

#### 3. `response.content_part.added`

New content part started within an item.

```json
{
  "type": "response.content_part.added",
  "sequence_number": 2,
  "item_id": "msg_xxx",
  "output_index": 0,
  "content_index": 0,
  "part": {
    "type": "output_text",
    "text": ""
  }
}
```

| Field | Type | Description |
|---|---|---|
| `item_id` | `string` | Parent item ID |
| `output_index` | `int` | Item position in output array |
| `content_index` | `int` | Part position in item's content array |
| `part` | `Content` | Content part with zero values (`text: ""`, `image_url: ""`) |

---

#### 4. `response.output_text.delta`

Text fragment increment. Repeated many times.

```json
{
  "type": "response.output_text.delta",
  "sequence_number": 3,
  "item_id": "msg_xxx",
  "output_index": 0,
  "content_index": 0,
  "delta": "Hello! "
}
```

| Field | Type | Description |
|---|---|---|
| `item_id` | `string` | Target message item ID |
| `output_index` | `int` | Item position |
| `content_index` | `int` | Part position |
| `delta` | `string` | Text fragment to append |

---

#### 5. `response.output_text.done`

Text delta sequence ended.

```json
{
  "type": "response.output_text.done",
  "sequence_number": 5,
  "item_id": "msg_xxx",
  "output_index": 0,
  "content_index": 0
}
```

---

#### 6. `response.reasoning_text.delta`

Reasoning fragment increment. Repeated.

```json
{
  "type": "response.reasoning_text.delta",
  "sequence_number": 6,
  "item_id": "rs_xxx",
  "output_index": 0,
  "content_index": 0,
  "delta": "Let me think..."
}
```

---

#### 7. `response.reasoning_text.done`

Reasoning delta sequence ended.

```json
{
  "type": "response.reasoning_text.done",
  "sequence_number": 8,
  "item_id": "rs_xxx",
  "output_index": 0,
  "content_index": 0
}
```

---

#### 8. `response.function_call_arguments.delta`

Function call arguments fragment increment.

```json
{
  "type": "response.function_call_arguments.delta",
  "sequence_number": 9,
  "item_id": "fc_xxx",
  "output_index": 0,
  "content_index": 0,
  "delta": "{\"city\":\"Beijing\"}"
}
```

---

#### 9. `response.function_call_arguments.done`

Function call arguments delta sequence ended.

```json
{
  "type": "response.function_call_arguments.done",
  "sequence_number": 10,
  "item_id": "fc_xxx",
  "output_index": 0,
  "content_index": 0
}
```

---

#### 10. `response.content_part.done`

Content part finalized. Contains final content.

```json
{
  "type": "response.content_part.done",
  "sequence_number": 11,
  "item_id": "msg_xxx",
  "output_index": 0,
  "content_index": 0,
  "part": {
    "type": "output_text",
    "text": "Hello! I'm doing well."
  }
}
```

| Field | Type | Description |
|---|---|---|
| `part` | `Content` | Final content with accumulated value |

---

#### 11. `response.output_item.done`

Output item finalized. Contains completed item.

```json
{
  "type": "response.output_item.done",
  "sequence_number": 12,
  "output_index": 0,
  "item": {
    "type": "message",
    "id": "msg_xxx",
    "status": "completed",
    "role": "assistant",
    "content": [
      { "type": "output_text", "text": "Hello! I'm doing well." }
    ]
  }
}
```

| Field | Type | Description |
|---|---|---|
| `output_index` | `int` | Final position |
| `item` | `ResponseItem` | Completed item (status: `completed`) |

---

#### 12. `response.completed`

Response finished. Contains final response with all output items.

```json
{
  "type": "response.completed",
  "sequence_number": 13,
  "response": {
    "id": "resp_xxx",
    "object": "response",
    "status": "completed",
    "output": [
      {
        "type": "message",
        "id": "msg_xxx",
        "status": "completed",
        "role": "assistant",
        "content": [
          { "type": "output_text", "text": "Hello! I'm doing well." }
        ]
      }
    ],
    "usage": { "input_tokens": 0, "output_tokens": 0 }
  }
}
```

---

#### 13. `response.failed`

Response encountered an error.

```json
{
  "type": "response.failed",
  "sequence_number": 5,
  "response": {
    "id": "resp_xxx",
    "object": "response",
    "status": "failed",
    "output": [],
    "error": {
      "message": "Model processing failed",
      "type": "model_error",
      "code": "model_not_found"
    }
  }
}
```

---

#### 14. `[DONE]`

Terminal marker — the literal string `data: [DONE]\n\n`.

```
data: [DONE]
```

### Event Sequence Examples

#### Text message

```
response.in_progress

  output_item.added           (type: message, status: in_progress)
    content_part.added        (part.type: output_text, text: "")
      output_text.delta       (repeated many times)
      output_text.delta
      ...
      output_text.done
    content_part.done         (part contains final text)
  output_item.done            (type: message, status: completed)

response.completed

[DONE]
```

#### Reasoning item

```
response.in_progress

  output_item.added           (type: reasoning, status: in_progress)
    content_part.added        (part.type: reasoning_text, text: "")
      reasoning_text.delta    (repeated)
      reasoning_text.delta
      ...
      reasoning_text.done
    content_part.done         (part contains final reasoning)
  output_item.done            (type: reasoning, status: completed)

response.completed

[DONE]
```

#### Function call

```
response.in_progress

  output_item.added           (type: function_call, status: in_progress)
    content_part.added        (part.type: function_call_arguments, arguments: "")
      function_call_arguments.delta   (repeated)
      function_call_arguments.done
    content_part.done         (part contains final arguments)
  output_item.done            (type: function_call, status: completed)

response.completed
```

#### Image output

```
response.in_progress

  output_item.added           (type: message, status: in_progress)
    content_part.added        (part.type: output_image, image_url: "")
    content_part.done         (part contains base64 data URL)
  output_item.done            (type: message, status: completed)

response.completed
```

### Event Order Rules

For each output item, events always follow this nesting:

```
output_item.added
  content_part.added
    <content_type>.delta   (0..N times)
    <content_type>.done
  content_part.done
output_item.done
```

Cross-item ordering:
- An item in `in_progress` state **must** be closed before a new item of the same "slot" starts
- `turn_end` closes all active items; after `turn_end`, a new item may begin
- The response terminates with `response.completed` or `response.failed`, then `[DONE]`

### Hierarchical Relationship

```
Response
  └── output_items[]          <-- output_item level
        └── Item
              └── content[]   <-- content_part level
                    └── Content Part
                          └── Delta
```

---

## Implementation Detail — Internal Event Pipeline

All agent outputs (`OutboundMessage`) are normalized into a single internal event pipeline (`streamEvent`) before being emitted as SSE events. This centralization ensures consistent event ordering and state machine transitions regardless of output type.

| Internal Event (`streamEventKind`) | Source (`message_kind`) | Mapped SSE Content Type | Fields | Description |
|---|---|---|---|---|
| `text_delta` | Streamer (`Update`) | `output_text` | `content: string` | Incremental text token from streaming mode. Accumulated by handler into `lastTextContent`. |
| `text` | `OutboundMessage` (default) | `output_text` | `content: string` | Complete text from non-streaming mode, or any non-special outbound message (e.g. `tool_timing`). |
| `reasoning` | `thought` | `reasoning_text` | `content: string` | Model reasoning / thought content. **Note:** Unlike text, there is no separate internal `reasoning_delta` event — both incremental deltas (via `UpdateReasoning`) and full text (via `Send`) use the same `eventKindReasoning` type. The handler accumulates them into `lastReasoningContent` and emits `response.reasoning_text.delta` SSE events uniformly. |
| `image` | `SendMedia` | `output_image` | `imageURL: string`, `caption: string` | Base64 data URL image output from media store. |
| `function_call` | `function_call` | `function_call_arguments` | `callID: string`, `name: string`, `arguments: string` | Tool invocation. Arguments may arrive as deltas (via `UpdateToolCall`) or complete (via `Send`). |
| `turn_end` | `turn_end` | — | — | Signals the end of a turn. Closes all active `in_progress` items and triggers `response.completed`. |

**Note on `tool_timing`:** This message kind is allowed through the streamer filter and maps to `eventKindText` (treated as informational text output).

