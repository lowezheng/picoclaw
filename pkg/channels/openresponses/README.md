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

## curl Test Examples

### 1. Basic non-streaming request

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 24cdcc7e2ed4ab5f67ed12301685a412" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "Hello, how are you?"
  }'
```

### 2. Request with conversation_id (session continuity)

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 24cdcc7e2ed4ab5f67ed12301685a412" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "What is the weather like?",
    "conversation_id": "conv_123"
  }'
```

### 3. SSE streaming request

```bash
curl -N -v -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 570694ff7910121aaf9feea5f42e6263" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "为用户建立一个自动化的周报生成系统",
    "stream": true
  }'
```

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 24cdcc7e2ed4ab5f67ed12301685a412" \
  -H "Content-Type: application/json" \
  -d '{
    "input": [
      {"type": "message", "role": "user", "content": "Explain quantum computing"}
    ],
    "conversation_id": "conv_456"
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
  -H "Authorization: Bearer 24cdcc7e2ed4ab5f67ed12301685a412" \
  -H "Content-Type: application/json" \
  -d '{"input": "   "}'
```

---

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
  "type": "message",
  "role": "user",
  "content": "Why do developers prefer dark mode?"
}
```

Content can be a string or an array of content parts for multimodal input:

```json
{
  "type": "message",
  "role": "user",
  "content": [
    { "type": "input_text", "text": "Describe this image" },
    { "type": "input_image", "image_url": "https://example.com/image.png" }
  ]
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

## SSE Streaming Events

OpenResponses uses **semantic events** (not raw text deltas) for streaming.

### Event Classification

| Category | Definition |
|---|---|
| **Delta Events** | Represent a change to an object since its last update |
| **State Machine Events** | Represent a change in the status of an object |

### State Machine Events

| Event | Trigger |
|---|---|
| `response.in_progress` | Response moves from `queued` to `in_progress` |
| `response.completed` | Response finished successfully |
| `response.failed` | Response encountered an error |

### Delta Events

| Event | Level | Trigger |
|---|---|---|
| `response.output_item.added` | output item | New output item generated |
| `response.content_part.added` | content part | New content part started |
| `response.<content_type>.delta` | content | Content fragment increment (repeated) |
| `response.<content_type>.done` | content | Content delta sequence ended |
| `response.content_part.done` | content part | Content part closed |
| `response.output_item.done` | output item | Output item closed |

> `<content_type>` is a placeholder. Actual values depend on the content part's `type`, e.g. `output_text`, `function_call_arguments`.

### Hierarchical Relationship

```
Response
  └── output_items[]          <-- output_item level
        └── Item
              └── content[]   <-- content_part level
                    └── Content Part
                          └── Delta
```

### Streaming Event Sequence

For a text message:

```
response.in_progress

  output_item.added           (status: in_progress)
    content_part.added        (part includes type and zero values)
      output_text.delta       (repeated many times)
      output_text.delta
      ...
      output_text.done
    content_part.done
  output_item.done            (status: completed)

response.completed

[DONE]
```

For a function call:

```
response.in_progress

  output_item.added           (type: function_call, status: in_progress)
    content_part.added
      function_call_arguments.delta   (repeated)
      function_call_arguments.done
    content_part.done
  output_item.done            (status: completed)

response.completed
```

### Common Event Fields

All events must include:

```json
{
  "type": "response.output_text.delta",
  "sequence_number": 10
}
```

Item-targeting events add:
- `item_id` -- the target item's ID
- `output_index` -- position in the response `output` array

Content-level events add:
- `content_index` -- position within the item's `content` array

Delta events add:
- `delta` -- the incremental string fragment

### Content Part `part` Field

`response.content_part.added` and `response.content_part.done` must include a `part` object:

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

At `added`, the `part` contains its `type` and zero values. At `done`, it contains the final content.

### Terminator

The terminal event must be the literal string:

```
data: [DONE]
```

### Full Stream Example

```
event: response.in_progress
data: {"type":"response.in_progress","sequence_number":0,"response":{"id":"resp_xxx","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_xxx","status":"in_progress","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":2,"item_id":"msg_xxx","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_xxx","output_index":0,"content_index":0,"delta":"Hello! "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_xxx","output_index":0,"content_index":0,"delta":"I'm doing well."}

event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":5,"item_id":"msg_xxx","output_index":0,"content_index":0}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":6,"item_id":"msg_xxx","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello! I'm doing well."}}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":{"type":"message","id":"msg_xxx","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello! I'm doing well."}]}}

event: response.completed
data: {"type":"response.completed","sequence_number":8,"response":{"id":"resp_xxx","status":"completed","output":[{"type":"message","id":"msg_xxx","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello! I'm doing well."}]}]}}

data: [DONE]
```
