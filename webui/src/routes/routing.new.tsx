import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import { getRoutingRules, setRoutingRules } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Card, CardContent } from '@/components/ui/card'
import { useOrgId } from '@/hooks/use-org-id'
import { RoutingRuleForm } from '@/components/routing-rule-form'
import { buildRoutingRule, emptyRoutingRule, type RoutingRuleFormValues } from '@/lib/routing-rules'

export const Route = createFileRoute('/routing/new')({
  component: NewRoutingRulePage,
})

function NewRoutingRulePage() {
  const navigate = useNavigate()
  const orgId = useOrgId()
  const { data, isLoading } = useQuery(getRoutingRules, {})
  const mutation = useMutation(setRoutingRules)

  const handleSubmit = async (values: RoutingRuleFormValues) => {
    const rules = data?.rules ?? []
    const newRule = buildRoutingRule(values)
    await mutation.mutateAsync({ rules: [...rules, newRule] })
    navigate({ to: '/events', search: { org: orgId } })
  }

  const handleCancel = () => {
    navigate({ to: '/events', search: { org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">New Routing Rule</h1>

      <Card>
        <CardContent>
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : (
            <RoutingRuleForm
              initialValues={emptyRoutingRule}
              submitLabel={mutation.isPending ? 'Saving...' : 'Create Rule'}
              isSubmitting={mutation.isPending}
              error={mutation.error?.message ?? null}
              onSubmit={handleSubmit}
              onCancel={handleCancel}
            />
          )}
        </CardContent>
      </Card>
    </div>
  )
}
