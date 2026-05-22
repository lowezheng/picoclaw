import { useTranslation } from "react-i18next"

import { cn } from "@/components/openresponses-chat/lib/utils"

interface SelectionBlockProps {
  options: string[]
  onSelect?: (option: string) => void
  disabled?: boolean
}

export function SelectionBlock({
  options,
  onSelect,
  disabled = false,
}: SelectionBlockProps) {
  const { t } = useTranslation()

  return (
    <div className="mt-3 flex flex-col gap-2 rounded-xl border border-dashed border-slate-200 bg-slate-50/60 p-3 dark:border-slate-700/60 dark:bg-slate-800/40">
      <span className="text-muted-foreground text-xs font-medium">
        💡 {t("chat.suggestedActions", "您还可以：")}
      </span>
      <div className="flex flex-wrap gap-2">
        {options.map((option, index) => (
          <button
            key={index}
            type="button"
            disabled={disabled}
            onClick={() => onSelect?.(option)}
            className={cn(
              "inline-flex items-center rounded-lg border px-3 py-1.5 text-sm transition-colors",
              "border-slate-200 bg-white text-slate-700 shadow-xs",
              "hover:border-slate-300 hover:bg-slate-50",
              "dark:border-slate-600 dark:bg-slate-700 dark:text-slate-200",
              "dark:hover:border-slate-500 dark:hover:bg-slate-600",
              disabled && "cursor-not-allowed opacity-50",
            )}
          >
            {option}
          </button>
        ))}
      </div>
    </div>
  )
}
