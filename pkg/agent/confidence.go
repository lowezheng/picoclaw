package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// ConfidenceScore is the structured evaluation result.
type ConfidenceScore struct {
	OverallScore int              `json:"overallScore"`
	Rating       string           `json:"rating"`
	Stars        int              `json:"stars"`
	Dimensions   []ScoreDimension `json:"dimensions"`
	Sources      []DataSource     `json:"sources"`
}

// ScoreDimension describes one evaluation dimension.
type ScoreDimension struct {
	Name   string  `json:"name"`
	Score  int     `json:"score"`
	Weight float64 `json:"weight"`
	Reason string  `json:"reason"`
}

// DataSource describes one tool/data source used in the turn.
type DataSource struct {
	ToolName     string `json:"toolName"`
	KeyData      string `json:"keyData"`
	CitationType string `json:"citationType"` // direct / summary / none
}

// ConfidenceRequest is the input to the evaluator.
type ConfidenceRequest struct {
	UserMessage  string
	Messages     []providers.Message
	FinalContent string
	Iterations   int
	Duration     time.Duration
}

// ConfidenceEvaluator evaluates the quality of an AI turn.
type ConfidenceEvaluator struct {
	provider providers.LLMProvider
	model    string
}

// NewConfidenceEvaluator creates a new evaluator using the given provider.
func NewConfidenceEvaluator(provider providers.LLMProvider, model string) *ConfidenceEvaluator {
	return &ConfidenceEvaluator{
		provider: provider,
		model:    model,
	}
}

// Evaluate runs the confidence evaluation.
func (e *ConfidenceEvaluator) Evaluate(ctx context.Context, req *ConfidenceRequest) (*ConfidenceScore, error) {
	if e.provider == nil {
		return nil, fmt.Errorf("confidence evaluator: no provider available")
	}

	evalMessages := []providers.Message{
		{Role: "system", Content: confidenceEvalPrompt},
		{Role: "user", Content: buildEvaluationInput(req)},
	}

	resp, err := e.provider.Chat(ctx, evalMessages, nil, e.model, map[string]any{
		"max_tokens":  512,
		"temperature": 0.1,
	})
	if err != nil {
		return nil, fmt.Errorf("confidence evaluation LLM call failed: %w", err)
	}

	return parseConfidenceScore(resp.Content)
}

const confidenceEvalPrompt = `# 你是一个严格的AI输出质量评估专家

## 评估输入
- 用户原始问题
- 完整的对话历史（包含LLM输出和工具返回结果）
- 最终回答内容

## 评估维度（每个维度0-100分）

### 1. 事实准确性（权重30%）
- 评估最终回答中的事实声明是否与工具返回结果一致
- 检查是否有工具未覆盖但被断言的内容
- **关键：比较最终回答和每个工具返回的原始数据。如果AI的输出与工具结果有较大差异，请判断：**
  - a) 这是合理的综合、归纳或推理（不影响分数）
  - b) 这是对工具结果的错误解读或核心事实的偏离（降低分数）
- 90-100：无事实错误，工具结果被正确使用
- 70-89：有少量存疑内容，但不影响核心结论
- <70：有明显事实错误或与工具结果矛盾

### 2. 推理链完整性（权重25%）
- 评估是否充分覆盖了用户问题的所有方面
- 检查推理过程是否有跳跃（缺少中间步骤）
- 检查是否遗漏了用户追问或隐含需求
- 90-100：完整覆盖，推理清晰无跳跃
- 70-89：基本覆盖，有少量遗漏
- <70：遗漏关键方面或推理有严重跳跃

### 3. 多步一致性（权重20%）
- 评估多轮迭代中前后声明是否矛盾
- 检查工具结果是否被正确引用（未被曲解）
- 检查迭代过程中是否有自我矛盾
- 90-100：完全一致，工具结果被准确引用
- 70-89：少量不一致但不影响结论
- <70：有明显矛盾或工具结果被曲解

### 4. 不确定性透明度（权重15%）
- 评估LLM是否对推测性内容明确标注
- 检查是否诚实承认了不确定的部分
- 90-100：所有推测性内容都明确标注
- 70-89：部分标注
- <70：未标注推测内容，假装确信

### 5. 来源可追溯性（权重10%）
- 评估事实声明是否有来源标注
- 检查是否说明了信息来自哪个工具/文件
- 90-100：所有事实声明都有来源
- 70-89：大部分有来源
- <70：大部分无来源

## 数据来源清单

对于本次对话中使用的每个工具，输出：
- toolName: 工具名称
- keyData: 工具返回的关键数据摘要（50字以内）
- citationType: 引用方式
  - "direct" — 最终回答直接引用了工具返回的具体内容
  - "summary" — 最终回答概括了工具返回的内容
  - "none" — 最终回答完全没有提到这个工具的结果

## 输出格式（严格JSON，不要输出其他内容）

{
  "overallScore": <0-100整数>,
  "rating": "<1-5个⭐>",
  "stars": <1-5整数>,
  "dimensions": [
    {"name": "事实准确性", "score": 85, "weight": 0.30, "reason": "..."},
    {"name": "推理链完整性", "score": 90, "weight": 0.25, "reason": "..."},
    {"name": "多步一致性", "score": 75, "weight": 0.20, "reason": "..."},
    {"name": "不确定性透明度", "score": 80, "weight": 0.15, "reason": "..."},
    {"name": "来源可追溯性", "score": 70, "weight": 0.10, "reason": "..."}
  ],
  "sources": [
    {"toolName": "search_web", "keyData": "返回10条结果，3条来自官方文档", "citationType": "summary"},
    {"toolName": "read_file", "keyData": "文件 config.json 含数据库配置", "citationType": "none"}
  ]
}`

func buildEvaluationInput(req *ConfidenceRequest) string {
	var b strings.Builder
	b.WriteString("# 用户原始问题\n")
	b.WriteString(req.UserMessage)
	b.WriteString("\n\n# 完整对话过程\n")

	msgNum := 0
	for _, msg := range req.Messages {
		switch msg.Role {
		case "user":
			msgNum++
			b.WriteString(fmt.Sprintf("\n--- 用户输入 #%d ---\n%s\n", msgNum, truncateForEval(msg.Content, 2000)))
		case "assistant":
			msgNum++
			if len(msg.ToolCalls) > 0 {
				b.WriteString(fmt.Sprintf("\n--- AI工具调用 #%d ---\n", msgNum))
				for _, tc := range msg.ToolCalls {
					args := ""
					if tc.Function != nil {
						args = tc.Function.Arguments
					}
					b.WriteString(fmt.Sprintf("工具: %s, 参数: %s\n", tc.Name, truncateForEval(args, 500)))
				}
			} else {
				b.WriteString(fmt.Sprintf("\n--- AI回答 #%d ---\n%s\n", msgNum, truncateForEval(msg.Content, 2000)))
			}
		case "tool":
			msgNum++
			b.WriteString(fmt.Sprintf("\n--- 工具返回结果 #%d ---\n%s\n", msgNum, truncateForEval(msg.Content, 2000)))
		}
	}

	b.WriteString("\n\n# 最终回答\n")
	b.WriteString(truncateForEval(req.FinalContent, 3000))
	b.WriteString("\n\n请根据以上信息，按照系统提示中的维度进行评分。")

	return b.String()
}

func truncateForEval(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... [truncated]"
}

var codeBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*\\n(.*?)")

func parseConfidenceScore(content string) (*ConfidenceScore, error) {
	content = strings.TrimSpace(content)

	// Try direct parse first
	var score ConfidenceScore
	if err := json.Unmarshal([]byte(content), &score); err == nil {
		return &score, nil
	}

	// Try extracting from markdown code block
	matches := codeBlockRe.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		if len(m) > 1 {
			block := strings.TrimSpace(m[1])
			if idx := strings.LastIndex(block, "```"); idx > 0 {
				block = strings.TrimSpace(block[:idx])
			}
			if err := json.Unmarshal([]byte(block), &score); err == nil {
				return &score, nil
			}
		}
	}

	return nil, fmt.Errorf("failed to parse confidence score JSON from: %s", truncateForEval(content, 200))
}

// FormatConfidenceBlock formats a ConfidenceScore as a UI message block.
func FormatConfidenceBlock(score *ConfidenceScore) string {
	data := map[string]any{
		"messageType": "confidence",
		"summary": map[string]any{
			"overallScore": score.OverallScore,
			"rating":       score.Rating,
			"stars":        score.Stars,
		},
		"dimensions": score.Dimensions,
		"sources":    score.Sources,
	}
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal confidence block", map[string]any{"error": err.Error()})
		return ""
	}
	return fmt.Sprintf("\n\n---MESSAGE_START---\n%s\n---MESSAGE_END---", string(jsonBytes))
}
