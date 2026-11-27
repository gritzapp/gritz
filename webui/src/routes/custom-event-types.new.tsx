import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getCustomEventTypes } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
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
    const created = buildEventTypeDef(values)
    await mutation.mutateAsync({ eventTypes: [...eventTypes, created] })
    navigate({
      to: '/custom-event-types/$type',
      params: { type: created.type },
      search: { org: orgId },
    })
  }

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading...</div>
      </div>
    )
  }

  return (
    <CustomEventTypeForm
      initialValues={emptyCustomEventType}
      submitLabel={mutation.isPending ? 'Creating...' : 'Create type'}
      isSubmitting={mutation.isPending}
      error={mutation.error?.message ?? null}
      onSubmit={handleSubmit}
    />
  )
}
