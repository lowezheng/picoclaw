import { atom, getDefaultStore } from "jotai"

import {
  getInitialActiveSessionId,
  writeStoredSessionId,
} from "@/features/chat/state"

export interface ChatAttachment {
  type: "image"
  url: string
  filename?: string
}

export type AssistantMessageKind = "normal" | "thought"

export interface ChatMessage {
  id: string
  role: "user" | "assistant"
  content: string
  timestamp: number | string
  kind?: AssistantMessageKind
  attachments?: ChatAttachment[]
  toolCall?: {
    callId: string
    name: string
    arguments: string
  }
}

export type ConnectionState = "idle" | "sending" | "error"

export interface OpenResponsesChatStoreState {
  messages: ChatMessage[]
  connectionState: ConnectionState
  isTyping: boolean
  activeSessionId: string
}

type ChatStorePatch = Partial<OpenResponsesChatStoreState>

const DEFAULT_STATE: OpenResponsesChatStoreState = {
  messages: [],
  connectionState: "idle",
  isTyping: false,
  activeSessionId: getInitialActiveSessionId(),
}

export const openResponsesChatAtom = atom<OpenResponsesChatStoreState>(DEFAULT_STATE)

const store = getDefaultStore()

export function getOpenResponsesChatState() {
  return store.get(openResponsesChatAtom)
}

export function updateOpenResponsesChatStore(
  patch:
    | ChatStorePatch
    | ((prev: OpenResponsesChatStoreState) => ChatStorePatch | OpenResponsesChatStoreState),
) {
  store.set(openResponsesChatAtom, (prev) => {
    const nextPatch = typeof patch === "function" ? patch(prev) : patch
    const next = { ...prev, ...nextPatch }

    if (next.activeSessionId !== prev.activeSessionId) {
      writeStoredSessionId(next.activeSessionId)
    }

    return next
  })
}
