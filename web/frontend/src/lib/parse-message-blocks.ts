const MESSAGE_BLOCK_REGEX =
  /---MESSAGE_START---\s*\n?([\s\S]*?)\n?\s*---MESSAGE_END---/g

export interface DataQualitySummary {
  原始数据纯度: string
  来源权威性: string | number
  数据时效性: string | number
  可溯源占比: string
  一致性校验: string
  置信度得分: number
}

export interface DataQualityItem {
  type: string
  name: string
}

export type ParsedBlock =
  | { type: "text"; content: string }
  | { type: "selection"; options: string[] }
  | { type: "dataquality"; summary: DataQualitySummary; items: DataQualityItem[] }

function parsePercent(value: string | number): number {
  if (typeof value === "number") return value
  const num = parseFloat(value.replace(/[^0-9.]/g, ""))
  return isNaN(num) ? 0 : num
}

function extractStarRating(score: number): { stars: number; label: string } {
  if (score >= 95) return { stars: 5, label: "⭐⭐⭐⭐⭐" }
  if (score >= 85) return { stars: 4, label: "⭐⭐⭐⭐" }
  if (score >= 75) return { stars: 3, label: "⭐⭐⭐" }
  if (score >= 65) return { stars: 2, label: "⭐⭐" }
  return { stars: 1, label: "⭐" }
}

export function parseMessageBlocks(content: string): ParsedBlock[] {
  const blocks: ParsedBlock[] = []
  let lastIndex = 0
  let match: RegExpExecArray | null

  MESSAGE_BLOCK_REGEX.lastIndex = 0

  while ((match = MESSAGE_BLOCK_REGEX.exec(content)) !== null) {
    const [fullMatch, jsonStr] = match
    const startIndex = match.index

    if (startIndex > lastIndex) {
      const text = content.slice(lastIndex, startIndex).trim()
      if (text) {
        blocks.push({ type: "text", content: text })
      }
    }

    try {
      const parsed = JSON.parse(jsonStr.trim())
      if (parsed.messageType === "selection" && Array.isArray(parsed.options)) {
        blocks.push({ type: "selection", options: parsed.options })
      } else if (
        parsed.messageType === "dataquality" &&
        parsed.summary &&
        Array.isArray(parsed.items)
      ) {
        const score = Number(parsed.summary["置信度得分"]) || 0
        blocks.push({
          type: "dataquality",
          summary: {
            ...parsed.summary,
            置信度得分: score,
            原始数据纯度: String(parsed.summary["原始数据纯度"] || "0%"),
            来源权威性: parsed.summary["来源权威性"] ?? "0",
            数据时效性: parsed.summary["数据时效性"] ?? "0",
            可溯源占比: String(parsed.summary["可溯源占比"] || "0%"),
            一致性校验: String(parsed.summary["一致性校验"] || "0%"),
          },
          items: parsed.items,
        })
      } else {
        blocks.push({ type: "text", content: fullMatch })
      }
    } catch {
      blocks.push({ type: "text", content: fullMatch })
    }

    lastIndex = startIndex + fullMatch.length
  }

  if (lastIndex < content.length) {
    const text = content.slice(lastIndex).trim()
    if (text) {
      blocks.push({ type: "text", content: text })
    }
  }

  return blocks
}

export { parsePercent, extractStarRating }
