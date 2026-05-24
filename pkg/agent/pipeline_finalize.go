// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/bus"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// Finalize handles turn finalization, either:
// - Early return when allResponsesHandled=true (ExecuteTools already finalized)
// - Normal finalization for allResponsesHandled=false (sets finalContent, saves session, compact)
func (p *Pipeline) Finalize(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	turnStatus TurnEndStatus,
	finalContent string,
) (turnResult, error) {
	al := p.al

	// When allResponsesHandled=true, ExecuteTools already finalized
	// (added handledToolResponseSummary, saved session, set phase to Completed).
	// But still check for hard abort - if requested, abort the turn.
	if exec.allResponsesHandled {
		if ts.hardAbortRequested() {
			cancelConfiguredStreamingLLM(turnCtx, exec)
			return al.abortTurn(ts)
		}
		finalizeStreamingIfNeeded(turnCtx, p, ts, exec, finalContent)
		ts.setPhase(TurnPhaseCompleted)
		return turnResult{
			finalContent: finalContent,
			modelName:    exec.llmModelName,
			status:       turnStatus,
			followUps:    append([]bus.InboundMessage(nil), ts.followUps...),
		}, nil
	}

	ts.setPhase(TurnPhaseFinalizing)
	ts.setFinalContent(finalContent)
	if !ts.opts.NoHistory {
		finalMsg := providers.Message{
			Role:             "assistant",
			Content:          finalContent,
			ModelName:        exec.llmModelName,
			ReasoningContent: responseReasoningContent(exec.response),
		}
		ts.agent.Sessions.AddFullMessage(ts.sessionKey, finalMsg)
		ts.recordPersistedMessage(finalMsg)
		ts.ingestMessage(turnCtx, al, finalMsg)
		if err := ts.agent.Sessions.Save(ts.sessionKey); err != nil {
			al.emitEvent(
				runtimeevents.KindAgentError,
				ts.eventMeta("runTurn", "turn.error"),
				ErrorPayload{
					Stage:   "session_save",
					Message: err.Error(),
				},
			)
			cancelConfiguredStreamingLLM(turnCtx, exec)
			return turnResult{status: TurnEndStatusError}, err
		}
	}

	if !ts.opts.NoHistory && ts.opts.EnableSummary {
		al.contextManager.Compact(
			turnCtx,
			&CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonSummarize,
				Budget:     ts.agent.ContextWindow,
			},
		)
	}

	contextUsage := computeContextUsage(ts.agent, ts.sessionKey)
	// Capture whether this turn ever used configured streaming before
	// finalizeConfiguredStreamingLLM clears the publisher.
	hadStreaming := exec.streamingPublisher != nil || exec.streamingFallback
	streamErr := finalizeConfiguredStreamingLLM(turnCtx, ts, exec, finalContent, contextUsage)
	// If streaming was never used, or it failed before visible output, or it
	// fell back to Chat, deliver the final answer via PublishOutbound so
	// channels like OpenResponses (which may not have streaming enabled in
	// config) still receive the response.
	if (!hadStreaming || (streamErr != nil && !isConfiguredStreamingVisibleError(streamErr)) || exec.streamingFallback) &&
		!ts.opts.SendResponse && ts.opts.AllowInterimPicoPublish && finalContent != "" {
		msg := outboundMessageForTurnWithOptions(ts, finalContent, outboundTurnMessageOptions{
			modelName: exec.llmModelName,
		})
		msg.ContextUsage = contextUsage
		markFinalOutbound(&msg)
		_ = al.bus.PublishOutbound(turnCtx, msg)
	}
	if streamErr != nil && isConfiguredStreamingVisibleError(streamErr) {
		ts.setPhase(TurnPhaseCompleted)
		return turnResult{
			finalContent: finalContent,
			status:       TurnEndStatusError,
			followUps:    append([]bus.InboundMessage(nil), ts.followUps...),
		}, streamErr
	}
	ts.setPhase(TurnPhaseCompleted)
	return turnResult{
		finalContent: finalContent,
		modelName:    exec.llmModelName,
		status:       turnStatus,
		followUps:    append([]bus.InboundMessage(nil), ts.followUps...),
	}, nil
}
