import { useEffect } from 'react'
import type { DescMethodUnary } from '@bufbuild/protobuf'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { createConnectQueryKey } from '@connectrpc/connect-query'
import {
  getTask,
  listLinks,
  listTasks,
  listExternalEvents,
  getEvent,
  listWorkspaces,
  listOrgMembers,
  listKeys,
  getSchedule,
  listSchedules,
  getAgent,
  listAgents,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useNotificationSSE, useTimelineFollowers } from '@/lib/services'
import type { TimelineFollowers } from '@/lib/timeline-follow'
import type { Notification, NotificationResource } from '@/lib/notification-sse'

function invalidateResource(
  qc: QueryClient,
  timelineFollowers: TimelineFollowers,
  r: NotificationResource,
) {
  console.debug('[sse] invalidate', r)
  switch (r.type) {
    case 'task':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({
          schema: getTask,
          input: { id: BigInt(r.id) },
          cardinality: 'finite',
        }),
      })
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listTasks, cardinality: 'finite' }),
      })
      break
    case 'task_events':
      // Reports and lifecycle transitions are events on the task's stream. A
      // task_events change means the stream grew. The
      // timeline is an append-only bidirectional infinite query, so instead of
      // invalidating (which would re-fetch every loaded page), drive its
      // live-follow: fetch only the newer page and append it at the tail.
      timelineFollowers.notify(String(r.id))
      break
    case 'task_links':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({
          schema: listLinks,
          input: { taskId: BigInt(r.id) },
          cardinality: 'finite',
        }),
      })
      break
    case 'event':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listExternalEvents, cardinality: 'finite' }),
      })
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({
          schema: getEvent,
          input: { id: BigInt(r.id) },
          cardinality: 'finite',
        }),
      })
      break
    case 'workspaces':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listWorkspaces, cardinality: 'finite' }),
      })
      break
    case 'org_members':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listOrgMembers, cardinality: 'finite' }),
      })
      break
    case 'keys':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listKeys, cardinality: 'finite' }),
      })
      break
    case 'schedule':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({
          schema: getSchedule,
          input: { id: BigInt(r.id) },
          cardinality: 'finite',
        }),
      })
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listSchedules, cardinality: 'finite' }),
      })
      break
    case 'agent':
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({
          schema: getAgent,
          input: { id: BigInt(r.id) },
          cardinality: 'finite',
        }),
      })
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listAgents, cardinality: 'finite' }),
      })
      break
    default:
      console.warn('[sse] unhandled resource type', r)
  }
}

// The finite query families a reconnect resyncs. While the SSE was disconnected
// we missed change notifications, so everything cached may be stale — these
// mirror the families invalidateResource refreshes per notification. The
// append-only listEventsByTask timeline is deliberately absent: refetching an
// invalidated infinite query re-fetches every loaded page, and reconnects happen
// routinely (network blips, tab background→foreground), so it catches up via a
// single tail-follow (notifyAll) instead.
const RECONNECT_SCHEMAS: DescMethodUnary[] = [
  getTask,
  listLinks,
  listTasks,
  listExternalEvents,
  getEvent,
  listWorkspaces,
  listOrgMembers,
  listKeys,
  getSchedule,
  listSchedules,
  getAgent,
  listAgents,
]

// handleReconnect resyncs after the notification SSE drops and comes back. It
// invalidates each finite family by allow-list (an input-less key prefix-matches
// every cached instance of that family), then drives every mounted timeline's
// tail-follow. The timeline is never invalidated by construction.
export function handleReconnect(qc: QueryClient, timelineFollowers: TimelineFollowers) {
  for (const schema of RECONNECT_SCHEMAS) {
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema, cardinality: 'finite' }) })
  }
  timelineFollowers.notifyAll()
}

function handleNotification(
  qc: QueryClient,
  timelineFollowers: TimelineFollowers,
  n: Notification,
) {
  console.debug('[sse] notification', n)
  if (n.type === 'ready') {
    return
  }
  for (const r of n.resources ?? []) {
    invalidateResource(qc, timelineFollowers, r)
  }
}

export function useOrgSSE() {
  const queryClient = useQueryClient()
  const sse = useNotificationSSE()
  const timelineFollowers = useTimelineFollowers()

  useEffect(() => {
    const removeNotification = sse.addNotificationListener((n) => {
      handleNotification(queryClient, timelineFollowers, n)
    })
    const removeReconnect = sse.addReconnectListener(() => {
      handleReconnect(queryClient, timelineFollowers)
    })
    return () => {
      removeNotification()
      removeReconnect()
    }
  }, [queryClient, sse, timelineFollowers])
}
