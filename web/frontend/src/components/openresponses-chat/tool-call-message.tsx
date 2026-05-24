import {
  IconCheck,
  IconChevronDown,
  IconCopy,
  IconTool,
} from "@tabler/icons-react"
import hljs from "highlight.js/lib/core"
import json from "highlight.js/lib/languages/json"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { cn } from "@/components/openresponses-chat/lib/utils"

hljs.registerLanguage("json", json)

interface ToolCallMessageProps {
  toolName: string
  args?: string
  output?: string
  timestamp?: string | number
}

function highlightJson(code: string): string | null {
  try {
    return hljs.highlight(code, { language: "json" }).value
  } catch {
    return null
  }
}

export function ToolCallMessage({
  toolName,
  args,
  output,
}: ToolCallMessageProps) {
  const { t } = useTranslation()
  const [isExpanded, setIsExpanded] = useState(true)
  const [isCopied, setIsCopied] = useState(false)

  const handleCopy = () => {
    const textToCopy = args || ""
    navigator.clipboard.writeText(textToCopy).then(() => {
      setIsCopied(true)
      setTimeout(() => setIsCopied(false), 2000)
    })
  }

  const formattedArgs = args ? (() => {
    try {
      const parsed = JSON.parse(args)
      return JSON.stringify(parsed, null, 2)
    } catch {
      return args
    }
  })() : ""

  const highlightedHtml = formattedArgs ? highlightJson(formattedArgs) : null

  return (
    <div className="group flex w-full flex-col gap-1.5">
      {/* Tool call card */}
      <div
        className={cn(
          "relative overflow-hidden rounded-xl border",
          "border-orange-200/80 bg-orange-50/40",
          "dark:border-orange-500/25 dark:bg-orange-500/8",
        )}
      >
        {/* Header bar */}
        <div
          className={cn(
            "flex items-center justify-between gap-2 border-b px-3 py-2",
            "border-orange-200/60 bg-orange-100/50",
            "dark:border-orange-500/20 dark:bg-orange-500/10",
          )}
        >
          <div className="flex items-center gap-2">
            <IconTool className="size-3.5 text-orange-600 dark:text-orange-400" />
            <span className="text-[13px] font-semibold text-orange-800 dark:text-orange-300">
              {toolName}
            </span>
          </div>
          <div className="flex items-center gap-1">
            <Button
              type="button"
              variant="ghost"
              size="xs"
              className="h-7 text-orange-700 hover:bg-orange-200/60 hover:text-orange-900 dark:text-orange-300 dark:hover:bg-orange-500/20 dark:hover:text-orange-100"
              onClick={handleCopy}
            >
              {isCopied ? (
                <IconCheck className="size-3.5 text-green-500" />
              ) : (
                <IconCopy className="size-3.5" />
              )}
              <span className="hidden sm:inline text-xs">
                {isCopied ? t("chat.copiedLabel") : t("chat.copyCode")}
              </span>
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="xs"
              className="h-7 text-orange-700 hover:bg-orange-200/60 hover:text-orange-900 dark:text-orange-300 dark:hover:bg-orange-500/20 dark:hover:text-orange-100"
              onClick={() => setIsExpanded((v) => !v)}
            >
              <IconChevronDown
                className={cn(
                  "size-3.5 transition-transform duration-200",
                  isExpanded && "rotate-180",
                )}
              />
              <span className="hidden sm:inline text-xs">
                {isExpanded ? t("chat.collapseCode") : t("chat.expandCode")}
              </span>
            </Button>
          </div>
        </div>

        {/* Code block */}
        {isExpanded && (
          <div className="overflow-x-auto">
            <pre className="m-0 bg-[#f6f8fa] px-4 py-3 font-mono text-[13px] leading-6 dark:bg-[#0d1117]">
              {highlightedHtml ? (
                <code
                  className="hljs language-json"
                  dangerouslySetInnerHTML={{ __html: highlightedHtml }}
                />
              ) : (
                <code className="language-json">
                  {formattedArgs}
                </code>
              )}
            </pre>
          </div>
        )}

        {/* Output */}
        {output && (
          <div className="border-t border-orange-200/60 px-3 py-2 dark:border-orange-500/20">
            <div className="text-muted-foreground/55 text-[11px] font-medium tracking-wide uppercase">
              {t("chat.toolCallOutputLabel")}
            </div>
            <pre className="mt-1.5 whitespace-pre-wrap break-words font-mono text-[12px] leading-relaxed text-slate-700 dark:text-slate-300">
              {output}
            </pre>
          </div>
        )}
      </div>
    </div>
  )
}
