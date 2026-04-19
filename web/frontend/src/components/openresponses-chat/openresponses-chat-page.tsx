import { IconPlus } from "@tabler/icons-react"
import { SessionHistoryMenu } from "@/components/chat/session-history-menu"
import { useSessionHistory } from "@/hooks/use-session-history"
import { type ChangeEvent, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { AssistantMessage } from "@/components/chat/assistant-message"
import { ToolCallMessage } from "@/components/chat/tool-call-message"
import {
  ChatComposer,
  type ChatInputDisabledReason,
} from "@/components/chat/chat-composer"
import { ChatEmptyState } from "@/components/chat/chat-empty-state"
import { TypingIndicator } from "@/components/chat/typing-indicator"
import { UserMessage } from "@/components/chat/user-message"
import { useOpenResponsesChat } from "@/components/openresponses-chat/use-openresponses-chat"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { useGateway } from "@/hooks/use-gateway"
import type { ChatAttachment } from "@/store/openresponses-chat"

const MAX_IMAGE_SIZE_BYTES = 7 * 1024 * 1024
const MAX_IMAGE_SIZE_LABEL = "7 MB"
const ALLOWED_IMAGE_TYPES = new Set([
  "image/jpeg",
  "image/png",
  "image/gif",
  "image/webp",
  "image/bmp",
])

function readFileAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (typeof reader.result === "string") {
        resolve(reader.result)
        return
      }
      reject(new Error("Failed to read file"))
    }
    reader.onerror = () =>
      reject(reader.error || new Error("Failed to read file"))
    reader.readAsDataURL(file)
  })
}

function resolveChatInputDisabledReason({
  gatewayState,
  connectionState,
}: {
  gatewayState: string
  connectionState: string
}): ChatInputDisabledReason | null {
  if (gatewayState === "unknown") {
    return "gatewayUnknown"
  }

  if (gatewayState === "starting") {
    return "gatewayStarting"
  }

  if (gatewayState === "restarting") {
    return "gatewayRestarting"
  }

  if (gatewayState === "stopping") {
    return "gatewayStopping"
  }

  if (gatewayState === "stopped") {
    return "gatewayStopped"
  }

  if (gatewayState === "error") {
    return "gatewayError"
  }

  if (connectionState === "error") {
    return "websocketError"
  }

  return null
}

// Known tool names that produce structured output in the chat
const KNOWN_TOOL_NAMES = ["exec", "web_search", "spawn", "mcp"]

/**
 * Detect if a message content looks like a tool call output.
 * Since the backend sends tool results as plain text without metadata,
 * we use heuristics: content that starts with a known tool name
 * followed by structured output patterns.
 */
function detectToolCall(content: string): { toolName: string; args: string; output: string } | null {
  if (!content || content.length < 3) return null
  const trimmed = content.trim()
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

export function OpenResponsesChatPage() {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [isAtBottom, setIsAtBottom] = useState(true)
  const [hasScrolled, setHasScrolled] = useState(false)
  const [input, setInput] = useState("")
  const [attachments, setAttachments] = useState<ChatAttachment[]>([])

  const {
    messages,
    connectionState,
    isTyping,
    activeSessionId,
    sendMessage,
    switchSession,
    newChat,
  } = useOpenResponsesChat()

  const { state: gwState } = useGateway()
  const isGatewayRunning = gwState === "running"

  const {
    sessions,
    hasMore,
    loadError,
    loadErrorMessage,
    observerRef,
    loadSessions,
    handleDeleteSession,
  } = useSessionHistory({
    activeSessionId,
    onDeletedActiveSession: newChat,
  })

  const inputDisabledReason = resolveChatInputDisabledReason({
    gatewayState: gwState,
    connectionState,
  })
  const canInput = inputDisabledReason === null

  const syncScrollState = (element: HTMLDivElement) => {
    const { scrollTop, scrollHeight, clientHeight } = element
    setHasScrolled(scrollTop > 0)
    setIsAtBottom(scrollHeight - scrollTop <= clientHeight + 10)
  }

  const handleScroll = (e: React.UIEvent<HTMLDivElement>) => {
    syncScrollState(e.currentTarget)
  }

  useEffect(() => {
    if (scrollRef.current) {
      if (isAtBottom) {
        scrollRef.current.scrollTop = scrollRef.current.scrollHeight
      }
      syncScrollState(scrollRef.current)
    }
  }, [messages, isTyping, isAtBottom])

  const handleSend = async () => {
    if (
      (!input.trim() && attachments.length === 0) ||
      !canInput
    ) {
      return
    }
    const content = input
    const currentAttachments = attachments
    setInput("")
    setAttachments([])
    const success = await sendMessage({
      content,
      attachments: currentAttachments,
    })
    if (!success) {
      setInput(content)
      setAttachments(currentAttachments)
    }
  }

  const handleAddImages = () => {
    if (!canInput) return
    fileInputRef.current?.click()
  }

  const handleRemoveAttachment = (index: number) => {
    setAttachments((prev) => prev.filter((_, itemIndex) => itemIndex !== index))
  }

  const handleImageSelection = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ""

    if (files.length === 0) {
      return
    }

    const nextAttachments: ChatAttachment[] = []
    for (const file of files) {
      if (!ALLOWED_IMAGE_TYPES.has(file.type)) {
        toast.error(
          t("chat.invalidImage", {
            name: file.name,
          }),
        )
        continue
      }

      if (file.size > MAX_IMAGE_SIZE_BYTES) {
        toast.error(
          t("chat.imageTooLarge", {
            name: file.name,
            size: MAX_IMAGE_SIZE_LABEL,
          }),
        )
        continue
      }

      try {
        nextAttachments.push({
          type: "image",
          filename: file.name,
          url: await readFileAsDataUrl(file),
        })
      } catch {
        toast.error(
          t("chat.imageReadFailed", {
            name: file.name,
          }),
        )
      }
    }

    if (nextAttachments.length > 0) {
      setAttachments(nextAttachments.slice(0, 1))
    }
  }

  const canSubmit =
    canInput &&
    (Boolean(input.trim()) || attachments.length > 0)

  return (
    <div className="bg-background/95 flex h-full flex-col">
      <PageHeader
        title={t("navigation.openresponsesChat")}
        className={`transition-shadow ${
          hasScrolled ? "shadow-xs" : "shadow-none"
        }`}
      >
        <Button
          variant="secondary"
          size="sm"
          onClick={newChat}
          className="h-9 gap-2"
        >
          <IconPlus className="size-4" />
          <span className="hidden sm:inline">{t("chat.newChat")}</span>
        </Button>

        <SessionHistoryMenu
          sessions={sessions}
          activeSessionId={activeSessionId}
          hasMore={hasMore}
          loadError={loadError}
          loadErrorMessage={loadErrorMessage}
          observerRef={observerRef}
          onOpenChange={(open) => {
            if (open) {
              void loadSessions(true)
            }
          }}
          onSwitchSession={switchSession}
          onDeleteSession={handleDeleteSession}
        />
      </PageHeader>

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="min-h-0 flex-1 overflow-y-auto px-4 py-6 md:px-8 lg:px-24 xl:px-48"
      >
        <div className="mx-auto flex w-full max-w-250 flex-col gap-8 pb-8">
          {messages.length === 0 && !isTyping && (
            <ChatEmptyState
              hasAvailableModels={isGatewayRunning}
              defaultModelName={isGatewayRunning ? "OpenResponses" : ""}
              isConnected={isGatewayRunning}
            />
          )}

          {messages.map((msg) => {
            const toolCall =
              msg.role === "assistant" && msg.kind !== "thought"
                ? detectToolCall(msg.content)
                : null

            return (
              <div key={msg.id} className="flex w-full">
                {msg.role === "assistant" ? (
                  toolCall ? (
                    <ToolCallMessage
                      toolName={toolCall.toolName}
                      args={toolCall.args}
                      output={toolCall.output}
                      timestamp={msg.timestamp}
                    />
                  ) : (
                    <AssistantMessage
                      content={msg.content}
                      isThought={msg.kind === "thought"}
                      timestamp={msg.timestamp}
                      attachments={msg.attachments}
                    />
                  )
                ) : (
                  <UserMessage
                    content={msg.content}
                    attachments={msg.attachments}
                  />
                )}
              </div>
            )
          })}

          {isTyping && <TypingIndicator />}
        </div>
      </div>

      <input
        ref={fileInputRef}
        type="file"
        accept="image/jpeg,image/png,image/gif,image/webp,image/bmp"
        className="hidden"
        onChange={handleImageSelection}
      />

      <ChatComposer
        input={input}
        attachments={attachments}
        onInputChange={setInput}
        onAddImages={handleAddImages}
        onRemoveAttachment={handleRemoveAttachment}
        onSend={handleSend}
        inputDisabledReason={inputDisabledReason}
        canSend={canSubmit}
      />
    </div>
  )
}
