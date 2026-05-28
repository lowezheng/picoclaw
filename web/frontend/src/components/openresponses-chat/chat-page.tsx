import { IconPlus } from "@tabler/icons-react"
import { useAtomValue } from "jotai"
import { type ChangeEvent, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { AssistantMessage } from "@/components/openresponses-chat/assistant-message"
import {
  ChatComposer,
  type ChatInputDisabledReason,
} from "@/components/openresponses-chat/chat-composer"
import { ChatEmptyState } from "@/components/openresponses-chat/chat-empty-state"
import { SessionHistoryMenu } from "@/components/openresponses-chat/session-history-menu"
import { TypingIndicator } from "@/components/openresponses-chat/typing-indicator"
import { UserMessage } from "@/components/openresponses-chat/user-message"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { ModelSelector } from "@/components/chat/model-selector"
import { useChatModels } from "@/hooks/use-chat-models"
import { useGateway } from "@/hooks/use-gateway"
import { useOpenResponsesSessionHistory } from "@/hooks/use-openresponses-session-history"
import {
  newOpenResponsesSession,
  sendOpenResponsesMessage,
  switchOpenResponsesSession,
} from "@/features/openresponses-chat/controller"
import { openResponsesChatAtom } from "@/store/openresponses-chat"
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
  hasDefaultModel,
  gatewayState,
}: {
  hasDefaultModel: boolean
  gatewayState: string
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
  if (!hasDefaultModel) {
    return "noDefaultModel"
  }
  return null
}

export function OpenResponsesChatPage() {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [isAtBottom, setIsAtBottom] = useState(true)
  const [input, setInput] = useState("")
  const [attachments, setAttachments] = useState<ChatAttachment[]>([])

  const {
    messages,
    isTyping,
    activeSessionId,
    contextUsage,
  } = useAtomValue(openResponsesChatAtom)

  const { state: gwState } = useGateway()
  const isGatewayRunning = gwState === "running"

  const {
    defaultModelName,
    hasAvailableModels,
    apiKeyModels,
    oauthModels,
    localModels,
    handleSetDefault,
  } = useChatModels({ isConnected: isGatewayRunning })
  const hasDefaultModel = Boolean(defaultModelName)

  const inputDisabledReason = resolveChatInputDisabledReason({
    hasDefaultModel,
    gatewayState: gwState,
  })
  const canInput = inputDisabledReason === null

  const {
    sessions,
    hasMore,
    loadError,
    loadErrorMessage,
    observerRef,
    loadSessions,
    handleDeleteSession,
  } = useOpenResponsesSessionHistory({
    activeSessionId,
    onDeletedActiveSession: newOpenResponsesSession,
  })

  const syncScrollState = (element: HTMLDivElement) => {
    const { clientHeight, scrollHeight, scrollTop } = element
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

  const handleSend = (content?: string) => {
    const text = content !== undefined ? content : input
    if ((!text.trim() && attachments.length === 0) || !canInput) return
    void sendOpenResponsesMessage({
      content: text,
      attachments,
      model: defaultModelName,
    })
    setInput("")
    setAttachments([])
  }

  const handleSelectOption = (option: string) => {
    handleSend(option)
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
    canInput && hasDefaultModel && (Boolean(input.trim()) || attachments.length > 0)

  return (
    <div className="bg-background/95 flex h-full flex-col">
      <PageHeader
        title={t("chat.openresponsesTitle", "OpenResponses Chat")}
        titleExtra={
          hasAvailableModels && (
            <ModelSelector
              defaultModelName={defaultModelName}
              apiKeyModels={apiKeyModels}
              oauthModels={oauthModels}
              localModels={localModels}
              onValueChange={handleSetDefault}
            />
          )
        }
      >
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground hover:text-foreground flex items-center gap-1"
            onClick={newOpenResponsesSession}
          >
            <IconPlus className="size-4" />
            <span className="hidden sm:inline">
              {t("chat.newChat", "New Chat")}
            </span>
          </Button>
          <SessionHistoryMenu
            sessions={sessions}
            hasMore={hasMore}
            loadError={loadError}
            loadErrorMessage={loadErrorMessage}
            observerRef={observerRef}
            activeSessionId={activeSessionId}
            onOpenChange={(open) => {
              if (open) {
                void loadSessions(true)
              }
            }}
            onSwitchSession={switchOpenResponsesSession}
            onDeleteSession={handleDeleteSession}
          />
        </div>
      </PageHeader>

      <input
        ref={fileInputRef}
        type="file"
        accept="image/*"
        multiple
        className="hidden"
        onChange={handleImageSelection}
      />

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto px-4 py-6 md:px-8 lg:px-24 xl:px-48"
      >
        {messages.length === 0 ? (
          <ChatEmptyState
            hasAvailableModels={hasAvailableModels}
            defaultModelName={defaultModelName}
            isConnected={isGatewayRunning}
          />
        ) : (
          <div className="mx-auto flex max-w-[1000px] flex-col gap-6 pb-8">
            {messages.map((message) => {
              if (message.role === "user") {
                return (
                  <UserMessage
                    key={message.id}
                    content={message.content}
                    attachments={message.attachments}
                  />
                )
              }
              return (
                <AssistantMessage
                  key={message.id}
                  content={message.content}
                  kind={message.kind}
                  timestamp={message.timestamp}
                  toolCalls={message.toolCalls}
                  attachments={message.attachments}
                  onSelectOption={handleSelectOption}
                />
              )
            })}
            {isTyping && <TypingIndicator />}
          </div>
        )}
      </div>

      <ChatComposer
        input={input}
        attachments={attachments}
        onInputChange={setInput}
        onAddImages={handleAddImages}
        onRemoveAttachment={handleRemoveAttachment}
        onSend={handleSend}
        inputDisabledReason={inputDisabledReason}
        canSend={canSubmit}
        contextUsage={contextUsage}
      />
    </div>
  )
}
