import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import { getAgent, updateAgent } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { AgentForm } from '@/components/agent-form'
import {
  agentFormValues,
  buildAgent,
  useInvalidateAgents,
  type AgentFormValues,
} from '@/lib/agents'

export const Route = createFileRoute('/agents/$id')({
  staticData: { orgSwitchRedirect: '/agents' },
  component: AgentPage,
})

function AgentPage() {
  const { id } = Route.useParams()
  const { data, isLoading, error } = useQuery(getAgent, { id: BigInt(id) })
  const mutation = useMutation(updateAgent)
  const invalidateAgents = useInvalidateAgents()
  // Bumped after each save, so the form is reseeded from the saved agent.
  const [revision, setRevision] = useState(0)

  const agent = data?.agent

  const handleSubmit = async (values: AgentFormValues) => {
    if (!agent) return
    await mutation.mutateAsync({ agent: buildAgent(values, agent.id) })
    await invalidateAgents()
    setRevision((r) => r + 1)
  }

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

  return (
    <AgentForm
      key={`${agent.id}-${revision}`}
      agentId={agent.id}
      savedSchedules={agent.schedules}
      initialValues={agentFormValues(agent)}
      submitLabel={mutation.isPending ? 'Saving...' : 'Save changes'}
      isSubmitting={mutation.isPending}
      error={mutation.error?.message ?? null}
      onSubmit={handleSubmit}
    />
  )
}
