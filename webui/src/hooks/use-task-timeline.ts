import { useCallback, useEffect, useMemo } from 'react'
import { useInfiniteQuery, useTransport, createConnectQueryKey } from '@connectrpc/connect-query'
import { useQueryClient, type InfiniteData } from '@tanstack/react-query'
import { listEventsByTask } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { ListEventsByTaskResponse } from '@/gen/gritz/v1/gritz_pb'
import { eventsToTimeline } from '@/lib/timeline'
import { useTimelineFollowers } from '@/lib/services'
import { useVisibilityInterval } from './use-visibility-interval'

// The timeline is append-only, so every page is an immutable ascending window
// over a fixed id range. That lets us open at the tail, prepend older pages on
// demand, and fetch only the newer page on each live signal — no reversal, no
// refetchInterval, no invalidation of loaded pages.
const PAGE_SIZE = 50

// New-entry detection is driven by the SSE task_events signal (→ follow()). A
// dropped or missed signal would stall the tail until the next signal or an SSE
// reconnect, so poll follow() on an interval as a backstop. follow() is a cheap
// one-page tail-fetch (an idle poll is one empty-page request), and
// cancelRefetch: false makes an overlap with an SSE-driven follow a no-op.
const FOLLOW_POLL_MS = 30_000

// Live-follow tail polls return an empty page once the stream is caught up
// (next_page_token is always populated, so it can't signal "done"). Left alone
// those empty pages accumulate. Trimming them is lossless: the preceding page's
// next_page_token already points at the same resume cursor, so the live-follow
// picks up exactly where it left off. Keep at least one page.
function dropTrailingEmpty(
  data: InfiniteData<ListEventsByTaskResponse>,
): InfiniteData<ListEventsByTaskResponse> {
  let end = data.pages.length
  while (end > 1 && data.pages[end - 1].events.length === 0) end--
  if (end === data.pages.length) return data
  return { pages: data.pages.slice(0, end), pageParams: data.pageParams.slice(0, end) }
}

// useTaskTimeline serves the task detail view's activity timeline as a
// bidirectional infinite query: it opens at the newest page, loads older pages
// via loadOlder, and follows the tail on each SSE task_events signal.
export function useTaskTimeline(taskId: bigint) {
  const transport = useTransport()
  const queryClient = useQueryClient()
  const timelineFollowers = useTimelineFollowers()

  // The page param (pageToken) must be present in the input; its value here is
  // the initial page param — empty selects the newest (tail) page. Memoized so
  // it stays referentially stable for the follow callback's deps.
  const input = useMemo(() => ({ taskId, pageSize: PAGE_SIZE, pageToken: '' }), [taskId])

  const { data, fetchPreviousPage, hasPreviousPage, isFetchingPreviousPage, fetchNextPage } =
    useInfiniteQuery(listEventsByTask, input, {
      // An empty initial pageToken selects the newest (tail) page: one request
      // on open, no history walk.
      pageParamKey: 'pageToken',
      // prev_page_token walks toward older rows; it empties at history's start,
      // which flips hasPreviousPage to false.
      getPreviousPageParam: (firstPage) => firstPage.prevPageToken || undefined,
      // next_page_token is always populated (it doubles as the live-follow
      // cursor), so it can't mean "stop" — it's fetched only on an SSE signal
      // via follow(), never as an automatic "load more". Even an empty first
      // page (a task with no events yet) carries one, following from the start.
      getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    })

  // follow fetches only events newer than the newest loaded (id > cursor) and
  // appends them at the bottom, then trims the empty page a caught-up poll
  // leaves behind. cancelRefetch: false makes overlapping signals a no-op
  // rather than cancelling an in-flight follow.
  const follow = useCallback(async () => {
    await fetchNextPage({ cancelRefetch: false })
    const key = createConnectQueryKey({
      schema: listEventsByTask,
      input,
      transport,
      cardinality: 'infinite',
      pageParamKey: 'pageToken',
    })
    queryClient.setQueryData<InfiniteData<ListEventsByTaskResponse>>(key, (prev) =>
      prev ? dropTrailingEmpty(prev) : prev,
    )
  }, [fetchNextPage, queryClient, transport, input])

  // A task_events SSE signal for this task arrives on the org-wide stream
  // (use-org-sse). Register so that signal drives our live-follow directly.
  useEffect(
    () => timelineFollowers.register(String(taskId), () => void follow()),
    [timelineFollowers, taskId, follow],
  )

  // Backstop the SSE-driven live-follow with a visibility-gated interval so a
  // dropped task_events signal can't stall the tail longer than the poll period.
  // The poll is skipped while the tab is hidden (a background tab has closed the
  // SSE and reconnects + resyncs on foreground).
  useVisibilityInterval(() => void follow(), FOLLOW_POLL_MS)

  // Every page is ascending, so the loaded pages flatten into one ascending
  // stream — no reversal.
  const events = data?.pages.flatMap((p) => p.events) ?? []

  return {
    timeline: eventsToTimeline(events),
    follow,
    loadOlder: fetchPreviousPage,
    hasOlder: hasPreviousPage,
    isLoadingOlder: isFetchingPreviousPage,
  }
}
