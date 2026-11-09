import { useEffect, useMemo } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getCustomEventTypes } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Card, CardContent } from '@/components/ui/card'
import { useOrgId } from '@/hooks/use-org-id'
import { CustomEventTypeForm } from '@/components/custom-event-type-form'
import {
  buildEventTypeDef,
  formValuesFromEventTypeDef,
  useSetCustomEventTypes,
  type CustomEventTypeFormValues,
} from '@/lib/custom-event-types'

export const Route = createFileRoute('/custom-event-types/$type')({
  component: EditCustomEventTypePage,
})

function EditCustomEventTypePage() {
  const { type } = Route.useParams()
  const navigate = useNavigate()
  const orgId = useOrgId()
  const { data, isLoading } = useQuery(getCustomEventTypes, {})
  const mutation = useSetCustomEventTypes()

  const eventTypes = data?.eventTypes ?? []
  const eventType = eventTypes.find((t) => t.type === type)

  useEffect(() => {
    if (!isLoading && !eventType) {
      navigate({ to: '/events', search: { tab: 'schemas', org: orgId }, replace: true })
    }
  }, [isLoading, eventType, navigate, orgId])

  const handleSubmit = async (values: CustomEventTypeFormValues) => {
    const updated = eventTypes.map((existing) =>
      existing.type === type ? buildEventTypeDef(values) : existing,
    )
    await mutation.mutateAsync({ eventTypes: updated })
    navigate({ to: '/events', search: { tab: 'schemas', org: orgId } })
  }

  const initialValues = useMemo(
    () => (eventType ? formValuesFromEventTypeDef(eventType) : undefined),
    [eventType],
  )

  const handleCancel = () => {
    navigate({ to: '/events', search: { tab: 'schemas', org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">Edit Custom Event Type</h1>

      <Card>
        <CardContent>
          {isLoading || !initialValues ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : (
            <CustomEventTypeForm
              initialValues={initialValues}
              typeReadOnly
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
