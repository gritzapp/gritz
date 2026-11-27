import { Link } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { listAgents } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Agent } from '@/gen/gritz/v1/gritz_pb'
import { AgentMonogram } from '@/components/agent-overview'
import { useOrgId } from '@/hooks/use-org-id'
import { cn } from '@/lib/utils'
import { Combobox, type ComboboxOption } from '@/components/combobox'

// AgentSelect picks one of the org's agents. value is the agent's decimal ID,
// or '' for none.
export function AgentSelect({
  id,
  value,
  onChange,
}: {
  id?: string
  value: string
  onChange: (agentId: string) => void
}) {
  const { data, isLoading } = useQuery(listAgents, {})
  const agents = data?.agents ?? []
  // A remembered or saved ID whose agent is gone shows as unselected.
  const selected = agents.some((a) => String(a.id) === value) ? value : ''

  return (
    <Combobox
      id={id}
      value={selected}
      onChange={onChange}
      disabled={isLoading}
      placeholder={
        isLoading
          ? 'Loading agents…'
          : agents.length === 0
            ? 'No agents yet — create one first'
            : 'Select an agent'
      }
      searchPlaceholder="Search agents..."
      emptyText="No agent found."
      options={agents.map(agentOption)}
    />
  )
}

// agentOption is an agent as a Combobox option: its monogram and name, with
// where its tasks run.
export function agentOption(agent: Agent): ComboboxOption {
  return {
    value: String(agent.id),
    label: agent.name,
    icon: <AgentMonogram name={agent.name} className="size-5 rounded text-[9px]" />,
    description: `${agent.runner}/${agent.workspace}`,
  }
}

// useAgent returns the org's live agent with the given ID, from ListAgents.
// It's undefined while loading and for a deleted agent.
export function useAgent(agentId: bigint): Agent | undefined {
  const { data } = useQuery(listAgents, {})
  return data?.agents.find((a) => a.id === agentId)
}

// AgentRef links to an agent's page by name. A deleted agent, which only
// archived tasks can still belong to, renders as its ID.
export function AgentRef({ agentId, className }: { agentId: bigint; className?: string }) {
  const orgId = useOrgId()
  const agent = useAgent(agentId)
  if (!agent) return <span className={className}>#{String(agentId)}</span>
  return (
    <Link
      to="/agents/$id"
      params={{ id: String(agentId) }}
      search={{ org: orgId }}
      className={className ?? 'text-primary hover:underline'}
    >
      {agent.name}
    </Link>
  )
}

// AgentBadge is AgentRef with the agent's monogram before its name, so a task
// carries its agent's colour wherever it is listed. With detail it is laid out
// as the Agents list shows an agent, for a list column of its own: a larger
// monogram, the name, and detail on a second line. Below md only the monogram
// is left.
export function AgentBadge({
  agentId,
  className,
  detail,
}: {
  agentId: bigint
  className?: string
  detail?: string
}) {
  const orgId = useOrgId()
  const agent = useAgent(agentId)
  if (!agent) {
    return <span className={cn('text-muted-foreground', className)}>#{String(agentId)}</span>
  }
  if (detail !== undefined) {
    return (
      <Link
        to="/agents/$id"
        params={{ id: String(agentId) }}
        search={{ org: orgId }}
        aria-label={agent.name}
        title={agent.name}
        className={cn('group inline-flex max-w-full min-w-0 items-center gap-3', className)}
      >
        <AgentMonogram name={agent.name} />
        <span className="hidden min-w-0 md:block">
          <span className="block truncate font-medium group-hover:underline">{agent.name}</span>
          <span className="text-muted-foreground block truncate font-mono text-xs">{detail}</span>
        </span>
      </Link>
    )
  }
  return (
    <Link
      to="/agents/$id"
      params={{ id: String(agentId) }}
      search={{ org: orgId }}
      className={cn('group inline-flex max-w-full min-w-0 items-center gap-2', className)}
    >
      <AgentMonogram name={agent.name} className="size-5 rounded text-[9px]" />
      <span className="truncate group-hover:underline">{agent.name}</span>
    </Link>
  )
}
