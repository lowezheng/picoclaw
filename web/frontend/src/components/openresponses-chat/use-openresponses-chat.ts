import { useAtomValue } from "jotai"

import {
  newOpenResponsesChatSession,
  sendOpenResponsesChatMessage,
  switchOpenResponsesChatSession,
} from "@/features/openresponses-chat/controller"
import { openResponsesChatAtom } from "@/store/openresponses-chat"

export function useOpenResponsesChat() {
  const { messages, connectionState, isTyping, activeSessionId } =
    useAtomValue(openResponsesChatAtom)

  return {
    messages,
    connectionState,
    isTyping,
    activeSessionId,
    sendMessage: sendOpenResponsesChatMessage,
    switchSession: switchOpenResponsesChatSession,
    newChat: newOpenResponsesChatSession,
  }
}
