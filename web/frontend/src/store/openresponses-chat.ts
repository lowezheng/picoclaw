import { atom, getDefaultStore } from "jotai"
import { atomWithStorage } from "jotai/utils"

const LAST_OR_SESSION_STORAGE_KEY = "picoclaw:or-last-session-id"
const UNIX_MS_THRESHOLD = 1e12

export interface ChatAttachment {
  type: "image" | "audio" | "video" | "file"
  url: string
  filename?: string
  contentType?: string
}

export interface ChatToolCallFunction {
  name?: string
  arguments?: string
}

export interface ChatToolCall {
  id?: string
  type?: string
  function?: ChatToolCallFunction
}

export type AssistantMessageKind = "normal" | "thought" | "tool_calls"

export interface ChatMessage {
  id: string
  role: "user" | "assistant"
  content: string
  timestamp: number | string
  kind?: AssistantMessageKind
  modelName?: string
  attachments?: ChatAttachment[]
  toolCalls?: ChatToolCall[]
}

export interface ContextUsage {
  used_tokens: number
  total_tokens: number
  compress_at_tokens: number
  used_percent: number
}

export type ConnectionState =
  | "idle"
  | "connecting"
  | "streaming"
  | "error"
  | "completed"

export interface OpenResponsesChatState {
  messages: ChatMessage[]
  connectionState: ConnectionState
  isTyping: boolean
  activeSessionId: string
  contextUsage?: ContextUsage
}

type ChatStorePatch = Partial<OpenResponsesChatState>

function readStorageValue(): string {
  return (
    globalThis.localStorage?.getItem(LAST_OR_SESSION_STORAGE_KEY)?.trim() || ""
  )
}

function writeStorageValue(sessionId: string) {
  if (sessionId) {
    globalThis.localStorage?.setItem(LAST_OR_SESSION_STORAGE_KEY, sessionId)
  } else {
    globalThis.localStorage?.removeItem(LAST_OR_SESSION_STORAGE_KEY)
  }
}

function generateSessionId(): string {
  const webCrypto = globalThis.crypto
  if (webCrypto && typeof webCrypto.randomUUID === "function") {
    return webCrypto.randomUUID()
  }
  if (webCrypto && typeof webCrypto.getRandomValues === "function") {
    const bytes = new Uint8Array(16)
    webCrypto.getRandomValues(bytes)
    bytes[6] = (bytes[6] & 0x0f) | 0x40
    bytes[8] = (bytes[8] & 0x3f) | 0x80
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0"))
    return (
      `${hex[0]}${hex[1]}${hex[2]}${hex[3]}-` +
      `${hex[4]}${hex[5]}-` +
      `${hex[6]}${hex[7]}-` +
      `${hex[8]}${hex[9]}-` +
      `${hex[10]}${hex[11]}${hex[12]}${hex[13]}${hex[14]}${hex[15]}`
    )
  }
  return `or-session-${Date.now()}-${Math.random().toString(16).slice(2, 10)}`
}

function getInitialActiveSessionId(): string {
  return readStorageValue() || generateSessionId()
}

export function normalizeUnixTimestamp(timestamp: number): number {
  return timestamp < UNIX_MS_THRESHOLD ? timestamp * 1000 : timestamp
}

const DEFAULT_STATE: OpenResponsesChatState = {
  messages: [],
  connectionState: "idle",
  isTyping: false,
  activeSessionId: getInitialActiveSessionId(),
}

export const openResponsesChatAtom = atom<OpenResponsesChatState>(DEFAULT_STATE)

export const showThoughtsAtom = atomWithStorage<boolean>(
  "picoclaw:or-show-thoughts",
  false,
)

const store = getDefaultStore()

export function getOpenResponsesChatState() {
  return store.get(openResponsesChatAtom)
}

export function updateOpenResponsesChatStore(
  patch: ChatStorePatch | ((prev: OpenResponsesChatState) => ChatStorePatch | OpenResponsesChatState),
) {
  store.set(openResponsesChatAtom, (prev) => {
    const nextPatch = typeof patch === "function" ? patch(prev) : patch
    const next = { ...prev, ...nextPatch }
    if (next.activeSessionId !== prev.activeSessionId) {
      writeStorageValue(next.activeSessionId)
    }
    return next
  })
}

export function generateNewOpenResponsesSessionId(): string {
  const id = generateSessionId()
  updateOpenResponsesChatStore({
    activeSessionId: id,
    messages: [],
    connectionState: "idle",
    isTyping: false,
    contextUsage: undefined,
  })
  return id
}

export function setOpenResponsesSessionId(sessionId: string) {
  updateOpenResponsesChatStore({
    activeSessionId: sessionId,
    messages: [],
    connectionState: "idle",
    isTyping: false,
    contextUsage: undefined,
  })
}
