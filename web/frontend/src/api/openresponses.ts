import { launcherFetch } from "@/api/http"

// API client for OpenResponses Channel.

interface OpenResponsesTokenResponse {
  token: string
  endpoint_url: string
  enabled: boolean
}

interface OpenResponsesSetupResponse {
  token: string
  endpoint_url: string
  enabled: boolean
  changed: boolean
}

interface OpenResponsesChatRequest {
  input: string | Array<{ type: string; role: string; content: string }>
  conversation_id?: string
  stream?: boolean
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as {
        error?: string
        errors?: string[]
        status?: string
      }
      if (Array.isArray(body.errors) && body.errors.length > 0) {
        message = body.errors.join("; ")
      } else if (
        typeof body.error === "string" &&
        body.error.trim() !== ""
      ) {
        message = body.error
      }
    } catch {
      // Keep default fallback message if response body is not JSON.
    }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

export async function getOpenResponsesToken(): Promise<OpenResponsesTokenResponse> {
  return request<OpenResponsesTokenResponse>("/api/openresponses/token")
}

export async function setupOpenResponses(): Promise<OpenResponsesSetupResponse> {
  return request<OpenResponsesSetupResponse>("/api/openresponses/setup", {
    method: "POST",
  })
}

/**
 * Parse a single SSE event block (between double newlines).
 * Returns the event name and data payload.
 *
 * Supports:
 *   - Standard events:   event: xxx \n data: xxx
 *   - Terminal marker:   data: [DONE]
 */
function parseSSEEventBlock(block: string): { event: string; data: string } | null {
  const lines = block.split("\n").map((l) => l.replace(/\r$/, ""))
  let event = ""
  let data = ""

  for (const line of lines) {
    if (line.startsWith("event: ")) {
      event = line.slice(7).trim()
    } else if (line.startsWith("data: ")) {
      if (data) {
        data += "\n"
      }
      data += line.slice(6)
    }
    // Comment lines (": ...") are intentionally ignored.
  }

  // Terminal marker without an explicit event name.
  if (data.trim() === "[DONE]") {
    return { event: "done", data: "" }
  }

  if (!event || !data.trim()) {
    return null
  }

  return { event, data }
}

export async function sendOpenResponsesMessage(
  request: OpenResponsesChatRequest,
  onStreamEvent?: (event: { type: string; outputIndex?: number; delta?: string; itemType?: string; callId?: string; name?: string }) => void,
): Promise<string> {
  const res = await launcherFetch("/api/openresponses/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(request),
  })

  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: { message?: string }; message?: string }
      message =
        body.error?.message ?? body.message ?? message
    } catch {
      // Keep default fallback
    }
    throw new Error(message)
  }

  const contentType = res.headers.get("content-type") || ""

  // Non-streaming JSON response
  if (!contentType.includes("text/event-stream")) {
    const data = (await res.json()) as {
      output?: Array<{
        content?: Array<{ text?: string }>
      }>
    }
    const text = data.output?.[0]?.content?.[0]?.text ?? ""
    return text
  }

  // SSE streaming response — manual parse because EventSource cannot set custom headers
  const reader = res.body?.getReader()
  if (!reader) {
    throw new Error("No response body for streaming")
  }

  const decoder = new TextDecoder()
  let buffer = ""
  const outputTexts: string[] = []

  while (true) {
    const { done, value } = await reader.read()
    if (done) break

    buffer += decoder.decode(value, { stream: true })

    // Split by double newline (SSE event delimiter)
    const parts = buffer.split("\n\n")
    // Keep the last incomplete part in buffer
    buffer = parts.pop() ?? ""

    for (const block of parts) {
      const trimmed = block.replace(/\n$/, "").trim()
      if (!trimmed) continue

      const parsedBlock = parseSSEEventBlock(trimmed)
      if (!parsedBlock) continue

      if (parsedBlock.event === "response.output_item.added") {
        try {
          const parsedJSON = JSON.parse(parsedBlock.data) as { output_index?: number; item?: { type?: string; call_id?: string; name?: string } }
          if (typeof parsedJSON.output_index === "number") {
            onStreamEvent?.({
              type: "item_added",
              outputIndex: parsedJSON.output_index,
              itemType: parsedJSON.item?.type,
              callId: parsedJSON.item?.call_id,
              name: parsedJSON.item?.name,
            })
          }
        } catch (err) {
          console.warn("Failed to parse SSE output_item.added JSON:", parsedBlock.data, err)
        }
      } else if (
        parsedBlock.event === "response.output_text.delta" ||
        parsedBlock.event === "response.reasoning_text.delta"
      ) {
        try {
          const parsedJSON = JSON.parse(parsedBlock.data) as { output_index?: number; delta?: string }
          if (typeof parsedJSON.output_index === "number" && typeof parsedJSON.delta === "string") {
            // Incremental delta — append rather than replace.
            const existing = outputTexts[parsedJSON.output_index] ?? ""
            outputTexts[parsedJSON.output_index] = existing + parsedJSON.delta
            onStreamEvent?.({
              type: "delta",
              outputIndex: parsedJSON.output_index,
              delta: parsedJSON.delta,
              itemType: parsedBlock.event === "response.reasoning_text.delta" ? "reasoning" : undefined,
            })
          }
        } catch (err) {
          console.warn("Failed to parse SSE delta JSON:", parsedBlock.data, err)
        }
      } else if (parsedBlock.event === "response.function_call_arguments.delta") {
        try {
          const parsedJSON = JSON.parse(parsedBlock.data) as { output_index?: number; delta?: string }
          if (typeof parsedJSON.output_index === "number" && typeof parsedJSON.delta === "string") {
            onStreamEvent?.({
              type: "function_call_delta",
              outputIndex: parsedJSON.output_index,
              delta: parsedJSON.delta,
            })
          }
        } catch (err) {
          console.warn("Failed to parse SSE function_call_arguments.delta JSON:", parsedBlock.data, err)
        }
      } else if (parsedBlock.event === "response.function_call_arguments.done") {
        try {
          const parsedJSON = JSON.parse(parsedBlock.data) as { output_index?: number; part?: { arguments?: string } }
          if (typeof parsedJSON.output_index === "number") {
            onStreamEvent?.({
              type: "function_call_done",
              outputIndex: parsedJSON.output_index,
              delta: parsedJSON.part?.arguments,
            })
          }
        } catch (err) {
          console.warn("Failed to parse SSE function_call_arguments.done JSON:", parsedBlock.data, err)
        }
      } else if (parsedBlock.event === "response.content_part.done") {
        try {
          const parsedJSON = JSON.parse(parsedBlock.data) as {
            output_index?: number
            part?: { type?: string; image_url?: string }
          }
          if (
            typeof parsedJSON.output_index === "number" &&
            parsedJSON.part?.type === "output_image" &&
            parsedJSON.part.image_url
          ) {
            onStreamEvent?.({
              type: "image",
              outputIndex: parsedJSON.output_index,
              delta: parsedJSON.part.image_url,
            })
          }
        } catch (err) {
          console.warn("Failed to parse SSE content_part.done JSON:", parsedBlock.data, err)
        }
      } else if (parsedBlock.event === "done") {
        // Terminal marker — stream ends here.
      } else if (parsedBlock.event === "response.completed") {
        onStreamEvent?.({ type: "completed", delta: outputTexts.join("\n\n") })
      }
    }
  }

  return outputTexts.join("\n\n")
}

export type { OpenResponsesTokenResponse, OpenResponsesSetupResponse, OpenResponsesChatRequest }
