import { toast } from "sonner"

import { sendOpenResponsesMessage } from "@/api/openresponses"
import { generateSessionId } from "@/features/chat/state"
import {
  type ChatAttachment,
  getOpenResponsesChatState,
  updateOpenResponsesChatStore,
} from "@/store/openresponses-chat"

let msgIdCounter = 0
let activeSessionIdRef = getOpenResponsesChatState().activeSessionId

function setActiveSessionId(sessionId: string) {
  activeSessionIdRef = sessionId
  updateOpenResponsesChatStore({ activeSessionId: sessionId })
}

interface SendChatMessageInput {
  content: string
  attachments?: ChatAttachment[]
}

function buildInputFromHistory(
  messages: Array<{ role: string; content: string }>,
): Array<{ type: string; role: string; content: string }> {
  const input: Array<{ type: string; role: string; content: string }> = []
  for (const msg of messages) {
    if (msg.role === "user" || msg.role === "assistant") {
      input.push({ type: "message", role: msg.role, content: msg.content })
    }
  }
  return input
}

export async function sendOpenResponsesChatMessage({
  content,
  attachments = [],
}: SendChatMessageInput): Promise<boolean> {
  if (getOpenResponsesChatState().connectionState === "sending") {
    return false
  }

  const normalizedContent = content.trim()
  const normalizedAttachments = attachments.filter(
    (a) => a.type === "image" && a.url,
  )

  if (!normalizedContent && normalizedAttachments.length === 0) {
    return false
  }

  const id = `msg-${++msgIdCounter}-${Date.now()}`
  const sessionId = activeSessionIdRef

  // Add user message immediately
  updateOpenResponsesChatStore((prev) => ({
    messages: [
      ...prev.messages,
      {
        id,
        role: "user",
        content: normalizedContent,
        attachments:
          normalizedAttachments.length > 0 ? normalizedAttachments : undefined,
        timestamp: Date.now(),
      },
    ],
    isTyping: true,
    connectionState: "sending",
  }))

  try {
    // Build conversation history for multi-turn support
    const history = getOpenResponsesChatState().messages
      .filter((m) => m.role === "user" || m.role === "assistant")
      .map((m) => ({ role: m.role, content: m.content }))

    const input = buildInputFromHistory(history)

    const assistantMessages = new Map<
      number,
      { id: string; content: string; kind?: "thought" }
    >()

    await sendOpenResponsesMessage(
      {
        input,
        conversation_id: sessionId,
        stream: true,
      },
      (event) => {
        if (event.type === "item_added" && typeof event.outputIndex === "number") {
          if (!assistantMessages.has(event.outputIndex)) {
            const msgId = `resp-${Date.now()}-${event.outputIndex}`
            const kind = event.itemType === "reasoning" ? "thought" : undefined
            assistantMessages.set(event.outputIndex, { id: msgId, content: "", kind })
            updateOpenResponsesChatStore((prev) => ({
              messages: [
                ...prev.messages,
                {
                  id: msgId,
                  role: "assistant",
                  content: "",
                  kind,
                  timestamp: Date.now(),
                },
              ],
            }))
          }
        } else if (event.type === "delta" && typeof event.outputIndex === "number" && event.delta) {
          // Delta contains the complete text (not incremental chunks).
          let msg = assistantMessages.get(event.outputIndex)
          if (!msg) {
            const msgId = `resp-${Date.now()}-${event.outputIndex}`
            const kind = event.itemType === "reasoning" ? "thought" : undefined
            msg = { id: msgId, content: event.delta, kind }
            assistantMessages.set(event.outputIndex, msg)
          } else {
            msg.content = event.delta
          }
          updateOpenResponsesChatStore((prev) => {
            const existing = prev.messages.find((m) => m.id === msg!.id)
            if (existing) {
              return {
                messages: prev.messages.map((m) =>
                  m.id === msg!.id ? { ...m, content: msg!.content } : m,
                ),
              }
            }
            return {
              messages: [
                ...prev.messages,
                {
                  id: msg!.id,
                  role: "assistant",
                  content: msg!.content,
                  kind: msg!.kind,
                  timestamp: Date.now(),
                },
              ],
            }
          })
        }
      },
    )

    // Ensure final content is set for all created messages
    updateOpenResponsesChatStore((prev) => {
      let messages = prev.messages
      for (const [, { id, content, kind }] of assistantMessages) {
        const existing = messages.find((m) => m.id === id)
        const finalContent = content
        if (existing) {
          messages = messages.map((m) =>
            m.id === id ? { ...m, content: finalContent } : m,
          )
        } else {
          messages = [
            ...messages,
            {
              id,
              role: "assistant",
              content: finalContent,
              kind,
              timestamp: Date.now(),
            },
          ]
        }
      }
      return {
        messages,
        isTyping: false,
        connectionState: "idle",
      }
    })

    return true
  } catch (error) {
    console.error("Failed to send OpenResponses message:", error)
    const message =
      error instanceof Error ? error.message : "Unknown error"
    toast.error(message)

    updateOpenResponsesChatStore((prev) => ({
      messages: prev.messages.filter((m) => m.id !== id),
      isTyping: false,
      connectionState: "error",
    }))
    return false
  }
}

export function newOpenResponsesChatSession() {
  if (getOpenResponsesChatState().messages.length === 0) {
    return
  }

  setActiveSessionId(generateSessionId())
  updateOpenResponsesChatStore({
    messages: [],
    isTyping: false,
    connectionState: "idle",
  })
}

export function initializeOpenResponsesChatStore() {
  activeSessionIdRef = getOpenResponsesChatState().activeSessionId
}

export function teardownOpenResponsesChatStore() {
  // No-op for now; no persistent connections to clean up
}
