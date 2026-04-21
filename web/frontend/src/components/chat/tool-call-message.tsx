import { IconChevronDown, IconChevronUp, IconTerminal, IconCheck, IconCopy } from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { formatMessageTime } from "@/hooks/use-pico-chat"
import { cn } from "@/lib/utils"

interface ChatAttachment {
  type: "image" | "file"
  url: string
  filename?: string
}

interface ToolCallMessageProps {
  toolName: string
  args?: string
  output?: string
  timestamp?: string | number
  attachments?: ChatAttachment[]
}

export function ToolCallMessage({
  toolName,
  args,
  output,
  timestamp = "",
  attachments = [],
}: ToolCallMessageProps) {
  const { t } = useTranslation()
  const [isExpanded, setIsExpanded] = useState(false)
  const [isCopied, setIsCopied] = useState(false)
  const formattedTimestamp =
    timestamp !== "" ? formatMessageTime(timestamp) : ""

  const handleCopy = () => {
    const textToCopy = [toolName, args, output].filter(Boolean).join("\n\n")
    navigator.clipboard.writeText(textToCopy).then(() => {
      setIsCopied(true)
      setTimeout(() => setIsCopied(false), 2000)
    })
  }

  return (
    <div className="group flex w-full flex-col gap-1.5">
      <div className="text-muted-foreground flex items-center justify-between gap-2 px-1 text-xs opacity-70">
        <div className="flex items-center gap-2">
          <span>PicoClaw</span>
          <span className="inline-flex items-center gap-1 rounded-full border border-slate-300/80 bg-slate-100/80 px-2 py-0.5 text-[11px] font-medium text-slate-700 dark:border-slate-500/40 dark:bg-slate-500/15 dark:text-slate-200">
            <IconTerminal className="size-3" />
            <span>{t("chat.toolCallLabel", { tool: toolName })}</span>
          </span>
          {formattedTimestamp && (
            <>
              <span className="opacity-50">•</span>
              <span>{formattedTimestamp}</span>
            </>
          )}
        </div>
      </div>

      <div className="relative overflow-hidden rounded-xl border border-slate-200/90 bg-slate-50/70 dark:border-slate-500/35 dark:bg-slate-500/10">
        {/* Tool args / header */}
        {args && (
          <div className="border-b border-slate-200/60 px-3 py-2 dark:border-slate-500/20">
            <button
              onClick={() => setIsExpanded(!isExpanded)}
              className="flex w-full items-center justify-between gap-2"
            >
              <code className="text-[13px] font-mono text-slate-800 dark:text-slate-100">
                {args.length > 80 ? args.slice(0, 80) + "..." : args}
              </code>
              <span className="shrink-0 text-slate-500 dark:text-slate-400">
                {isExpanded ? (
                  <IconChevronUp className="size-4" />
                ) : (
                  <IconChevronDown className="size-4" />
                )}
              </span>
            </button>
          </div>
        )}

        {/* Output */}
        <div
          className={cn(
            "overflow-hidden transition-all",
            isExpanded ? "max-h-[600px]" : "max-h-[120px]",
          )}
        >
          <pre className="whitespace-pre-wrap break-words p-3 text-[13px] leading-relaxed text-slate-800 dark:text-slate-100">
            {output || "..."}
          </pre>
        </div>

        {/* Gradient fade when collapsed */}
        {!isExpanded && output && output.length > 200 && (
          <div className="pointer-events-none absolute bottom-0 left-0 right-0 h-8 bg-gradient-to-t from-slate-50/70 to-transparent dark:from-slate-500/10" />
        )}

        {/* Copy button */}
        <Button
          variant="ghost"
          size="icon"
          className="absolute top-2 right-2 h-7 w-7 opacity-0 transition-opacity group-hover:opacity-100 bg-slate-100/70 hover:bg-slate-200/80 dark:bg-slate-500/20 dark:hover:bg-slate-400/30"
          onClick={handleCopy}
        >
          {isCopied ? (
            <IconCheck className="h-4 w-4 text-green-500" />
          ) : (
            <IconCopy className="text-muted-foreground h-4 w-4" />
          )}
        </Button>
      </div>

      {attachments.length > 0 && (
        <div className="flex flex-wrap gap-2 px-1">
          {attachments.map((attachment, index) => (
            <img
              key={`${attachment.url}-${index}`}
              src={attachment.url}
              alt="Generated image"
              className="max-h-72 max-w-full rounded-lg object-cover shadow-sm"
            />
          ))}
        </div>
      )}
    </div>
  )
}
