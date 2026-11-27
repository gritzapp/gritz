import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import { createAgent, getAgent } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { AgentForm } from '@/components/agent-form'
import { useOrgId } from '@/hooks/use-org-id'
import {
  agentCloneValues,
  buildAgent,
  emptyAgentValues,
  useInvalidateAgents,
  type AgentFormValues,
} from '@/lib/agents'

// clone prefills the form with a copy of that agent, as its page's Clone does.
type NewAgentSearch = { clone?: string }

export const Route = createFileRoute('/agents/new')({
  // Another org's agent doesn't apply.
  staticData: { orgSwitchRedirect: '/agents/new' },
  validateSearch: (search: Record<string, unknown>): NewAgentSearch => ({
    clone: typeof search.clone === 'string' ? search.clone : undefined,
  }),
  component: NewAgentPage,
})

function NewAgentPage() {
  const { clone } = Route.useSearch()
  const cloneId = clone && /^\d+$/.test(clone) ? BigInt(clone) : undefined
  const { data, isLoading, error } = useQuery(
    getAgent,
    { id: cloneId },
    { enabled: cloneId !== undefined },
  )

  if (clone === undefined) return <NewAgentForm key="new" initialValues={emptyAgentValues()} />

  const agent = data?.agent
  if (isLoading || error || !agent) {
    return (
      <div className="flex h-full items-center justify-center">
        {error ? (
          <div className="text-destructive">Error: {error.message}</div>
        ) : (
          <div className="text-muted-foreground">
            {isLoading ? 'Loading agent...' : 'Agent not found'}
          </div>
        )}
      </div>
    )
  }
  return <NewAgentForm key={`clone-${agent.id}`} initialValues={agentCloneValues(agent)} />
}

function NewAgentForm({ initialValues: initial }: { initialValues: AgentFormValues }) {
  const navigate = useNavigate()
  const orgId = useOrgId()
  const mutation = useMutation(createAgent)
  const invalidateAgents = useInvalidateAgents()
  // Seeded once: a refetch of the cloned agent doesn't reset the draft.
  const [initialValues] = useState(initial)

  const handleSubmit = async (values: AgentFormValues) => {
    const { agent } = await mutation.mutateAsync({ agent: buildAgent(values, 0n) })
    await invalidateAgents()
    if (agent) {
      navigate({ to: '/agents/$id', params: { id: String(agent.id) }, search: { org: orgId } })
    } else {
      navigate({ to: '/agents', search: { org: orgId } })
    }
  }

  return (
    <AgentForm
      initialValues={initialValues}
      submitLabel={mutation.isPending ? 'Creating...' : 'Create agent'}
      isSubmitting={mutation.isPending}
      error={mutation.error?.message ?? null}
      onSubmit={handleSubmit}
    />
  )
}
