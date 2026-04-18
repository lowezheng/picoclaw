import { createFileRoute } from "@tanstack/react-router"

import { OpenResponsesChatPage } from "@/components/openresponses-chat/openresponses-chat-page"

export const Route = createFileRoute("/openresponses-chat")({
  component: OpenResponsesChatPage,
})
