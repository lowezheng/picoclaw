package agent

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestBuildEvaluationInput(t *testing.T) {
	req := &ConfidenceRequest{
		UserMessage:  "How do I use Go concurrency?",
		FinalContent: "Go concurrency is based on goroutines and channels.",
		Iterations:   2,
		Duration:     5 * time.Second,
		Messages: []providers.Message{
			{Role: "user", Content: "How do I use Go concurrency?"},
			{Role: "assistant", Content: "Let me search for that."},
			{Role: "assistant", Content: "", ToolCalls: []providers.ToolCall{
				{ID: "1", Name: "search_web", Function: &providers.FunctionCall{Name: "search_web", Arguments: `{"query":"Go concurrency patterns"}`}},
			}},
			{Role: "tool", Content: "Go concurrency uses goroutines (lightweight threads) and channels for communication."},
			{Role: "assistant", Content: "Go concurrency is based on goroutines and channels."},
		},
	}

	input := buildEvaluationInput(req)

	if input == "" {
		t.Fatal("buildEvaluationInput returned empty string")
	}
	if !contains(input, "用户原始问题") {
		t.Error("missing user question section")
	}
	if !contains(input, "完整对话过程") {
		t.Error("missing conversation history section")
	}
	if !contains(input, "最终回答") {
		t.Error("missing final answer section")
	}
	if !contains(input, "search_web") {
		t.Error("missing tool call info")
	}
	if !contains(input, "goroutines") {
		t.Error("missing tool result content")
	}
}

func TestParseConfidenceScore(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, s *ConfidenceScore)
	}{
		{
			name:  "direct JSON",
			input: `{"overallScore":82,"rating":"⭐⭐⭐⭐","stars":4,"dimensions":[{"name":"事实准确性","score":85,"weight":0.30,"reason":"ok"}],"sources":[{"toolName":"search_web","keyData":"results","citationType":"summary"}]}`,
			check: func(t *testing.T, s *ConfidenceScore) {
				if s.OverallScore != 82 {
					t.Errorf("overallScore = %d, want 82", s.OverallScore)
				}
				if s.Stars != 4 {
					t.Errorf("stars = %d, want 4", s.Stars)
				}
				if len(s.Dimensions) != 1 {
					t.Errorf("dimensions len = %d, want 1", len(s.Dimensions))
				}
				if len(s.Sources) != 1 {
					t.Errorf("sources len = %d, want 1", len(s.Sources))
				}
				if s.Sources[0].CitationType != "summary" {
					t.Errorf("citationType = %s, want summary", s.Sources[0].CitationType)
				}
			},
		},
		{
			name:  "JSON in markdown code block",
			input: "```json\n{\"overallScore\":90,\"rating\":\"⭐⭐⭐⭐⭐\",\"stars\":5,\"dimensions\":[],\"sources\":[]}\n```",
			check: func(t *testing.T, s *ConfidenceScore) {
				if s.OverallScore != 90 {
					t.Errorf("overallScore = %d, want 90", s.OverallScore)
				}
			},
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, err := parseConfidenceScore(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, score)
			}
		})
	}
}

func TestTruncateForEval(t *testing.T) {
	short := "short"
	if truncateForEval(short, 10) != short {
		t.Error("short string should not be truncated")
	}

	long := "this is a very long string that should be truncated"
	result := truncateForEval(long, 10)
	if !contains(result, "truncated") {
		t.Error("long string should be truncated with indicator")
	}

	// Test rune-safe truncation with Chinese characters
	chinese := "这是一个很长的中文字符串用来测试截断功能"
	resultChinese := truncateForEval(chinese, 5)
	if !contains(resultChinese, "truncated") {
		t.Error("Chinese string should be truncated with indicator")
	}
	// Verify no invalid UTF-8
	for _, r := range resultChinese {
		if r == '�' {
			t.Error("truncation produced invalid UTF-8 rune")
		}
	}
}

func TestFormatConfidenceBlock(t *testing.T) {
	score := &ConfidenceScore{
		OverallScore: 82,
		Rating:       "⭐⭐⭐⭐",
		Stars:        4,
		Dimensions: []ScoreDimension{
			{Name: "事实准确性", Score: 85, Weight: 0.30, Reason: "ok"},
		},
		Sources: []DataSource{
			{ToolName: "search_web", KeyData: "results", CitationType: "summary"},
		},
	}

	block, err := FormatConfidenceBlock(score)
	if err != nil {
		t.Fatalf("FormatConfidenceBlock returned error: %v", err)
	}
	if block == "" {
		t.Fatal("FormatConfidenceBlock returned empty string")
	}
	if !contains(block, "---MESSAGE_START---") {
		t.Error("missing MESSAGE_START marker")
	}
	if !contains(block, "---MESSAGE_END---") {
		t.Error("missing MESSAGE_END marker")
	}
	if !contains(block, `"messageType":"confidence"`) {
		t.Error("missing confidence messageType")
	}
	if !contains(block, `"overallScore":82`) {
		t.Error("missing overallScore")
	}
}

func TestFormatConfidenceBlockNil(t *testing.T) {
	block, err := FormatConfidenceBlock(nil)
	if err == nil {
		t.Fatal("expected error for nil score, got nil")
	}
	if block != "" {
		t.Errorf("expected empty string for nil score, got %q", block)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
