import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { formatDistanceToNow } from 'date-fns'
import { listTasks } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Agent, Task } from '@/gen/gritz/v1/gritz_pb'
import { StatusBadge } from '@/components/status-badge'
import { HoverCard, HoverCardContent, HoverCardTrigger } from '@/components/ui/hover-card'
import { useOrgId } from '@/hooks/use-org-id'
import { taskLabel } from '@/lib/task'
import {
  AgentActions,
  AgentName,
  LinkKindChip,
  LinkKindItem,
  LinkTypeIcon,
  MarkTile,
  Starters,
  type AgentsViewProps,
} from '@/components/agent-overview'
import { Overflow } from '@/components/overflow'
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'

// How many of an agent's triggers and schedules, and of its link kinds, a row
// shows before the rest collapse into a count.
const MAX_STARTERS = 5
const MAX_LINKS = 5

// The page of tasks the Recent column is drawn from, and how many of an
// agent's tasks in it its hover card lists.
const RECENT_PAGE_SIZE = 100
const MAX_RECENT = 8

// AgentsList is the agents as a table: who the agent is, what starts its
// tasks, and what those tasks follow.
export function AgentsList({ agents, eventTypes, linkTypes }: AgentsViewProps) {
  // The newest page of unarchived tasks, which is also what the task switcher
  // loads. An agent whose tasks are all older than that page shows none.
  const { data: tasksData } = useQuery(listTasks, {
    pageSize: RECENT_PAGE_SIZE,
    pageToken: '',
    archived: false,
  })
  const recent = useMemo(() => {
    const byAgent = new Map<bigint, Task[]>()
    for (const task of tasksData?.tasks ?? []) {
      const tasks = byAgent.get(task.agentId)
      if (tasks) tasks.push(task)
      else byAgent.set(task.agentId, [task])
    }
    return byAgent
  }, [tasksData])

  return (
    <ListTable
      header={
        <>
          <ListHead>Agent</ListHead>
          <ListHead>Starts on</ListHead>
          <ListHead className="hidden lg:table-cell">Follows</ListHead>
          <ListHead className="hidden md:table-cell">Recent</ListHead>
          <ListHead />
        </>
      }
    >
      <TableBody>
        {agents.map((agent) => (
          <TableRow key={String(agent.id)}>
            <TableCell className="w-[30%] py-3 pl-4">
              <AgentName agent={agent} />
            </TableCell>
            <TableCell className="py-3 whitespace-normal">
              <Starters agent={agent} eventTypes={eventTypes} max={MAX_STARTERS} />
            </TableCell>
            <TableCell className="hidden w-[18%] py-3 whitespace-normal lg:table-cell">
              {agent.links.length === 0 ? (
                <span className="text-muted-foreground/70">Nothing</span>
              ) : agent.links.length === 1 ? (
                <Overflow
                  layout="row"
                  max={1}
                  items={[
                    {
                      key: '0',
                      node: <LinkKindChip link={agent.links[0]} linkTypes={linkTypes} />,
                      full: (
                        <LinkKindItem
                          link={agent.links[0]}
                          eventTypes={eventTypes}
                          linkTypes={linkTypes}
                        />
                      ),
                    },
                  ]}
                />
              ) : (
                <Overflow
                  layout="stack"
                  max={MAX_LINKS}
                  items={agent.links.map((l, i) => ({
                    key: String(i),
                    node: (
                      <MarkTile>
                        <LinkTypeIcon
                          type={l.type}
                          className="mr-0 size-3.5 -translate-x-0.5 -translate-y-0.5"
                        />
                      </MarkTile>
                    ),
                    full: <LinkKindItem link={l} eventTypes={eventTypes} linkTypes={linkTypes} />,
                  }))}
                />
              )}
            </TableCell>
            <TableCell className="hidden w-[16%] py-3 whitespace-nowrap md:table-cell">
              <RecentTasks agent={agent} tasks={recent.get(agent.id) ?? []} />
            </TableCell>
            <TableCell className="w-px py-3 pr-3">
              <AgentActions agent={agent} />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </ListTable>
  )
}

// RecentTasks says how long ago the agent's newest task was created, linking
// to the task list filtered to the agent. Hovering lists its newest tasks.
function RecentTasks({ agent, tasks }: { agent: Agent; tasks: Task[] }) {
  const orgId = useOrgId()
  if (tasks.length === 0) {
    return <span className="text-muted-foreground/70">None</span>
  }
  const newest = tasks[0].createdAt
  const shown = tasks.slice(0, MAX_RECENT)
  return (
    <HoverCard openDelay={150} closeDelay={100}>
      <HoverCardTrigger asChild>
        <Link
          to="/tasks"
          search={{ org: orgId, agent: String(agent.id) }}
          className="hover:underline"
        >
          {newest ? formatDistanceToNow(timestampDate(newest), { addSuffix: true }) : 'Tasks'}
        </Link>
      </HoverCardTrigger>
      <HoverCardContent align="start" className="w-auto max-w-md min-w-72 space-y-3 p-3">
        {shown.map((task) => (
          <Link
            key={String(task.id)}
            to="/tasks/$id"
            params={{ id: String(task.id) }}
            search={{ org: orgId }}
            className="group flex items-center gap-3 text-sm"
          >
            <span className="min-w-0 flex-1 truncate group-hover:underline">{taskLabel(task)}</span>
            <StatusBadge task={task} />
            {task.createdAt && (
              <span className="text-muted-foreground shrink-0 text-xs">
                {formatDistanceToNow(timestampDate(task.createdAt), { addSuffix: true })}
              </span>
            )}
          </Link>
        ))}
        {tasks.length > shown.length && (
          <div className="text-muted-foreground text-xs">
            +{tasks.length - shown.length} more in the task list
          </div>
        )}
      </HoverCardContent>
    </HoverCard>
  )
}
