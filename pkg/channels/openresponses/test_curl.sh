#!/bin/bash
# OpenResponses Channel E2E Test Script
# Run this after starting picoclaw with openresponses channel configured.
#
# Example config:
#   "openresponses": {
#     "enabled": true,
#     "type": "openresponses",
#     "settings": {"token": "test-token-123"}
#   }

set -e

BASE_URL="http://localhost:8080/v1/responses"
TOKEN="test-token-123"

echo "=== OpenResponses Channel E2E Tests ==="
echo ""

# Test 1: Auth failure
echo "Test 1: Auth failure (no token)"
curl -s -X POST "$BASE_URL" \
  -H "Content-Type: application/json" \
  -d '{"input":"hello"}' | jq .
echo ""

# Test 2: Empty input
echo "Test 2: Empty input"
curl -s -X POST "$BASE_URL" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"input":""}' | jq .
echo ""

# Test 3: Non-streaming request
echo "Test 3: Non-streaming request"
curl -s -X POST "$BASE_URL" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"input":"Hello, what can you do?","conversation_id":"conv_test_001"}' | jq .
echo ""

# Test 4: Streaming request (SSE)
echo "Test 4: Streaming request (first 30 lines)"
curl -s -X POST "$BASE_URL" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"input":"Hello","stream":true,"conversation_id":"conv_test_002"}' | head -30
echo ""

# Test 5: Multi-part input with ContentPart
echo "Test 5: Multi-part input"
curl -s -X POST "$BASE_URL" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"input":[{"type":"input_text","content":"Hello"},{"type":"input_text","content":"World"}],"conversation_id":"conv_test_003"}' | jq .
echo ""

# Test 6: Session list
echo "Test 6: Session list"
curl -s "$BASE_URL/sessions" \
  -H "Authorization: Bearer $TOKEN" | jq .
echo ""

# Test 7: Session detail (may 404 if not found)
echo "Test 7: Session detail"
curl -s "$BASE_URL/sessions/conv_test_001" \
  -H "Authorization: Bearer $TOKEN" | jq .
echo ""

echo "=== Tests Complete ==="
