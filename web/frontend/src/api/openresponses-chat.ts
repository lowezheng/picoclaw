import { launcherFetch } from "@/api/http"

export interface CreateResponseRequest {
  input: string | { role: string; content: string }[]
  model?: string
  conversation_id?: string
  attachments?: {
    type?: "image" | "audio" | "video" | "file"
    url: string
    filename?: string
    content_type?: string
  }[]
  stream?: boolean
}

export type SSEEvent =
  | { type: "text_delta"; content: string }
  | { type: "text"; content: string }
  | { type: "reasoning"; content: string }
  | { type: "function_call"; call_id: string; name: string; arguments: string }
  | { type: "image"; image_url: string; caption?: string }
  | { type: "turn_end" }
  | { type: "error"; message: string }

function parseSSEEvent(line: string): SSEEvent | null {
  const trimmed = line.trim()
  if (!trimmed.startsWith("data: ")) {
    return null
  }
  const jsonStr = trimmed.slice(6).trim()
  if (jsonStr === "[DONE]") {
    return { type: "turn_end" }
  }
  try {
    const data = JSON.parse(jsonStr)
    const eventType = data.type || data.object || ""

    if (eventType === "response.output_text.delta" || eventType === "response.text.delta") {
      return { type: "text_delta", content: String(data.delta || data.text || "") }
    }
    if (eventType === "response.output_item.added") {
      const item = data.item || {}
      if (item.type === "function_call" || item.type === "tool_call") {
        return {
          type: "function_call",
          // function_call_arguments.delta sends item_id, not call_id;
          // use item.id so the delta can match the same pending entry
          call_id: String(item.id || item.call_id || ""),
          name: String(item.name || ""),
          arguments: String(item.arguments || ""),
        }
      }
      // reasoning / message item start signals a turn boundary;
      // emit turn_end so the controller resets refs and isolates turns
      if (item.type === "reasoning" || item.type === "message") {
        return { type: "turn_end" }
      }
    }
    if (eventType === "response.function_call_arguments.delta") {
      return {
        type: "function_call",
        // OpenAI sends item_id to identify the output item; fall back to call_id
        call_id: String(data.item_id || data.call_id || ""),
        name: String(data.name || ""),
        arguments: String(data.arguments || data.delta || ""),
      }
    }
    if (eventType === "response.completed" || eventType === "response.done") {
      return { type: "turn_end" }
    }
    if (eventType === "response.content_part.done" || eventType === "response.content_part.added") {
      const part = data.part || {}
      if (part.type === "output_image" && part.url) {
        return { type: "image", image_url: String(part.url), caption: String(part.caption || "") }
      }
    }
    if (eventType === "response.reasoning_text.delta" || eventType === "response.reasoning.delta" || eventType === "response.reasoning") {
      return { type: "reasoning", content: String(data.delta || data.content || "") }
    }
    if (eventType === "error") {
      return { type: "error", message: String(data.message || "Unknown error") }
    }
    // Fallback: if the backend sends plain content fields
    if (typeof data.content === "string" && data.content) {
      return { type: "text_delta", content: data.content }
    }
    if (typeof data.delta === "string" && data.delta) {
      return { type: "text_delta", content: data.delta }
    }
  } catch {
    // Not JSON, ignore
  }
  return null
}

export async function* streamOpenResponses(
  req: CreateResponseRequest,
  abortSignal?: AbortSignal,
): AsyncGenerator<SSEEvent, void, unknown> {
  const res = await launcherFetch("/v1/responses", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
    },
    body: JSON.stringify({ ...req, stream: true }),
    signal: abortSignal,
  })

  if (!res.ok) {
    let message = `HTTP ${res.status}`
    try {
      const err = await res.json()
      message = err.error?.message || err.message || message
    } catch {
      // ignore
    }
    yield { type: "error", message }
    return
  }

  if (!res.body) {
    yield { type: "error", message: "Empty response body" }
    return
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ""

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split("\n")
      buffer = lines.pop() || ""
      for (const line of lines) {
        const event = parseSSEEvent(line)
        if (event) yield event
      }
    }
    if (buffer.trim()) {
      const event = parseSSEEvent(buffer)
      if (event) yield event
    }
  } finally {
    reader.releaseLock()
  }
}

export async function sendOpenResponsesNonStreaming(
  req: CreateResponseRequest,
): Promise<{ content: string; toolCalls?: { id: string; name: string; arguments: string }[] }> {
  const res = await launcherFetch("/v1/responses", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ ...req, stream: false }),
  })

  if (!res.ok) {
    let message = `HTTP ${res.status}`
    try {
      const err = await res.json()
      message = err.error?.message || err.message || message
    } catch {
      // ignore
    }
    throw new Error(message)
  }

  const data = await res.json()
  const content = String(data.output?.[0]?.content?.value || data.output?.[0]?.text || data.content || "")
  const toolCalls = (data.output || [])
    .filter((item: Record<string, unknown>) => item.type === "function_call" || item.type === "tool_call")
    .map((item: Record<string, unknown>) => ({
      id: String(item.call_id || item.id || ""),
      name: String(item.name || ""),
      arguments: String(item.arguments || ""),
    }))

  return { content, toolCalls }
}
