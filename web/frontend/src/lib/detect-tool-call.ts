// Known tool names that produce structured output in the chat
// Used as a fallback for legacy plain-text messages that do not use
// the FormatToolFeedbackMessage shape.
export const KNOWN_TOOL_NAMES = ["exec", "web_search", "spawn", "mcp"]

/**
 * Detect if a message content looks like a tool call output.
 *
 * Supports two formats:
 *   1. Backend FormatToolFeedbackMessage: 🔧 `toolName`\n```\nargs\n```
 *      – output strips markdown code-block markers so it matches the
 *        plain-JSON rendering used by the WeCom channel.
 *   2. Legacy plain tool name at the start of the first line.
 */
export function detectToolCall(
  content: string,
): { toolName: string; args: string; output: string } | null {
  if (!content || content.length < 3) return null
  const trimmed = content.trim()

  // ── Format 1: 🔧 `toolName` (FormatToolFeedbackMessage) ──
  const toolMatch = trimmed.match(/^🔧\s*`([^`]+)`/)
  if (toolMatch) {
    const toolName = toolMatch[1]
    // send_file is a media-delivery marker, not a real tool call
    if (toolName === "send_file") return null
    let rest = trimmed.slice(toolMatch[0].length).trim()
    // Strip markdown code-block fences (with optional language tag)
    rest = rest.replace(/^```[a-zA-Z0-9]*\n?/, "").replace(/\n?```$/, "").trim()
    return {
      toolName,
      args: toolName,      // short header for ToolCallMessage
      output: rest || toolName,
    }
  }

  // ── Format 2: plain tool name at start of first line (legacy) ──
  const firstLine = trimmed.split("\n")[0]
  for (const name of KNOWN_TOOL_NAMES) {
    if (firstLine.toLowerCase().startsWith(name)) {
      const rest = trimmed.slice(firstLine.length).trim()
      return {
        toolName: name,
        args: firstLine,
        output: rest || firstLine,
      }
    }
  }

  return null
}
