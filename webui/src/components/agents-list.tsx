import {
  AgentActions,
  AgentName,
  LinkKindChip,
  LinkKindItem,
  Starters,
  type AgentsViewProps,
} from '@/components/agent-overview'
import { Overflow } from '@/components/overflow'
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'

// How many of an agent's triggers and schedules, and of its link kinds, a row
// shows before the rest collapse into a count.
const MAX_STARTERS = 3
const MAX_LINKS = 3

// AgentsList is the agents as a table: who the agent is, what starts its
// tasks, and what those tasks follow.
export function AgentsList({ agents, eventTypes, linkTypes }: AgentsViewProps) {
  return (
    <ListTable
      header={
        <>
          <ListHead>Agent</ListHead>
          <ListHead>Starts on</ListHead>
          <ListHead className="hidden lg:table-cell">Follows</ListHead>
          <ListHead />
        </>
      }
    >
      <TableBody>
        {agents.map((agent) => (
          <TableRow key={String(agent.id)}>
            <TableCell className="py-3 pl-4 align-top">
              <AgentName agent={agent} />
            </TableCell>
            <TableCell className="py-3 align-top whitespace-normal">
              <Starters agent={agent} eventTypes={eventTypes} max={MAX_STARTERS} />
            </TableCell>
            <TableCell className="hidden py-3 align-top whitespace-normal lg:table-cell">
              {agent.links.length === 0 ? (
                <span className="text-muted-foreground/70">Nothing</span>
              ) : (
                <Overflow
                  layout="row"
                  max={MAX_LINKS}
                  className="gap-x-1.5 gap-y-1.5"
                  items={agent.links.map((l, i) => ({
                    key: String(i),
                    node: <LinkKindChip link={l} linkTypes={linkTypes} />,
                    full: <LinkKindItem link={l} eventTypes={eventTypes} linkTypes={linkTypes} />,
                  }))}
                />
              )}
            </TableCell>
            <TableCell className="py-3 pr-3 align-top">
              <AgentActions agent={agent} />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </ListTable>
  )
}
