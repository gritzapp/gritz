import { Link } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { listAgents } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Agent } from '@/gen/gritz/v1/gritz_pb'
import { AgentMonogram } from '@/components/agent-overview'
import { useOrgId } from '@/hooks/use-org-id'
import { cn } from '@/lib/utils'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

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
    <Select value={selected} onValueChange={onChange} disabled={isLoading}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue
          placeholder={
            isLoading
              ? 'Loading agents…'
              : agents.length === 0
                ? 'No agents yet — create one first'
                : 'Select an agent'
          }
        />
      </SelectTrigger>
      <SelectContent>
        {agents.map((agent) => (
          <SelectItem key={String(agent.id)} value={String(agent.id)}>
            <span>{agent.name}</span>
            <span className="text-muted-foreground text-xs ml-2">
              {agent.runner} / {agent.workspace}
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
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
// carries its agent's colour wherever it is listed.
export function AgentBadge({ agentId, className }: { agentId: bigint; className?: string }) {
  const orgId = useOrgId()
  const agent = useAgent(agentId)
  if (!agent) {
    return <span className={cn('text-muted-foreground', className)}>#{String(agentId)}</span>
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
