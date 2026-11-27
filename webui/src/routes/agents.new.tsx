import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { createAgent } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { AgentForm } from '@/components/agent-form'
import { useOrgId } from '@/hooks/use-org-id'
import {
  buildAgent,
  emptyAgentValues,
  useInvalidateAgents,
  type AgentFormValues,
} from '@/lib/agents'

export const Route = createFileRoute('/agents/new')({
  component: NewAgentPage,
})

function NewAgentPage() {
  const navigate = useNavigate()
  const orgId = useOrgId()
  const mutation = useMutation(createAgent)
  const invalidateAgents = useInvalidateAgents()
  const [initialValues] = useState(emptyAgentValues)

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
