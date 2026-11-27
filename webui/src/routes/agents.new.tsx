import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { createAgent } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Card, CardContent } from '@/components/ui/card'
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

  const handleSubmit = async (values: AgentFormValues) => {
    await mutation.mutateAsync({ agent: buildAgent(values, 0n) })
    await invalidateAgents()
    navigate({ to: '/agents', search: { org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">New Agent</h1>
      <Card>
        <CardContent>
          <AgentForm
            initialValues={emptyAgentValues()}
            submitLabel={mutation.isPending ? 'Creating...' : 'Create Agent'}
            isSubmitting={mutation.isPending}
            error={mutation.error?.message ?? null}
            onSubmit={handleSubmit}
            onCancel={() => navigate({ to: '/agents', search: { org: orgId } })}
          />
        </CardContent>
      </Card>
    </div>
  )
}
