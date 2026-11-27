import type { TimelineItem } from '@/lib/timeline'
import { Scrollback } from '@/components/scrollback'
import { TaskTimeline } from '@/components/task-timeline'

// A chat-style task timeline — the message list is an infinite scroll (older
// pages load as you scroll up, the tail follows new events) and the composer is
// locked to the bottom. Wraps the existing TaskTimeline renderer; the
// scroll/stick/prepend behavior lives in Scrollback.
export function TaskTimelineChat({
  items,
  agentId,
  hasOlder,
  loadOlder,
  isLoadingOlder,
  composer,
}: {
  items: TimelineItem[]
  // The task's agent, which its output is shown under.
  agentId: bigint
  hasOlder: boolean
  loadOlder: () => void
  isLoadingOlder: boolean
  composer?: React.ReactNode
}) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <Scrollback
        contentLength={items.length}
        hasOlder={hasOlder}
        loadOlder={loadOlder}
        isLoadingOlder={isLoadingOlder}
        className="p-6"
      >
        <TaskTimeline items={items} agentId={agentId} />
      </Scrollback>
      {composer && <div className="shrink-0 border-t p-4">{composer}</div>}
    </div>
  )
}
