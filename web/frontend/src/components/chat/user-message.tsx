import type { ChatAttachment } from "@/store/chat"

interface UserMessageProps {
  content: string
  attachments?: ChatAttachment[]
}

export function UserMessage({ content, attachments = [] }: UserMessageProps) {
  const hasText = content.trim().length > 0
  const imageAttachments = attachments.filter(
    (attachment) => attachment.type === "image",
  )
  const fileAttachments = attachments.filter(
    (attachment) => attachment.type === "file",
  )

  return (
    <div className="flex w-full flex-col items-end gap-1.5">
      {imageAttachments.length > 0 && (
        <div className="flex max-w-[70%] flex-wrap justify-end gap-2">
          {imageAttachments.map((attachment, index) => (
            <img
              key={`${attachment.url}-${index}`}
              src={attachment.url}
              alt={attachment.filename || "Uploaded image"}
              className="max-h-72 max-w-full object-cover"
            />
          ))}
        </div>
      )}

      {fileAttachments.length > 0 && (
        <div className="flex max-w-[70%] flex-col gap-1.5">
          {fileAttachments.map((attachment, index) => (
            <a
              key={`${attachment.url}-${index}`}
              href={attachment.url}
              download={attachment.filename}
              className="rounded-lg bg-violet-500/20 px-3 py-2 text-sm text-white hover:bg-violet-500/30"
            >
              <span className="truncate">{attachment.filename || "File"}</span>
            </a>
          ))}
        </div>
      )}

      {hasText && (
        <div className="max-w-[70%] rounded-2xl rounded-tr-sm bg-violet-500 px-5 py-3 text-[15px] leading-relaxed wrap-break-word whitespace-pre-wrap text-white shadow-sm">
          {content}
        </div>
      )}
    </div>
  )
}
