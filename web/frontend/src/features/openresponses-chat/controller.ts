import { toast } from "sonner"

import { sendOpenResponsesMessage } from "@/api/openresponses"
import {
  generateSessionId,
  writeStoredSessionId,
} from "@/features/chat/state"
import i18n from "@/i18n"
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

    const assistantMsgId = `resp-${Date.now()}`
    let assistantContent = ""

    const fullText = await sendOpenResponsesMessage(
      {
        input,
        conversation_id: sessionId,
        stream: true,
      },
      (event) => {
        if (event.type === "delta" && event.delta) {
          // Delta contains the complete text (not incremental chunks).
          assistantContent = event.delta
          updateOpenResponsesChatStore((prev) => {
            const existing = prev.messages.find(
              (m) => m.id === assistantMsgId,
            )
            if (existing) {
              return {
                messages: prev.messages.map((m) =>
                  m.id === assistantMsgId
                    ? { ...m, content: assistantContent }
                    : m,
                ),
              }
            }
            return {
              messages: [
                ...prev.messages,
                {
                  id: assistantMsgId,
                  role: "assistant",
                  content: assistantContent,
                  timestamp: Date.now(),
                },
              ],
            }
          })
        }
      },
    )

    // Ensure final content is set
    updateOpenResponsesChatStore((prev) => {
      const existing = prev.messages.find((m) => m.id === assistantMsgId)
      const finalContent = fullText || assistantContent
      if (existing) {
        return {
          messages: prev.messages.map((m) =>
            m.id === assistantMsgId
              ? { ...m, content: finalContent }
              : m,
          ),
          isTyping: false,
          connectionState: "idle",
        }
      }
      return {
        messages: [
          ...prev.messages,
          {
            id: assistantMsgId,
            role: "assistant",
            content: finalContent,
            timestamp: Date.now(),
          },
        ],
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
