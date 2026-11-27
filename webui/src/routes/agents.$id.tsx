import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import { getAgent, updateAgent } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Card, CardContent } from '@/components/ui/card'
import { AgentForm } from '@/components/agent-form'
import { useOrgId } from '@/hooks/use-org-id'
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
  const navigate = useNavigate()
  const orgId = useOrgId()
  const { data, isLoading, error } = useQuery(getAgent, { id: BigInt(id) })
  const mutation = useMutation(updateAgent)
  const invalidateAgents = useInvalidateAgents()

  const agent = data?.agent

  const handleSubmit = async (values: AgentFormValues) => {
    if (!agent) return
    await mutation.mutateAsync({ agent: buildAgent(values, agent.id) })
    // Refresh the cached agent before leaving, so reopening the form doesn't
    // seed it from the pre-save copy.
    await invalidateAgents()
    navigate({ to: '/agents', search: { org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">{agent ? agent.name : 'Agent'}</h1>
      <Card>
        <CardContent>
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : error ? (
            <div className="text-destructive">Error: {error.message}</div>
          ) : !agent ? (
            <div className="text-muted-foreground">Agent not found</div>
          ) : (
            <AgentForm
              key={String(agent.id)}
              initialValues={agentFormValues(agent)}
              submitLabel={mutation.isPending ? 'Saving...' : 'Save Changes'}
              isSubmitting={mutation.isPending}
              error={mutation.error?.message ?? null}
              onSubmit={handleSubmit}
              onCancel={() => navigate({ to: '/agents', search: { org: orgId } })}
            />
          )}
        </CardContent>
      </Card>
    </div>
  )
}
