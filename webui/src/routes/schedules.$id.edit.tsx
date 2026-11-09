import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { createConnectQueryKey, useMutation, useQuery } from '@connectrpc/connect-query'
import { useQueryClient } from '@tanstack/react-query'
import {
  getSchedule,
  setScheduleEnabled,
  updateSchedule,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Schedule } from '@/gen/gritz/v1/gritz_pb'
import { Card, CardContent } from '@/components/ui/card'
import { useOrgId } from '@/hooks/use-org-id'
import { ScheduleForm, type ScheduleFormValues } from '@/components/schedule-form'
import { autoArchiveSelectValue, durationFromAutoArchiveSelect } from '@/lib/duration'

export const Route = createFileRoute('/schedules/$id/edit')({
  component: EditSchedulePage,
})

function scheduleToValues(schedule: Schedule): ScheduleFormValues {
  return {
    name: schedule.name,
    runner: schedule.runner,
    workspace: schedule.workspace,
    namespace: schedule.namespace,
    // A schedule stores a list of instructions but the form edits a single text
    // block (the common case); join any extras with blank lines so nothing is
    // silently dropped on save.
    instruction: schedule.instructions.map((i) => i.text).join('\n\n'),
    cronExpr: schedule.cronExpr,
    timezone: schedule.timezone,
    enabled: schedule.enabled,
    autoArchive: autoArchiveSelectValue(schedule.autoArchive),
  }
}

function EditSchedulePage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const orgId = useOrgId()

  const queryClient = useQueryClient()

  const { data, isLoading, error } = useQuery(getSchedule, { id: BigInt(id) })
  const updateMutation = useMutation(updateSchedule)
  const enabledMutation = useMutation(setScheduleEnabled)

  const schedule = data?.schedule

  const handleSubmit = async (values: ScheduleFormValues) => {
    if (!schedule) return
    await updateMutation.mutateAsync({
      id: schedule.id,
      name: values.name,
      runner: values.runner,
      workspace: values.workspace,
      namespace: values.namespace,
      instructions: [{ text: values.instruction, url: '' }],
      cronExpr: values.cronExpr,
      timezone: values.timezone,
      autoArchive: durationFromAutoArchiveSelect(values.autoArchive),
    })
    // enabled lives on its own RPC (it has distinct side effects: enabling
    // recomputes next_run_at, disabling clears it), so only call it when the
    // toggle actually changed.
    if (values.enabled !== schedule.enabled) {
      await enabledMutation.mutateAsync({ id: schedule.id, enabled: values.enabled })
    }
    // Refresh this schedule's cached read before leaving. Awaiting it here is
    // what makes it take effect: the query still has this page as an observer,
    // so invalidating refetches now and resolves with the saved row in cache.
    // Once we navigate the query goes inactive, and an invalidation then (the
    // SSE notification racing us) only marks it stale — re-opening the form
    // would render the pre-edit copy while the refetch runs, and ScheduleForm
    // seeds its fields from initialValues on mount, so the stale values would
    // stick for the whole visit.
    await queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: getSchedule,
        input: { id: schedule.id },
        cardinality: 'finite',
      }),
    })
    navigate({ to: '/schedules', search: { org: orgId } })
  }

  const handleCancel = () => {
    navigate({ to: '/schedules', search: { org: orgId } })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">Edit Schedule</h1>

      <Card>
        <CardContent>
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : error ? (
            <div className="text-destructive">Error: {error.message}</div>
          ) : !schedule ? (
            <div className="text-muted-foreground">Schedule not found</div>
          ) : (
            <ScheduleForm
              initialValues={scheduleToValues(schedule)}
              submitLabel={
                updateMutation.isPending || enabledMutation.isPending ? 'Saving...' : 'Save Changes'
              }
              isSubmitting={updateMutation.isPending || enabledMutation.isPending}
              error={updateMutation.error?.message ?? enabledMutation.error?.message ?? null}
              onSubmit={handleSubmit}
              onCancel={handleCancel}
            />
          )}
        </CardContent>
      </Card>
    </div>
  )
}
