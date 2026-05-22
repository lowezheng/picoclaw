const MESSAGE_BLOCK_REGEX =
  /---MESSAGE_START---\s*\n?([\s\S]*?)\n?\s*---MESSAGE_END---/g

export interface Dimension {
  name: string
  score: number
  weight: number
  reason: string
}

export interface Source {
  toolName: string
  keyData: string
  citationType: "direct" | "summary" | "none"
}

export type ParsedBlock =
  | { type: "text"; content: string }
  | { type: "selection"; options: string[] }
  | {
      type: "dataquality"
      overallScore: number
      rating: string
      dimensions: Dimension[]
      sources: Source[]
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
        typeof parsed.overallScore === "number" &&
        Array.isArray(parsed.dimensions)
      ) {
        blocks.push({
          type: "dataquality",
          overallScore: parsed.overallScore,
          rating: String(parsed.rating ?? ""),
          dimensions: parsed.dimensions.map((d: Record<string, unknown>) => ({
            name: String(d.name ?? ""),
            score: Number(d.score ?? 0),
            weight: Number(d.weight ?? 0),
            reason: String(d.reason ?? ""),
          })),
          sources: Array.isArray(parsed.sources)
            ? parsed.sources.map((s: Record<string, unknown>) => ({
                toolName: String(s.toolName ?? ""),
                keyData: String(s.keyData ?? ""),
                citationType: ["direct", "summary", "none"].includes(
                  String(s.citationType),
                )
                  ? (String(s.citationType) as "direct" | "summary" | "none")
                  : "none",
              }))
            : [],
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
