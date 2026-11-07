import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getCustomEventTypes } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Card, CardContent } from '@/components/ui/card'
import { useOrgId } from '@/hooks/use-org-id'
import { CustomEventTypeForm } from '@/components/custom-event-type-form'
import {
  buildEventTypeDef,
  emptyCustomEventType,
  useSetCustomEventTypes,
  type CustomEventTypeFormValues,
} from '@/lib/custom-event-types'

export const Route = createFileRoute('/custom-event-types/new')({
  component: NewCustomEventTypePage,
})

function NewCustomEventTypePage() {
  const navigate = useNavigate()
  const orgId = useOrgId()
  const { data, isLoading } = useQuery(getCustomEventTypes, {})
  const mutation = useSetCustomEventTypes()

  const handleSubmit = async (values: CustomEventTypeFormValues) => {
    const eventTypes = data?.eventTypes ?? []
    await mutation.mutateAsync({ eventTypes: [...eventTypes, buildEventTypeDef(values)] })
    navigate({ to: '/events', search: { tab: 'schemas', org: orgId } })
  }

  const handleCancel = () => {
    navigate({ to: '/events', search: { tab: 'schemas', org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">New Custom Event Type</h1>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : (
            <CustomEventTypeForm
              initialValues={emptyCustomEventType}
              submitLabel={mutation.isPending ? 'Saving...' : 'Create Type'}
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
