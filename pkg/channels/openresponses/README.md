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
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 24cdcc7e2ed4ab5f67ed12301685a412" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "Tell me a short story",
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

### 5. Invalid token (should return 401)

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer wrong-token" \
  -H "Content-Type: application/json" \
  -d '{"input": "test"}'
```

### 6. Empty input (should return 400)

```bash
curl -X POST http://localhost:18790/v1/responses \
  -H "Authorization: Bearer 24cdcc7e2ed4ab5f67ed12301685a412" \
  -H "Content-Type: application/json" \
  -d '{"input": "   "}'
```

## Expected JSON Response

```json
{
  "id": "resp_xxx",
  "object": "response",
  "created_at": 1712345678,
  "status": "completed",
  "output": [
    {
      "type": "message",
      "id": "msg_xxx",
      "status": "completed",
      "role": "assistant",
      "content": [
        {
          "type": "output_text",
          "text": "Hello! I'm doing well, thank you for asking. How can I help you today?"
        }
      ]
    }
  ]
}
```

## Expected SSE Stream

```
event: response.created
data: {"type":"response.created","sequence_number":0,"response":{...}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{...}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":2,"item_id":"msg_xxx","output_index":0,"content_index":0}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_xxx","output_index":0,"content_index":0,"delta":"Hello! I'm doing well..."}

event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":4,"item_id":"msg_xxx","output_index":0,"content_index":0}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":5,"item_id":"msg_xxx","output_index":0,"content_index":0}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{...}}

event: response.completed
data: {"type":"response.completed","sequence_number":7,"response":{...}}

data: [DONE]
```
