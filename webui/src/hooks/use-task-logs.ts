import { useCallback, useMemo } from 'react'
import { useInfiniteQuery, useTransport, createConnectQueryKey } from '@connectrpc/connect-query'
import { useQueryClient, type InfiniteData } from '@tanstack/react-query'
import { listLogChunksByTask } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { ListLogChunksByTaskResponse } from '@/gen/gritz/v1/gritz_pb'
import { useVisibilityInterval } from './use-visibility-interval'

// The log is append-only, so every page is an immutable ascending window over a
// fixed chunk-id range — the same shape as useTaskTimeline's event pages: open
// at the tail, prepend older pages on demand, fetch only the newer page to
// follow. Chunks within a page are oldest-first, so pages flatten straight into
// transcript order.
const PAGE_SIZE = 50

// There is no append notification for log chunks (deliberately — see the
// ship-driver-logs proposal), so follow mode is purely poll-based in v1 and
// this interval is the only thing driving the tail. It is NOT a backstop behind
// a faster signal, which is what the timeline's identically-named constant is.
// The shipper cuts a chunk at most every 2s, so there is little point polling
// much faster than that.
const FOLLOW_POLL_MS = 5_000

// Follow polls return an empty page once the tail is caught up (next_page_token
// is always populated, so it can't signal "done"). Left alone those empty pages
// accumulate. Trimming them is lossless: the preceding page's next_page_token
// already points at the same resume cursor. Keep at least one page.
export function dropTrailingEmpty(
  data: InfiniteData<ListLogChunksByTaskResponse>,
): InfiniteData<ListLogChunksByTaskResponse> {
  let end = data.pages.length
  while (end > 1 && data.pages[end - 1].chunks.length === 0) end--
  if (end === data.pages.length) return data
  return { pages: data.pages.slice(0, end), pageParams: data.pageParams.slice(0, end) }
}

// The chunk bytes are a raw terminal transcript (a mirror of /gritz/log), so
// they can carry ANSI escape sequences from the agent CLI or setup commands.
// Strip CSI/OSC sequences for display; anything else renders verbatim.
// eslint-disable-next-line no-control-regex
const ANSI_ESCAPES = /\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)?)/g

// decodeChunks concatenates the loaded pages' chunk bytes and decodes them as
// one UTF-8 stream. The chunks are cut at byte counts, so a multi-byte
// character can straddle a chunk boundary — the bytes must be joined before
// decoding, not decoded chunk by chunk.
export function decodeChunks(data: InfiniteData<ListLogChunksByTaskResponse> | undefined): string {
  if (!data) return ''
  const chunks = data.pages.flatMap((p) => p.chunks)
  const size = chunks.reduce((n, c) => n + c.data.length, 0)
  const bytes = new Uint8Array(size)
  let offset = 0
  for (const c of chunks) {
    bytes.set(c.data, offset)
    offset += c.data.length
  }
  return new TextDecoder().decode(bytes).replace(ANSI_ESCAPES, '')
}

// useTaskLogs serves the task detail view's Logs tab as a bidirectional
// infinite query over the shipped driver log: it opens at the newest page (the
// end of the log — what a post-mortem reader wants first), loads older pages
// via loadOlder, and, while `follow` is set (the task is still producing
// output), polls the tail on the backstop interval.
export function useTaskLogs(taskId: bigint) {
  const transport = useTransport()
  const queryClient = useQueryClient()

  // The page param (pageToken) must be present in the input; its value here is
  // the initial page param — empty selects the newest (tail) page. Memoized so
  // it stays referentially stable for the follow callback's deps.
  const input = useMemo(() => ({ taskId, pageSize: PAGE_SIZE, pageToken: '' }), [taskId])

  const {
    data,
    isLoading,
    fetchPreviousPage,
    hasPreviousPage,
    isFetchingPreviousPage,
    fetchNextPage,
  } = useInfiniteQuery(listLogChunksByTask, input, {
    // An empty initial pageToken selects the newest (tail) page: one request
    // on open, no history walk.
    pageParamKey: 'pageToken',
    // prev_page_token walks toward older chunks; it empties at history's start,
    // which flips hasPreviousPage to false.
    getPreviousPageParam: (firstPage) => firstPage.prevPageToken || undefined,
    // next_page_token is always populated (it doubles as the live-follow
    // cursor), so it can't mean "stop" — it's fetched only by the follow poll,
    // never as an automatic "load more". Even an empty first page (a task that
    // has not shipped a chunk yet) carries one, following from the start of the
    // log.
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
  })

  // followTail fetches only chunks newer than the newest loaded and appends
  // them at the bottom, then trims the empty page a caught-up poll leaves
  // behind. cancelRefetch: false makes overlapping polls a no-op rather than
  // cancelling an in-flight follow.
  const followTail = useCallback(async () => {
    await fetchNextPage({ cancelRefetch: false })
    const key = createConnectQueryKey({
      schema: listLogChunksByTask,
      input,
      transport,
      cardinality: 'infinite',
      pageParamKey: 'pageToken',
    })
    queryClient.setQueryData<InfiniteData<ListLogChunksByTaskResponse>>(key, (prev) =>
      prev ? dropTrailingEmpty(prev) : prev,
    )
  }, [fetchNextPage, queryClient, transport, input])

  // Poll unconditionally, including for a terminal task. The driver submits its
  // terminal runner event BEFORE flushing the log (internal/agent/driver.go), so
  // a task's final chunks — the "task failed" / "agent stopped" lines and
  // whatever DriverLog.Close's backstop ships — always land after the status has
  // already flipped. Gating on !isTerminalTask meant those were never fetched
  // until a remount. `gritz logs -f` has never gated on status either.
  useVisibilityInterval(() => void followTail(), FOLLOW_POLL_MS)

  return {
    text: useMemo(() => decodeChunks(data), [data]),
    isLoading,
    loadOlder: fetchPreviousPage,
    hasOlder: hasPreviousPage,
    isLoadingOlder: isFetchingPreviousPage,
  }
}
