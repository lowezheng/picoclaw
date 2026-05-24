import { createFileRoute } from "@tanstack/react-router"

import { OpenResponsesChatPage } from "@/components/openresponses-chat/chat-page"

export const Route = createFileRoute("/openresponses")({
  component: OpenResponsesChatPage,
})
