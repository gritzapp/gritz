import { useEffect, useMemo } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import { getRoutingRules, setRoutingRules } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Card, CardContent } from '@/components/ui/card'
import { useOrgId } from '@/hooks/use-org-id'
import { RoutingRuleForm } from '@/components/routing-rule-form'
import {
  buildRoutingRule,
  formValuesFromRoutingRule,
  type RoutingRuleFormValues,
} from '@/lib/routing-rules'

export const Route = createFileRoute('/routing/$index')({
  component: EditRoutingRulePage,
})

function EditRoutingRulePage() {
  const { index } = Route.useParams()
  const navigate = useNavigate()
  const orgId = useOrgId()
  const { data, isLoading } = useQuery(getRoutingRules, {})
  const mutation = useMutation(setRoutingRules)

  const rules = data?.rules ?? []
  const parsedIndex = Number.parseInt(index, 10)
  const isValidIndex =
    Number.isInteger(parsedIndex) && parsedIndex >= 0 && parsedIndex < rules.length
  const rule = isValidIndex ? rules[parsedIndex] : undefined

  useEffect(() => {
    if (!isLoading && !rule) {
      navigate({ to: '/events', search: { org: orgId }, replace: true })
    }
  }, [isLoading, rule, navigate, orgId])

  const handleSubmit = async (values: RoutingRuleFormValues) => {
    if (!isValidIndex) return
    const updated = rules.map((existing, i) =>
      i === parsedIndex ? buildRoutingRule(values) : existing,
    )
    await mutation.mutateAsync({ rules: updated })
    navigate({ to: '/events', search: { org: orgId } })
  }

  const initialValues = useMemo(() => (rule ? formValuesFromRoutingRule(rule) : undefined), [rule])

  const handleCancel = () => {
    navigate({ to: '/events', search: { org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">Edit Routing Rule</h1>

      <Card>
        <CardContent>
          {isLoading || !initialValues ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : (
            <RoutingRuleForm
              initialValues={initialValues}
              submitLabel={mutation.isPending ? 'Saving...' : 'Save Changes'}
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
