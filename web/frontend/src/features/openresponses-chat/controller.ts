import { toast } from "sonner"

import { streamOpenResponses, type SSEEvent } from "@/api/openresponses-chat"
import { loadSessionMessages } from "@/features/openresponses-chat/history"
import {
  generateNewOpenResponsesSessionId,
  getOpenResponsesChatState,
  setOpenResponsesSessionId,
  updateOpenResponsesChatStore,
} from "@/store/openresponses-chat"

let abortController: AbortController | null = null
let msgIdSeq = 0

function isStreaming(): boolean {
  return getOpenResponsesChatState().connectionState === "streaming"
}

export function cancelOpenResponsesStream() {
  if (abortController) {
    abortController.abort()
    abortController = null
  }
}

function appendUserMessage(content: string, attachments?: { type: string; url: string; filename?: string }[]) {
  const id = `or-user-${++msgIdSeq}`
  updateOpenResponsesChatStore((prev) => ({
    messages: [
      ...prev.messages,
      {
        id,
        role: "user",
        content,
        timestamp: Date.now(),
        attachments: attachments?.map((a) => ({
          type: a.type as "image" | "audio" | "video" | "file",
          url: a.url,
          filename: a.filename,
        })),
      },
    ],
    isTyping: true,
    connectionState: "streaming",
  }))
  return id
}

function appendToolCalls(toolCalls: { id?: string; type?: string; function?: { name?: string; arguments?: string } }[]) {
  const id = `or-tool-${++msgIdSeq}`
  updateOpenResponsesChatStore((prev) => ({
    messages: [
      ...prev.messages,
      {
        id,
        role: "assistant",
        content: "",
        timestamp: Date.now(),
        kind: "tool_calls",
        toolCalls: toolCalls.map((tc) => ({
          id: tc.id,
          type: tc.type || "function",
          function: tc.function,
        })),
      },
    ],
  }))
}

function updateAssistantContent(messageId: string, delta: string) {
  updateOpenResponsesChatStore((prev) => ({
    messages: prev.messages.map((msg) => {
      if (msg.id !== messageId || msg.role !== "assistant") return msg
      return { ...msg, content: msg.content + delta }
    }),
  }))
}

function finalizeAssistantMessage(messageId: string, finalContent: string) {
  updateOpenResponsesChatStore((prev) => ({
    messages: prev.messages.map((msg) => {
      if (msg.id !== messageId || msg.role !== "assistant") return msg
      return { ...msg, content: finalContent || msg.content }
    }),
    isTyping: false,
    connectionState: "completed",
  }))
}

export async function sendOpenResponsesMessage({
  content,
  attachments = [],
  model,
}: {
  content: string
  attachments?: { type: string; url: string; filename?: string }[]
  model?: string
}) {
  if (isStreaming()) {
    cancelOpenResponsesStream()
    // Wait briefly for abort to take effect
    await new Promise((r) => setTimeout(r, 100))
  }

  const sessionId = getOpenResponsesChatState().activeSessionId
  msgIdSeq = 0
  appendUserMessage(content, attachments)

  const imageAttachments = attachments
    .filter((a) => a.type === "image" && a.url)
    .map((a) => ({
      type: "image" as const,
      url: a.url,
      filename: a.filename,
    }))

  abortController = new AbortController()
  const pendingToolCalls: Map<string, { name: string; arguments: string }> = new Map()
  const textMessageRef = { id: null as string | null }
  const reasoningMessageRef = { id: null as string | null }

  try {
    for await (const event of streamOpenResponses(
      {
        input: content,
        conversation_id: sessionId,
        model,
        attachments: imageAttachments.length > 0 ? imageAttachments : undefined,
      },
      abortController.signal,
    )) {
      handleSSEEvent(event, pendingToolCalls, textMessageRef, reasoningMessageRef)
    }
  } catch (err) {
    if (err instanceof Error && err.name === "AbortError") {
      const state = getOpenResponsesChatState()
      const targetId = textMessageRef.id || reasoningMessageRef.id
      if (targetId) {
        finalizeAssistantMessage(targetId, state.messages.find((m) => m.id === targetId)?.content || "")
      }
      return
    }
    console.error("OpenResponses stream error:", err)
    toast.error(err instanceof Error ? err.message : "Stream error")
    updateOpenResponsesChatStore({
      isTyping: false,
      connectionState: "error",
    })
  } finally {
    abortController = null
    if (getOpenResponsesChatState().connectionState === "streaming") {
      updateOpenResponsesChatStore({
        isTyping: false,
        connectionState: "completed",
      })
    }
  }
}

function handleSSEEvent(
  event: SSEEvent,
  pendingToolCalls: Map<string, { name: string; arguments: string }>,
  textMessageRef: { id: string | null },
  reasoningMessageRef: { id: string | null },
) {
  switch (event.type) {
    case "text_delta":
    case "text": {
      if (!textMessageRef.id) {
        const id = `or-assistant-${++msgIdSeq}`
        updateOpenResponsesChatStore((prev) => ({
          messages: [
            ...prev.messages,
            {
              id,
              role: "assistant",
              content: event.content,
              timestamp: Date.now(),
              kind: "normal",
            },
          ],
        }))
        textMessageRef.id = id
      } else {
        updateAssistantContent(textMessageRef.id, event.content)
      }
      break
    }
    case "reasoning": {
      if (!reasoningMessageRef.id) {
        const id = `or-assistant-reasoning-${++msgIdSeq}`
        updateOpenResponsesChatStore((prev) => ({
          messages: [
            ...prev.messages,
            {
              id,
              role: "assistant",
              content: event.content,
              timestamp: Date.now(),
              kind: "thought",
            },
          ],
        }))
        reasoningMessageRef.id = id
      } else {
        updateAssistantContent(reasoningMessageRef.id, event.content)
      }
      break
    }
    case "function_call": {
      if (event.call_id) {
        const existing = pendingToolCalls.get(event.call_id)
        if (existing) {
          existing.arguments += event.arguments || ""
        } else {
          pendingToolCalls.set(event.call_id, {
            name: event.name,
            arguments: event.arguments || "",
          })
        }
      }
      break
    }
    case "turn_end": {
      // Flush any pending tool calls
      if (pendingToolCalls.size > 0) {
        const calls = Array.from(pendingToolCalls.entries()).map(([id, fn]) => ({
          id,
          type: "function",
          function: { name: fn.name, arguments: fn.arguments },
        }))
        appendToolCalls(calls)
        pendingToolCalls.clear()
      }
      // Update context usage if present
      if (event.total_tokens !== undefined || event.used_percent !== undefined) {
        updateOpenResponsesChatStore({
          contextUsage: {
            used_tokens: event.total_tokens || 0,
            total_tokens: event.total_tokens || 0,
            compress_at_tokens: 0,
            used_percent: event.used_percent || 0,
          },
        })
      }
      // Reset refs so the next turn starts fresh messages
      textMessageRef.id = null
      reasoningMessageRef.id = null
      break
    }
    case "image": {
      const id = `or-assistant-img-${++msgIdSeq}`
      updateOpenResponsesChatStore((prev) => ({
        messages: [
          ...prev.messages,
          {
            id,
            role: "assistant",
            content: "",
            timestamp: Date.now(),
            kind: "normal",
            attachments: [{ type: "image", url: event.image_url }],
          },
        ],
      }))
      break
    }
    case "error":
      toast.error(event.message)
      updateOpenResponsesChatStore({
        isTyping: false,
        connectionState: "error",
      })
      break
  }
}

export function newOpenResponsesSession() {
  cancelOpenResponsesStream()
  generateNewOpenResponsesSessionId()
}

export async function switchOpenResponsesSession(sessionId: string) {
  cancelOpenResponsesStream()
  try {
    const historyMessages = await loadSessionMessages(sessionId)
    setOpenResponsesSessionId(sessionId)
    updateOpenResponsesChatStore({
      messages: historyMessages,
      isTyping: false,
      connectionState: "completed",
    })
  } catch (error) {
    console.error("Failed to load session history:", error)
    toast.error("Failed to load session history")
    setOpenResponsesSessionId(sessionId)
  }
}

export function initializeOpenResponsesChatStore() {
  // No-op for now; store initializes with defaults
}
