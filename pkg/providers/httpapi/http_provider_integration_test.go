//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// loadDefaultModelConfig loads ~/.picoclaw/config.json, resolves the default
// model name from agents.defaults.model_name, finds the matching ModelConfig in
// model_list, and extracts the API endpoint + model identifier.
func loadDefaultModelConfig(t *testing.T) (apiBase, proxy, modelID string, modelCfg *config.ModelConfig) {
	t.Helper()

	home := config.GetHome()
	configPath := os.Getenv(config.EnvConfig)
	if configPath == "" {
		configPath = filepath.Join(home, "config.json")
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(%q) error = %v", configPath, err)
	}

	modelName := cfg.Agents.Defaults.ModelName
	if modelName == "" {
		t.Fatalf("agents.defaults.model_name is empty in %q", configPath)
	}

	var mc *config.ModelConfig
	for _, m := range cfg.ModelList {
		if m != nil && m.ModelName == modelName {
			mc = m
			break
		}
	}
	if mc == nil {
		t.Fatalf("model_name %q not found in model_list", modelName)
	}

	// Parse protocol prefix to get the real model ID sent to the API.
	protocol, id := extractProtocol(mc.Model)
	_ = protocol // protocol determines which provider factory to use; here we always use HTTPProvider

	apiBase = mc.APIBase
	if apiBase == "" {
		apiBase = defaultAPIBase(protocol)
	}

	return apiBase, mc.Proxy, id, mc
}

// defaultAPIBase returns the hard-coded default endpoint for known OpenAI-compatible
// protocols. Mirrors the logic in pkg/providers/factory_provider.go.
func defaultAPIBase(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return "https://api.openai.com/v1"
	case "qwen", "qwen-portal":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1"
	case "qwen-intl", "qwen-international", "dashscope-intl":
		return "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"
	case "qwen-us", "dashscope-us":
		return "https://dashscope-us.aliyuncs.com/compatible-mode/v1"
	case "coding-plan", "alibaba-coding", "qwen-coding":
		return "https://coding-intl.dashscope.aliyuncs.com/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "moonshot":
		return "https://api.moonshot.cn/v1"
	case "zhipu", "glm":
		return "https://open.bigmodel.cn/api/paas/v4"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "cerebras":
		return "https://api.cerebras.ai/v1"
	case "nvidia":
		return "https://integrate.api.nvidia.com/v1"
	case "venice":
		return "https://api.venice.ai/api/v1"
	case "vivgrid":
		return "https://api.vivgrid.com/v1"
	case "volcengine":
		return "https://ark.cn-beijing.volces.com/api/v3"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "avian":
		return "https://api.avian.io/v1"
	case "minimax":
		return "https://api.minimaxi.com/v1"
	case "longcat":
		return "https://api.longcat.chat/openai"
	case "modelscope":
		return "https://api-inference.modelscope.cn/v1"
	case "novita":
		return "https://api.novita.ai/openai"
	case "ollama":
		return "http://localhost:11434/v1"
	case "lmstudio":
		return "http://localhost:1234/v1"
	case "vllm":
		return "http://localhost:8000/v1"
	case "litellm":
		return "http://localhost:4000/v1"
	case "shengsuanyun":
		return "https://router.shengsuanyun.com/api/v1"
	case "mimo":
		return "https://api.xiaomimimo.com/v1"
	default:
		return ""
	}
}

// createHTTPProviderFromConfig builds an HTTPProvider using parameters from the
// loaded ModelConfig, matching the real provider factory logic.
func createHTTPProviderFromConfig(mc *config.ModelConfig) *HTTPProvider {
	return NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
		mc.APIKey(),
		mc.APIBase,
		mc.Proxy,
		mc.MaxTokensField,
		mc.UserAgent,
		mc.RequestTimeout,
		mc.ExtraBody,
		mc.CustomHeaders,
	)
}

// TestIntegration_HTTPProvider_ChatStream connects to the real endpoint configured
// in ~/.picoclaw/config.json (agents.defaults.model_name) and prints every chunk
// received for debugging.
//
// Run with: go test -tags=integration -v ./pkg/providers/httpapi/ -run TestIntegration_HTTPProvider_ChatStream
func TestIntegration_HTTPProvider_ChatStream(t *testing.T) {
	apiBase, proxy, model, mc := loadDefaultModelConfig(t)

	p := createHTTPProviderFromConfig(mc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Say hello and explain what 2+2 equals in one short sentence. Think step by step"},
	}

	t.Logf("=== ChatStream Request ===")
	t.Logf("API Base: %s", apiBase)
	t.Logf("Proxy:    %s", proxy)
	t.Logf("Model:    %s", model)
	t.Logf("Messages: %+v", messages)

	chunkCount := 0
	var finalContent, finalReasoning string

	resp, err := p.ChatStream(ctx, messages, nil, model, nil,
		func(content, reasoning string) {
			chunkCount++
			finalContent = content
			finalReasoning = reasoning

			// Print every chunk so we can see the exact streaming behavior
			if reasoning != "" {
				t.Logf("[chunk %3d] reasoning=%q  \ncontent=%q", chunkCount, reasoning, content)
			} else {
				t.Logf("[chunk %3d] content=%q", chunkCount, content)
			}
		},
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}

	t.Logf("=== ChatStream Response ===")
	t.Logf("Total chunks:   %d", chunkCount)
	t.Logf("Final content:  %q", resp.Content)
	t.Logf("Final reasoning: %q", resp.ReasoningContent)
	t.Logf("Finish reason:  %s", resp.FinishReason)
	if resp.Usage != nil {
		t.Logf("Usage: prompt=%d, completion=%d, total=%d",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}
	if len(resp.ToolCalls) > 0 {
		t.Logf("ToolCalls: %+v", resp.ToolCalls)
	}

	// Basic sanity checks
	if resp.Content == "" {
		t.Error("Content is empty")
	}
	if resp.FinishReason == "" {
		t.Error("FinishReason is empty")
	}
	if chunkCount == 0 {
		t.Error("No chunks received — stream may not be working")
	}

	// Verify final callback matches response
	if finalContent != resp.Content {
		t.Errorf("Final callback content %q != response content %q", finalContent, resp.Content)
	}
	if finalReasoning != resp.ReasoningContent {
		t.Errorf("Final callback reasoning %q != response reasoning %q", finalReasoning, resp.ReasoningContent)
	}
}

// TestIntegration_HTTPProvider_ChatStream_WithToolCall tests streaming when the model
// decides to call a tool. Useful for debugging tool-call delta assembly.
func TestIntegration_HTTPProvider_ChatStream_WithToolCall(t *testing.T) {
	apiBase, proxy, model, mc := loadDefaultModelConfig(t)

	p := createHTTPProviderFromConfig(mc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := []ToolDefinition{
		{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name:        "get_weather",
				Description: "获取全国省会城市的天气",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
					"required": []string{"location"},
				},
			},
		},
	}

	messages := []Message{
		{Role: "system", Content: "You have access to tools. Use them when needed."},
		{Role: "user", Content: "What is the weather in Beijing?"},
	}

	t.Logf("=== ChatStream (with tools) Request ===")
	t.Logf("API Base: %s", apiBase)
	t.Logf("Proxy:    %s", proxy)
	t.Logf("Model:    %s", model)
	t.Logf("Tools:    %+v", tools)

	chunkCount := 0
	resp, err := p.ChatStream(ctx, messages, tools, model, nil,
		func(content, reasoning string) {
			chunkCount++
			t.Logf("[chunk %3d] content=%q reasoning=%q", chunkCount, content, reasoning)
		},
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}

	t.Logf("=== ChatStream (with tools) Response ===")
	t.Logf("Total chunks:   %d", chunkCount)
	t.Logf("Content:        %q", resp.Content)
	t.Logf("Finish reason:  %s", resp.FinishReason)
	if len(resp.ToolCalls) > 0 {
		for i, tc := range resp.ToolCalls {
			t.Logf("ToolCall[%d]: id=%s name=%s args=%+v", i, tc.ID, tc.Name, tc.Arguments)
		}
	}

	// If finish_reason is tool_calls, we expect at least one tool call
	if resp.FinishReason == "tool_calls" && len(resp.ToolCalls) == 0 {
		t.Error("FinishReason is tool_calls but no ToolCalls parsed")
	}
}

// TestIntegration_HTTPProvider_Chat is the non-streaming counterpart for comparison.
func TestIntegration_HTTPProvider_Chat(t *testing.T) {
	apiBase, proxy, model, mc := loadDefaultModelConfig(t)

	p := createHTTPProviderFromConfig(mc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	messages := []Message{
		{Role: "user", Content: "Respond with only the word 'pong'. Nothing else."},
	}

	t.Logf("=== Chat (non-streaming) Request ===")
	t.Logf("API Base: %s", apiBase)
	t.Logf("Proxy:    %s", proxy)
	t.Logf("Model:    %s", model)

	resp, err := p.Chat(ctx, messages, nil, model, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	t.Logf("=== Chat (non-streaming) Response ===")
	t.Logf("Content:       %q", resp.Content)
	t.Logf("Finish reason: %s", resp.FinishReason)
	if resp.Usage != nil {
		t.Logf("Usage: prompt=%d, completion=%d, total=%d",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}

	if resp.Content == "" {
		t.Error("Content is empty")
	}
	if !strings.Contains(strings.ToLower(resp.Content), "pong") {
		t.Errorf("Content = %q, expected to contain 'pong'", resp.Content)
	}
}

// TestIntegration_HTTPProvider_ChatStream_ReasoningModel tests a model that outputs
// reasoning_content (e.g. DeepSeek R1) to verify reasoning delta handling.
// Uses a separate model entry named "deepseek-reasoner" if present in model_list,
// otherwise falls back to the default model.
func TestIntegration_HTTPProvider_ChatStream_ReasoningModel(t *testing.T) {
	apiBase, proxy, model, mc := loadDefaultModelConfig(t)

	// Allow overriding with a reasoning-specific model via environment variable
	if envModel := os.Getenv("HTTP_REASONING_MODEL"); envModel != "" {
		model = envModel
	}

	p := createHTTPProviderFromConfig(mc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	messages := []Message{
		{Role: "user", Content: "What is 9 * 9? Think step by step."},
	}

	t.Logf("=== ChatStream (reasoning model) Request ===")
	t.Logf("API Base: %s", apiBase)
	t.Logf("Proxy:    %s", proxy)
	t.Logf("Model:    %s", model)

	contentChunks := 0
	reasoningChunks := 0

	resp, err := p.ChatStream(ctx, messages, nil, model, nil,
		func(content, reasoning string) {
			if reasoning != "" {
				reasoningChunks++
				if reasoningChunks <= 5 || reasoningChunks%1 == 0 {
					t.Logf("[reasoning chunk %3d: %4d] %q", reasoningChunks, len(reasoning), reasoning)
				}
			}
			if content != "" {
				contentChunks++
				if contentChunks <= 5 || contentChunks%1 == 0 {
					t.Logf("[content chunk %3d: %4d]  %q", contentChunks, len(content), content)
				}
			}
		},
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}

	t.Logf("=== ChatStream (reasoning model) Response ===")
	t.Logf("Content chunks:  %d", contentChunks)
	t.Logf("Reasoning chunks: %d", reasoningChunks)
	t.Logf("Final content:   %q", resp.Content)
	t.Logf("Final reasoning: %q", resp.ReasoningContent)
	t.Logf("Finish reason:   %s", resp.FinishReason)
	if resp.Usage != nil {
		t.Logf("Usage: prompt=%d, completion=%d, total=%d",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}

	if resp.Content == "" {
		t.Error("Content is empty")
	}
}

// TestIntegration_HTTPProvider_ChatStream_LongOutput verifies that long streaming
// responses do not hang or truncate.
func TestIntegration_HTTPProvider_ChatStream_LongOutput(t *testing.T) {
	apiBase, proxy, model, mc := loadDefaultModelConfig(t)

	p := createHTTPProviderFromConfig(mc)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	messages := []Message{
		{Role: "user", Content: "List the numbers 1 through 20, each on a new line."},
	}

	t.Logf("=== ChatStream (long output) Request ===")
	t.Logf("API Base: %s", apiBase)
	t.Logf("Proxy:    %s", proxy)
	t.Logf("Model:    %s", model)

	chunkCount := 0
	start := time.Now()
	resp, err := p.ChatStream(ctx, messages, nil, model, nil,
		func(content, reasoning string) {
			chunkCount++
		},
	)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}

	t.Logf("=== ChatStream (long output) Response ===")
	t.Logf("Chunks:        %d", chunkCount)
	t.Logf("Elapsed:       %v", elapsed)
	t.Logf("Content length: %d", len(resp.Content))
	t.Logf("Content:\n%s", resp.Content)

	if chunkCount < 5 {
		t.Errorf("Only %d chunks received for a long response — possible truncation", chunkCount)
	}

	// Verify numbers 1-20 appear
	for i := 1; i <= 20; i++ {
		if !strings.Contains(resp.Content, fmt.Sprintf("%d", i)) {
			t.Errorf("Content missing number %d", i)
		}
	}
}
