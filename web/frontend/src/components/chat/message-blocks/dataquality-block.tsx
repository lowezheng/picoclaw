import { useState } from "react"
import {
  IconChevronDown,
  IconChevronUp,
  IconQuote,
  IconBook,
  IconX,
} from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { cn } from "@/lib/utils"
import type { Dimension, Source } from "@/lib/parse-message-blocks"

interface DataQualityBlockProps {
  overallScore: number
  rating: string
  dimensions: Dimension[]
  sources: Source[]
}

function ScoreBar({
  label,
  score,
  weight,
  reason,
}: {
  label: string
  score: number
  weight: number
  reason: string
}) {
  const pct = Math.min(100, Math.max(0, score))
  const colorClass =
    score >= 90
      ? "bg-emerald-500 dark:bg-emerald-400"
      : score >= 70
        ? "bg-amber-500 dark:bg-amber-400"
        : "bg-rose-500 dark:bg-rose-400"

  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2 text-xs">
        <span className="w-24 shrink-0 font-medium text-slate-600 dark:text-slate-300">
          {label}
        </span>
        <div className="h-2 flex-1 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-700">
          <div
            className={cn("h-full rounded-full transition-all duration-500", colorClass)}
            style={{ width: `${pct}%` }}
          />
        </div>
        <span className="w-8 shrink-0 text-right tabular-nums text-slate-700 dark:text-slate-200">
          {score}
        </span>
        <span className="inline-block rounded bg-slate-100 px-1.5 py-0.5 text-[10px] tabular-nums text-slate-500 dark:bg-slate-700 dark:text-slate-400">
          {(weight * 100).toFixed(0)}%
        </span>
      </div>
      {reason && (
        <p className="ml-[6.5rem] text-[11px] text-slate-400 dark:text-slate-500">
          {reason}
        </p>
      )}
    </div>
  )
}

function CitationBadge({ type }: { type: Source["citationType"] }) {
  const config = {
    direct: {
      icon: <IconQuote className="h-3 w-3" />,
      label: "直接引用",
      className:
        "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-500/15 dark:text-emerald-300 dark:border-emerald-500/30",
    },
    summary: {
      icon: <IconBook className="h-3 w-3" />,
      label: "概括",
      className:
        "bg-sky-50 text-sky-700 border-sky-200 dark:bg-sky-500/15 dark:text-sky-300 dark:border-sky-500/30",
    },
    none: {
      icon: <IconX className="h-3 w-3" />,
      label: "未提及",
      className:
        "bg-slate-50 text-slate-500 border-slate-200 dark:bg-slate-700/50 dark:text-slate-400 dark:border-slate-600",
    },
  }
  const c = config[type]
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px]",
        c.className,
      )}
    >
      {c.icon}
      {c.label}
    </span>
  )
}

export function DataQualityBlock({
  overallScore,
  rating,
  dimensions,
  sources,
}: DataQualityBlockProps) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)

  const scoreColor =
    overallScore >= 90
      ? "text-emerald-600 dark:text-emerald-400"
      : overallScore >= 70
        ? "text-amber-600 dark:text-amber-400"
        : "text-rose-600 dark:text-rose-400"

  return (
    <div className="mt-3 rounded-xl border border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-800/60">
      {/* Header — 始终显示 */}
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className={cn(
          "flex w-full items-center justify-between gap-3 px-4 py-3 text-left transition-colors",
          "hover:bg-slate-50 dark:hover:bg-slate-700/40",
          expanded && "border-b border-slate-100 dark:border-slate-700/60",
        )}
      >
        <div className="flex items-center gap-2">
          <span className="text-sm">📊</span>
          <span className="text-sm font-medium text-slate-700 dark:text-slate-200">
            {t("chat.dqTitle", "数据质量评估")}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <span
            className={cn(
              "text-lg font-bold tabular-nums",
              scoreColor,
            )}
          >
            {overallScore}
          </span>
          <span className="text-sm">{rating}</span>
          {expanded ? (
            <IconChevronUp className="h-4 w-4 text-slate-400" />
          ) : (
            <IconChevronDown className="h-4 w-4 text-slate-400" />
          )}
        </div>
      </button>

      {/* Body — 展开后显示 */}
      {expanded && (
        <div className="space-y-4 px-4 py-3">
          {/* Dimensions */}
          <div className="space-y-3">
            {dimensions.map((dim, idx) => (
              <ScoreBar
                key={idx}
                label={dim.name}
                score={dim.score}
                weight={dim.weight}
                reason={dim.reason}
              />
            ))}
          </div>

          {/* Sources */}
          {sources.length > 0 && (
            <>
              <div className="border-t border-slate-100 dark:border-slate-700/60" />
              <div>
                <span className="mb-2 block text-xs font-medium text-slate-500 dark:text-slate-400">
                  📋 {t("chat.dqSources", "数据来源")}
                </span>
                <div className="space-y-1.5">
                  {sources.map((src, idx) => (
                    <div
                      key={idx}
                      className="flex items-center gap-2 text-xs text-slate-600 dark:text-slate-300"
                    >
                      <span className="inline-block rounded bg-slate-100 px-1.5 py-0.5 font-medium text-slate-600 dark:bg-slate-700 dark:text-slate-300">
                        {src.toolName}
                      </span>
                      <span className="flex-1 truncate">{src.keyData}</span>
                      <CitationBadge type={src.citationType} />
                    </div>
                  ))}
                </div>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}
