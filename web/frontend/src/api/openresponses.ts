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
  }

  if (!event || !data.trim()) {
    return null
  }

  return { event, data }
}

export async function sendOpenResponsesMessage(
  request: OpenResponsesChatRequest,
  onStreamEvent?: (event: { type: string; delta?: string }) => void,
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
  let fullText = ""

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

      if (parsedBlock.event === "response.output_text.delta") {
        try {
          const parsedJSON = JSON.parse(parsedBlock.data) as { delta?: string }
          if (typeof parsedJSON.delta === "string") {
            // NOTE: OpenResponses channel sends the complete text in each
            // delta (not incremental chunks), so replace rather than append.
            fullText = parsedJSON.delta
            onStreamEvent?.({ type: "delta", delta: parsedJSON.delta })
          }
        } catch (err) {
          console.warn("Failed to parse SSE delta JSON:", parsedBlock.data, err)
        }
      } else if (parsedBlock.event === "response.completed") {
        onStreamEvent?.({ type: "completed", delta: fullText })
      }
    }
  }

  return fullText
}

export type { OpenResponsesTokenResponse, OpenResponsesSetupResponse, OpenResponsesChatRequest }
