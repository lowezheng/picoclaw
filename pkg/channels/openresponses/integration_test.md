# OpenResponses Integration Test — curl 对照手册

> 对应 `integration_test.go` 中的每个测试用例，提供可直接执行的 `curl` 命令。
> Base URL: `http://localhost:18790/v1/responses`
> Token: `test-token-123`

---

## Auth & Validation

### 1. 鉴权失败 (TestIntegration_AuthFailure)

```bash
curl -v -w "\nHTTP %{http_code}\n" -X POST "http://localhost:18790/v1/responses/chat" \
  -H "Content-Type: application/json" \
  -d '{"input":"hello"}'
```

**期望:** `HTTP 401`

---

### 2. 空输入 (TestIntegration_EmptyInput)

```bash
curl -v -w "\nHTTP %{http_code}\n" -X POST "http://localhost:18790/v1/responses/chat" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-token-123" \
  -d '{"input":""}'
```

**期望:** `HTTP 400`

---

### 3. 方法不允许 (TestIntegration_MethodNotAllowed)

```bash
curl -v -w "\nHTTP %{http_code}\n" -X GET "http://localhost:18790/v1/responses/chat" \
  -H "Authorization: Bearer test-token-123"
```

**期望:** `HTTP 405`

---

## Non-streaming Response

### 4. 纯文本非流式响应 (TestIntegration_NonStreaming_Text)

```bash
curl -v -w "\nHTTP %{http_code}\n" -X POST "http://localhost:18790/v1/responses/chat" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-token-123" \
  -d '{
    "input": "Say exactly: Hello from integration test",
    "conversation_id": "conv_integ_text_001"
  }' | jq .
```

**期望:** `HTTP 200`, `status="completed"`, `output[0].type="message"`

---

### 5. 多模态输入 (PDF) 非流式 (TestIntegration_NonStreaming_MultiPartInput)

```bash
# 1. 将 PDF 转为 base64 Data URL
PDF_B64="data:application/pdf;base64,$(base64 -i testdata/test_upload.pdf | tr -d '\n')"

# 2. 发送请求
curl -v -w "\nHTTP %{http_code}\n" -X POST "http://localhost:18790/v1/responses/chat" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-token-123" \
  -d '{
    "input": [
      {"type": "input_text", "content": "请总结上传的PDF文件中的关键信息，用一句话概括"},
      {"type": "input_file", "content": "'"$PDF_B64"'"}
    ],
    "conversation_id": "conv_integ_multi_001"
  }' | jq .
```

**期望:** `HTTP 200`, 返回内容包含 PDF 中的关键词（天空/蓝色/水/沸腾/openresponses）

---

## Streaming SSE Response

### 6. 纯文本流式响应 (TestIntegration_Streaming_Text)

```bash
curl -v -N -X POST "http://localhost:18790/v1/responses/chat" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-token-123" \
  -H "Accept: text/event-stream" \
  -d '{
    "input": "Say exactly: Hello from SSE stream",
    "stream": true,
    "conversation_id": "conv_integ_stream_001"
  }'
```

**期望:** `Content-Type: text/event-stream`, 首事件 `response.in_progress`, 尾事件 `response.completed` + `[DONE]`

---

### 7. 流式事件序列检查 (TestIntegration_Streaming_EventSequence)

```bash
curl -v -N -X POST "http://localhost:18790/v1/responses/chat" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-token-123" \
  -H "Accept: text/event-stream" \
  -d '{
    "input": "Reply with exactly the word: OK",
    "stream": true,
    "conversation_id": "conv_integ_seq_001"
  }'
```

**期望:** 严格以 `response.in_progress` → `response.output_item.added` → `response.content_part.added` 开头；以 `response.completed` → `[DONE]` 结尾。

---

### 8. 多模态输入 (PDF) 流式响应 (TestIntegration_Streaming_MultiPartInput)

```bash
# 1. 将 PDF 转为 base64 Data URL
PDF_B64="data:application/pdf;base64,$(base64 -i testdata/test_upload.pdf | tr -d '\n')"

# 2. 发送流式请求
curl -v -N -X POST "http://localhost:18790/v1/responses/chat" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-token-123" \
  -H "Accept: text/event-stream" \
  -d '{
    "input": [
      {"type": "input_text", "content": "请总结上传的PDF文件中的关键信息，用一句话概括"},
      {"type": "input_file", "content": "'"$PDF_B64"'"}
    ],
    "stream": true,
    "conversation_id": "conv_integ_stream_multi_001"
  }'
```

**期望:** SSE 流中包含多轮 `function_call` 事件，最终文本输出包含 PDF 关键词。

---

## Session API

### 9. 会话列表 (TestIntegration_SessionList)

```bash
curl -v -w "\nHTTP %{http_code}\n" -X GET "http://localhost:18790/v1/responses/sessions" \
  -H "Authorization: Bearer test-token-123" | jq .
```

**期望:** `HTTP 200`, 返回 JSON 数组

---

### 10. 会话详情不存在 (TestIntegration_SessionDetail_NotFound)

```bash
curl -v -w "\nHTTP %{http_code}\n" -X GET "http://localhost:18790/v1/responses/sessions/nonexistent_session_12345" \
  -H "Authorization: Bearer test-token-123" | jq .
```

**期望:** `HTTP 404`, `error.type="not_found"`

---

## 快速运行全部测试

```bash
#!/bin/bash
set -e

echo "=== 1. Auth failure ==="
curl -v -o /dev/null -w "%{http_code}\n" -X POST "http://localhost:18790/v1/responses/chat" -H "Content-Type: application/json" -d '{"input":"hello"}'

echo "=== 2. Empty input ==="
curl -v -o /dev/null -w "%{http_code}\n" -X POST "http://localhost:18790/v1/responses/chat" -H "Content-Type: application/json" -H "Authorization: Bearer test-token-123" -d '{"input":""}'

echo "=== 3. Method not allowed ==="
curl -v -o /dev/null -w "%{http_code}\n" -X GET "http://localhost:18790/v1/responses/chat" -H "Authorization: Bearer test-token-123"

echo "=== 4. Non-streaming text ==="
curl -v -o /dev/null -w "%{http_code}\n" -X POST "http://localhost:18790/v1/responses/chat" -H "Content-Type: application/json" -H "Authorization: Bearer test-token-123" -d '{"input":"hello","conversation_id":"conv_001"}'

echo "=== 5. Streaming text ==="
curl -v -o /dev/null -w "%{http_code}\n" -N -X POST "http://localhost:18790/v1/responses/chat" -H "Content-Type: application/json" -H "Authorization: Bearer test-token-123" -d '{"input":"hello","stream":true,"conversation_id":"conv_stream_001"}'

echo "=== 6. Session list ==="
curl -v -o /dev/null -w "%{http_code}\n" -X GET "http://localhost:18790/v1/responses/sessions" -H "Authorization: Bearer test-token-123"

echo "=== 7. Session not found ==="
curl -v -o /dev/null -w "%{http_code}\n" -X GET "http://localhost:18790/v1/responses/sessions/nonexistent" -H "Authorization: Bearer test-token-123"
```
