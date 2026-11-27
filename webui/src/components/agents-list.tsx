import {
  AgentActions,
  AgentName,
  LinkKindChip,
  Starters,
  type AgentsViewProps,
} from '@/components/agent-overview'
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'

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
              <Starters agent={agent} eventTypes={eventTypes} />
            </TableCell>
            <TableCell className="hidden py-3 align-top whitespace-normal lg:table-cell">
              {agent.links.length === 0 ? (
                <span className="text-muted-foreground/70">Nothing</span>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {agent.links.map((l, i) => (
                    <LinkKindChip key={i} link={l} eventTypes={eventTypes} linkTypes={linkTypes} />
                  ))}
                </div>
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
