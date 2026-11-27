import {
  AgentActions,
  AgentName,
  LinkKindChip,
  Starters,
  type AgentsViewProps,
} from '@/components/agent-overview'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

const headClass = 'text-muted-foreground h-9 text-xs font-medium tracking-wide uppercase'

// AgentsList is the agents as a table: who the agent is, what starts its
// tasks, and what those tasks follow.
export function AgentsList({ agents, eventTypes, linkTypes }: AgentsViewProps) {
  return (
    <div className="overflow-hidden rounded-lg border">
      <Table>
        <TableHeader className="bg-muted/50">
          <TableRow className="hover:bg-transparent">
            <TableHead className={`${headClass} pl-4`}>Agent</TableHead>
            <TableHead className={headClass}>Starts on</TableHead>
            <TableHead className={`${headClass} hidden lg:table-cell`}>Follows</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
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
                      <LinkKindChip
                        key={i}
                        link={l}
                        eventTypes={eventTypes}
                        linkTypes={linkTypes}
                      />
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
      </Table>
    </div>
  )
}
