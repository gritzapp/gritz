import {
  AgentActions,
  AgentName,
  LinkKindItem,
  Starters,
  type AgentsViewProps,
} from '@/components/agent-overview'

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-muted-foreground mb-2 text-[11px] font-medium tracking-wider uppercase">
      {children}
    </div>
  )
}

// AgentsCards is each agent as a card with everything written out: every
// trigger's conditions and every link kind's events.
export function AgentsCards({ agents, eventTypes, linkTypes }: AgentsViewProps) {
  return (
    <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
      {agents.map((agent) => (
        <div key={String(agent.id)} className="bg-card flex flex-col rounded-xl border shadow-xs">
          <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
            <AgentName agent={agent} />
            <AgentActions agent={agent} />
          </div>
          <div className="grid flex-1 gap-x-6 gap-y-4 px-4 py-4 sm:grid-cols-2">
            <div className="min-w-0">
              <SectionLabel>Starts on</SectionLabel>
              <Starters agent={agent} eventTypes={eventTypes} detail />
            </div>
            <div className="min-w-0">
              <SectionLabel>Follows</SectionLabel>
              {agent.links.length === 0 ? (
                <span className="text-muted-foreground/70 text-sm">Nothing</span>
              ) : (
                <div className="space-y-3">
                  {agent.links.map((l, i) => (
                    <LinkKindItem key={i} link={l} eventTypes={eventTypes} linkTypes={linkTypes} />
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}
