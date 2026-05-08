# Membench 架构文档

Membench 是 PicoClaw 的记忆基准测试工具，用于评估不同记忆后端在长对话场景下的检索质量。它基于 LOCOMO 长上下文问答数据集进行评测。

## 核心职责

Membench 负责三件事：

1. **数据导入（Ingest）**：将 LOCOMO 对话样本导入到一个或多个记忆后端
2. **上下文检索（Retrieve）**：对每个问题，使用后端自身的搜索能力或原始历史截断来获取上下文
3. **质量评分（Score）**：将检索到的上下文与标准答案和证据对比，产出每样本和聚合指标

## 当前代码结构

所有代码目前都在 `cmd/membench/` 目录下，作为一个 `main` 包：

| 文件 | 职责 |
|------|------|
| `main.go` | Cobra 命令行入口、参数解析、各模块串联 |
| `locomo.go` | LOCOMO 数据集的数据模型、加载、会话展平 |
| `legacy_store.go` | 旧版 `SessionManager` 的包装和导入 |
| `ingest.go` | 向 Seahorse 引擎导入数据 |
| `metrics.go` | 关键词提取、Token-F1 计算、证据命中率、预算截断 |
| `eval.go` | Token 模式评测逻辑、结果聚合、JSON/控制台输出 |
| `eval_llm.go` | LLM 模式评测逻辑（生成答案 + LLM 评分） |
| `llm_client.go` | OpenAI 兼容的 Chat Completion 客户端（含重试） |

## 数据集模型

LOCOMO 样本是多轮对话，通常由两个人在多场会话中进行：

```go
type LocomoSample struct {
    SampleID     string                     // 样本唯一 ID
    Conversation map[string]json.RawMessage // session_1, session_2, ...
    QA           []LocomoQA                 // 配套的问答对
}

type LocomoQA struct {
    Question          string          // 问题
    Answer            json.RawMessage // 标准答案（可能是字符串或整数）
    AdversarialAnswer string          // 对抗性答案（仅 category 5）
    Evidence          []string        // 证据引用（dia_id 格式）
    Category          int             // 1=单跳, 2=多跳, 3=开放, 5=对抗
}
```

`GetTurns()` 将所有会话按时间顺序展平为对话轮次列表。`GetTurnByDiaID()` 通过证据引用找回原文，用于计算命中率。

## 数据流

```text
LOCOMO JSON 文件
  -> LoadDataset()              [数据加载]
  -> Ingest() 按后端导入        [数据导入]
       legacy:  SessionManager.AddMessage()
       seahorse: Engine.Ingest() + FTS5 索引构建
  -> 对每个 QA：
       BuildContext()           [上下文构建]
         legacy:  BudgetTruncate(原始历史)
         seahorse: 关键词搜索 -> BM25 排序合并 -> 消息展开 -> 预算截断
       Score()                  [评分]
         token 模式: TokenOverlapF1(检索到的上下文, 标准答案)
         llm 模式:   generateAnswer() -> judgeAnswer()
          always:   RecallHitRate(证据, 检索到的上下文)
  -> Aggregate()                [结果聚合]
  -> SaveResults() / PrintComparison()  [输出]
```

## 上下文检索流程

### Seahorse 检索（逐问题）

1. `ExtractKeywords(question)`：去停用词，最多保留 6 个关键词
2. 每个关键词独立搜索：`SearchMessages(pattern=关键词, conversationID, limit=20)`
3. 合并结果，对每个 `message_id` 保留最好的 BM25 分数（越负越好）
4. 按 BM25 分数升序排列（最好的在前）
5. `ExpandMessages(messageIDs)` 获取完整消息内容
6. `BudgetTruncate(contentParts, budgetTokens)` 从前往后保留，直到 token 预算耗尽

### Legacy 检索

1. 获取该样本会话的全部消息
2. `BudgetTruncate(messages, budgetTokens)` 从前往后（按时间顺序）保留，直到预算耗尽

## 评分指标

### Token Overlap F1

检索到的上下文与标准答案之间的 token 级 F1 分数。两者都转小写并按空格分词。

**局限**：对于多跳（category 2）和开放性问题（category 3），标准答案的措辞可能与原文不同，导致 F1 被低估。因此增加了 LLM-as-Judge 模式。

### 证据命中率（Recall Hit Rate）

证据中引用的 `dia_id` 对应的原文，有多少出现在检索到的上下文中。无法解析的 `dia_id` 不计入分母。

### 聚合方式

按有效问题数进行**加权平均**：

```
总 F1 = sum(样本 F1 × 样本有效问题数) / sum(样本有效问题数)
```

这修复了早期按样本数简单平均的 bug，避免问题数少的样本对结果产生不当影响。

## LLM 客户端

`LLMClient` 封装了 OpenAI 兼容的 `/chat/completions` 接口：

- 可配置超时和重试（指数退避：1s, 2s, 4s...）
- 网络错误、5xx、429 会重试；其他 4xx 直接失败
- `NoThinking` 标志支持三种 provider 的禁用思考模式：
  - llama.cpp: `chat_template_kwargs.enable_thinking = false`
  - Ollama: `think = false`
  - GLM（智谱）: `thinking.type = "disabled"`
  - 同时在 system prompt 前加 `/no_think` 作为兜底
- 自动清理 `<think>...</think>` 标签
- 主内容为空时回退到 `reasoning_content`

## 评测并发

LLM 评测支持并发（`--concurrency`）。每个 QA 项由一个 goroutine 处理，通过信号量限制并发数。goroutine 直接写入预分配的 `qaResults[qi]` 位置，无需额外同步。

Token 评测是 CPU 密集型，按样本串行执行。

## 输出格式

- **每样本 JSON**：`eval_{模式}_{样本ID}.json`，包含完整 QA 结果数组
- **聚合 JSON**：`results.json`，包含每种模式的聚合指标
- **控制台表格**：模式 ×（命中率、F1、各类别命中率）

## 当前问题

1. **所有代码在一个包里**：10 个文件都在 `main` 包中，无法复用，测试也受限于包内私有
2. **评测逻辑大量重复**：`EvalLegacy`/`EvalSeahorse`/`EvalLegacyLLM`/`EvalSeahorseLLM` 四段几乎相同的循环
3. **Ingest 和 Eval 强耦合**：`eval` 命令每次都要重新做 ingest，即使之前已经导过数据
4. **关注点混杂**：数据加载、存储、检索、打分、报告全部混在一起

## 重构方案

### 目标结构

```
cmd/membench/
├── main.go                        # CLI 入口、参数解析、模块串联
├── internal/
│   ├── dataset/
│   │   ├── model.go               # LocomoSample, LocomoQA, LocomoTurn
│   │   └── loader.go              # LoadDataset, GetTurns, GetTurnByDiaID
│   ├── backend/
│   │   ├── backend.go             # Backend 接口定义
│   │   ├── legacy.go              # LegacyBackend 实现
│   │   └── seahorse.go            # SeahorseBackend 实现
│   ├── retriever/
│   │   ├── keywords.go            # ExtractKeywords, SplitEvidenceIDs, NormalizeDiaID
│   │   └── truncate.go            # BudgetTruncate, StringListToContent
│   ├── scorer/
│   │   ├── scorer.go              # Scorer 接口定义
│   │   ├── token.go               # TokenOverlapF1
│   │   └── llm.go                 # LLM 生成答案 + LLM 评分
│   ├── llm/
│   │   └── client.go              # LLMClient, 请求/响应类型
│   ├── reporter/
│   │   ├── model.go               # EvalResult, QAResult, AggMetrics, CatMetrics
│   │   ├── aggregate.go           # aggregateMetrics, computeModeAgg
│   │   ├── json.go                # SaveResults, SaveAggregated
│   │   └── console.go             # PrintComparison, printSection
│   └── bench/
│       └── engine.go              # RunEvaluation(样本集, 后端列表, 评分器)
```

### 核心抽象

#### Backend 接口

```go
type Backend interface {
    Name() string
    Ingest(ctx context.Context, samples []dataset.LocomoSample) error
    BuildContext(ctx context.Context, sampleID string, question string, budgetTokens int) (string, error)
    Close() error
}
```

两个实现：
- `LegacyBackend`：包装 `session.SessionManager`，返回预算截断后的原始历史
- `SeahorseBackend`：包装 `seahorse.Engine`，执行关键词搜索 + BM25 排序合并 + 消息展开 + 预算截断

#### Scorer 接口

```go
type Scorer interface {
    Score(ctx context.Context, question, goldAnswer, contextText string) (ScoreResult, error)
}

type ScoreResult struct {
    TokenF1 float64  // 0-1，失败时为 -1
}
```

两个实现：
- `TokenScorer`：`TokenOverlapF1(上下文, 标准答案)`
- `LLMScorer`：先生成答案，再用 LLM 评分，返回归一化 0-1 分数

`RecallHitRate` 与评分器无关，它直接从证据列表和检索到的上下文计算，应放在 retriever 或 metrics 包中。

### 统一评测循环

重构后，四段重复的评测逻辑合并为一个 `RunEvaluation`：

```go
func RunEvaluation(
    ctx context.Context,
    samples []dataset.LocomoSample,
    backends []backend.Backend,
    scorer scorer.Scorer,
    budgetTokens int,
    concurrency int,
) ([]reporter.EvalResult, error)
```

外层遍历 backends，内层遍历 samples 和 QA，调用 `backend.BuildContext()` 和 `scorer.Score()`。并发控制通过信号量实现。

### 重构收益

1. **消除重复**：四段评测循环合并为一段，后端和评分器可任意组合
2. **提升可测试性**：每个包接口清晰，可用 mock 替代真实后端或 LLM 进行单元测试
3. **易于扩展**：
   - 新增后端（如向量数据库）只需实现 `Backend` 接口
   - 新增评分方式只需实现 `Scorer` 接口
4. **关注点分离**：数据、存储、检索、打分、报告各层独立

## 相关文件

- `cmd/membench/main.go`
- `cmd/membench/locomo.go`
- `cmd/membench/legacy_store.go`
- `cmd/membench/ingest.go`
- `cmd/membench/metrics.go`
- `cmd/membench/eval.go`
- `cmd/membench/eval_llm.go`
- `cmd/membench/llm_client.go`
- `pkg/seahorse/` — Seahorse 引擎和检索 API
- `pkg/session/` — 旧版 SessionManager
