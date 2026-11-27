import { createFileRoute, Link } from '@tanstack/react-router'
import { TestEventForm } from '@/components/test-event-form'
import { useOrgId } from '@/hooks/use-org-id'
import { ChevronLeft, FlaskConical } from 'lucide-react'

export const Route = createFileRoute('/agents/test-event')({
  staticData: { orgSwitchRedirect: '/agents/test-event' },
  component: TestEventPage,
})

function TestEventPage() {
  const orgId = useOrgId()

  return (
    <div className="mx-auto max-w-3xl space-y-10 px-6 py-8">
      <header className="space-y-3">
        <Link
          to="/agents"
          search={{ org: orgId }}
          className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronLeft className="h-4 w-4" />
          Agents
        </Link>
        <div className="flex items-center gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card">
            <FlaskConical className="size-5" />
          </span>
          <h1 className="min-w-0 truncate text-2xl font-semibold tracking-tight">Test an event</h1>
        </div>
        <p className="text-sm text-muted-foreground">
          Compose a synthetic event and see what your agents would do with it. Dry run reports each
          agent&apos;s matching trigger and the tasks the event reaches without touching anything;
          firing routes the event for real.
        </p>
      </header>

      <TestEventForm />
    </div>
  )
}
