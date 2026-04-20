import { useState } from "react"
import { IconChevronDown, IconChevronUp } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { cn } from "@/lib/utils"
import {
  type DataQualityItem,
  type DataQualitySummary,
  extractStarRating,
  parsePercent,
} from "@/lib/parse-message-blocks"

interface DataQualityBlockProps {
  summary: DataQualitySummary
  items: DataQualityItem[]
}

function ProgressBar({
  label,
  value,
  max = 100,
}: {
  label: string
  value: number
  max?: number
}) {
  const pct = Math.min(100, Math.max(0, (value / max) * 100))
  return (
    <div className="flex items-center gap-2 text-xs">
      <span className="w-20 shrink-0 text-slate-500 dark:text-slate-400">
        {label}
      </span>
      <div className="h-2 flex-1 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-700">
        <div
          className="h-full rounded-full bg-emerald-500 transition-all duration-500 dark:bg-emerald-400"
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="w-10 shrink-0 text-right tabular-nums text-slate-600 dark:text-slate-300">
        {value}
      </span>
    </div>
  )
}

export function DataQualityBlock({ summary, items }: DataQualityBlockProps) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const score = summary["置信度得分"]
  const { label: starLabel } = extractStarRating(score)

  const dimensions = [
    { label: t("chat.dqPurity", "原始数据纯度"), value: parsePercent(summary["原始数据纯度"]) },
    { label: t("chat.dqAuthority", "来源权威性"), value: parsePercent(summary["来源权威性"]) },
    { label: t("chat.dqTimeliness", "数据时效性"), value: parsePercent(summary["数据时效性"]) },
    { label: t("chat.dqTraceability", "可溯源占比"), value: parsePercent(summary["可溯源占比"]) },
    { label: t("chat.dqConsistency", "一致性校验"), value: parsePercent(summary["一致性校验"]) },
  ]

  return (
    <div className="mt-3 rounded-xl border border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-800/60">
      {/* Header - 始终显示 */}
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
          <span className="text-lg font-bold text-emerald-600 dark:text-emerald-400 tabular-nums">
            {score}
          </span>
          <span className="text-sm">{starLabel}</span>
          {expanded ? (
            <IconChevronUp className="h-4 w-4 text-slate-400" />
          ) : (
            <IconChevronDown className="h-4 w-4 text-slate-400" />
          )}
        </div>
      </button>

      {/* Body - 展开后显示 */}
      {expanded && (
        <div className="space-y-3 px-4 py-3">
          <div className="space-y-2">
            {dimensions.map((dim) => (
              <ProgressBar
                key={dim.label}
                label={dim.label}
                value={dim.value}
              />
            ))}
          </div>

          {items.length > 0 && (
            <>
              <div className="border-t border-slate-100 dark:border-slate-700/60" />
              <div>
                <span className="mb-1.5 block text-xs font-medium text-slate-500 dark:text-slate-400">
                  📋 {t("chat.dqSources", "数据来源")}
                </span>
                <div className="space-y-1">
                  {items.map((item, idx) => (
                    <div
                      key={idx}
                      className="flex items-center gap-2 text-xs text-slate-600 dark:text-slate-300"
                    >
                      <span className="inline-block rounded bg-slate-100 px-1.5 py-0.5 text-[10px] text-slate-500 dark:bg-slate-700 dark:text-slate-400">
                        {item.type}
                      </span>
                      <span>{item.name}</span>
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
