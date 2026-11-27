import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getCustomEventTypes } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
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
  // Bumped after each save, so the form is reseeded from the saved type.
  const [revision, setRevision] = useState(0)

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
    setRevision((r) => r + 1)
  }

  const initialValues = useMemo(
    () => (eventType ? formValuesFromEventTypeDef(eventType) : undefined),
    [eventType],
  )

  if (!initialValues) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading custom event type...</div>
      </div>
    )
  }

  return (
    <CustomEventTypeForm
      key={`${type}-${revision}`}
      initialValues={initialValues}
      typeReadOnly
      submitLabel={mutation.isPending ? 'Saving...' : 'Save changes'}
      isSubmitting={mutation.isPending}
      error={mutation.error?.message ?? null}
      onSubmit={handleSubmit}
    />
  )
}
