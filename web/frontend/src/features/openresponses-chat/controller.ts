import { toast } from "sonner"

import { sendOpenResponsesMessage, type ContentPart } from "@/api/openresponses"
import { loadSessionMessages } from "@/features/chat/history"
import i18n from "@/i18n"
import { generateSessionId } from "@/features/chat/state"
import {
  type ChatAttachment,
  getOpenResponsesChatState,
  updateOpenResponsesChatStore,
} from "@/store/openresponses-chat"

let msgIdCounter = 0
let activeSessionIdRef = getOpenResponsesChatState().activeSessionId
let pendingRequestCount = 0

function setActiveSessionId(sessionId: string) {
  activeSessionIdRef = sessionId
  updateOpenResponsesChatStore({ activeSessionId: sessionId })
}

function adjustPendingCount(delta: number): number {
  pendingRequestCount += delta
  return pendingRequestCount
}

function formatToolCallContent(name: string, args: string): string {
  return `🔧 \`${name}\`\n\`\`\`\n${args}\n\`\`\``
}

interface SendChatMessageInput {
  content: string
  attachments?: ChatAttachment[]
}

export async function sendOpenResponsesChatMessage({
  content,
  attachments = [],
}: SendChatMessageInput): Promise<boolean> {
  const normalizedContent = content.trim()
  const normalizedAttachments = attachments.filter(
    (a) => a.url,
  )

  if (!normalizedContent && normalizedAttachments.length === 0) {
    return false
  }

  const id = `msg-${++msgIdCounter}-${Date.now()}`
  const sessionId = activeSessionIdRef

  adjustPendingCount(1)

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
    // Only send the current user input; the backend manages conversation
    // history via conversation_id.
    const hasAttachments = normalizedAttachments.length > 0

    let requestBody: { input?: string; content?: ContentPart[]; conversation_id: string; stream: boolean }
    if (hasAttachments) {
      const contentParts: ContentPart[] = []
      if (normalizedContent) {
        contentParts.push({ type: "input_text", content: normalizedContent })
      }
      for (const a of normalizedAttachments) {
        contentParts.push({ type: "input_image", content: a.url })
      }
      requestBody = { content: contentParts, conversation_id: sessionId, stream: true }
    } else {
      requestBody = { input: normalizedContent, conversation_id: sessionId, stream: true }
    }

    const assistantMessages = new Map<
      number,
      { id: string; content: string; kind?: "thought"; callId?: string; name?: string; toolArgs?: string }
    >()
    const assistantImages = new Map<number, string[]>()

    await sendOpenResponsesMessage(requestBody,
      (event) => {
        if (event.type === "item_added" && typeof event.outputIndex === "number") {
          if (event.itemType === "function_call") {
            const msgId = `resp-${Date.now()}-${event.outputIndex}`
            assistantMessages.set(event.outputIndex, {
              id: msgId,
              content: "",
              callId: event.callId,
              name: event.name,
              toolArgs: "",
            })
            updateOpenResponsesChatStore((prev) => ({
              messages: [
                ...prev.messages,
                {
                  id: msgId,
                  role: "assistant",
                  content: "",
                  timestamp: Date.now(),
                  toolCall: { callId: event.callId!, name: event.name!, arguments: "" },
                },
              ],
            }))
            return
          }
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
          // Incremental delta — append rather than replace.
          let msg = assistantMessages.get(event.outputIndex)
          if (!msg) {
            const msgId = `resp-${Date.now()}-${event.outputIndex}`
            const kind = event.itemType === "reasoning" ? "thought" : undefined
            msg = { id: msgId, content: "", kind }
            assistantMessages.set(event.outputIndex, msg)
          }
          msg.content += event.delta
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
        } else if (event.type === "function_call_delta" && typeof event.outputIndex === "number" && event.delta) {
          const msg = assistantMessages.get(event.outputIndex)
          if (msg) {
            msg.toolArgs = (msg.toolArgs || "") + event.delta
            msg.content = formatToolCallContent(msg.name || "", msg.toolArgs)
            updateOpenResponsesChatStore((prev) => ({
              messages: prev.messages.map((m) =>
                m.id === msg.id && m.toolCall
                  ? { ...m, content: msg.content, toolCall: { ...m.toolCall, arguments: msg.toolArgs! } }
                  : m,
              ),
            }))
          }
        } else if (event.type === "function_call_done" && typeof event.outputIndex === "number") {
          const msg = assistantMessages.get(event.outputIndex)
          if (msg && event.delta) {
            msg.toolArgs = event.delta
            msg.content = formatToolCallContent(msg.name || "", msg.toolArgs)
            updateOpenResponsesChatStore((prev) => ({
              messages: prev.messages.map((m) =>
                m.id === msg.id && m.toolCall
                  ? { ...m, content: msg.content, toolCall: { ...m.toolCall, arguments: event.delta! } }
                  : m,
              ),
            }))
          }
        } else if (event.type === "image" && typeof event.outputIndex === "number" && event.delta) {
          const images = assistantImages.get(event.outputIndex) ?? []
          images.push(event.delta)
          assistantImages.set(event.outputIndex, images)
          const msg = assistantMessages.get(event.outputIndex)
          if (msg) {
            updateOpenResponsesChatStore((prev) => ({
              messages: prev.messages.map((m) =>
                m.id === msg.id
                  ? {
                      ...m,
                      attachments: images.map((url) => ({ type: "image" as const, url })),
                    }
                  : m,
              ),
            }))
          }
        }
      },
    )

    // Ensure final content is set for all created messages
    updateOpenResponsesChatStore((prev) => {
      let messages = prev.messages
      for (const [outputIndex, msgData] of assistantMessages) {
        const { id, content, kind, callId, name, toolArgs } = msgData
        const existing = messages.find((m) => m.id === id)
        const finalContent = content
        const images = assistantImages.get(outputIndex) ?? []
        const attachments =
          images.length > 0
            ? images.map((url) => ({ type: "image" as const, url }))
            : undefined
        const toolCall = callId && name
          ? { callId, name, arguments: toolArgs || "" }
          : undefined
        if (existing) {
          messages = messages.map((m) =>
            m.id === id
              ? { ...m, content: finalContent, attachments, ...(toolCall && { toolCall }) }
              : m,
          )
        } else {
          messages = [
            ...messages,
            {
              id,
              role: "assistant",
              content: finalContent,
              kind,
              attachments,
              ...(toolCall && { toolCall }),
              timestamp: Date.now(),
            },
          ]
        }
      }
      const remaining = adjustPendingCount(-1)
      return {
        messages,
        isTyping: remaining > 0,
        connectionState: remaining > 0 ? "sending" : "idle",
      }
    })

    return true
  } catch (error) {
    console.error("Failed to send OpenResponses message:", error)
    const message =
      error instanceof Error ? error.message : "Unknown error"
    toast.error(message)

    const remaining = adjustPendingCount(-1)
    updateOpenResponsesChatStore((prev) => ({
      messages: prev.messages.filter((m) => m.id !== id),
      isTyping: remaining > 0,
      connectionState: remaining > 0 ? "sending" : "error",
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

export async function switchOpenResponsesChatSession(sessionId: string) {
  if (sessionId === activeSessionIdRef) {
    return
  }

  try {
    const historyMessages = await loadSessionMessages(sessionId)

    setActiveSessionId(sessionId)
    updateOpenResponsesChatStore({
      messages: historyMessages,
      isTyping: false,
      connectionState: "idle",
    })
  } catch (error) {
    console.error("Failed to load session history:", error)
    toast.error(i18n.t("chat.historyOpenFailed"))
  }
}
