import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { createSchedule } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Card, CardContent } from '@/components/ui/card'
import { useOrgId } from '@/hooks/use-org-id'
import {
  ScheduleForm,
  emptyScheduleValues,
  type ScheduleFormValues,
} from '@/components/schedule-form'
import { durationFromAutoArchiveSelect } from '@/lib/duration'

export const Route = createFileRoute('/schedules/new')({
  component: NewSchedulePage,
})

function NewSchedulePage() {
  const navigate = useNavigate()
  const orgId = useOrgId()
  const mutation = useMutation(createSchedule)

  const handleSubmit = async (values: ScheduleFormValues) => {
    await mutation.mutateAsync({
      name: values.name,
      runner: values.runner,
      workspace: values.workspace,
      namespace: values.namespace,
      instructions: [{ text: values.instruction, url: '' }],
      cronExpr: values.cronExpr,
      timezone: values.timezone,
      enabled: values.enabled,
      autoArchive: durationFromAutoArchiveSelect(values.autoArchive),
    })
    navigate({ to: '/schedules', search: { org: orgId } })
  }

  const handleCancel = () => {
    navigate({ to: '/schedules', search: { org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">New Schedule</h1>

      <Card>
        <CardContent>
          <ScheduleForm
            initialValues={emptyScheduleValues()}
            submitLabel={mutation.isPending ? 'Creating...' : 'Create Schedule'}
            isSubmitting={mutation.isPending}
            error={mutation.error?.message ?? null}
            onSubmit={handleSubmit}
            onCancel={handleCancel}
          />
        </CardContent>
      </Card>
    </div>
  )
}
