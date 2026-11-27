import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { TestEventForm } from '@/components/test-event-form'
import { useOrgId } from '@/hooks/use-org-id'
import { ArrowLeft } from 'lucide-react'

export const Route = createFileRoute('/agents/test-event')({
  staticData: { orgSwitchRedirect: '/agents/test-event' },
  component: TestEventPage,
})

function TestEventPage() {
  const navigate = useNavigate()
  const orgId = useOrgId()

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <div className="flex items-center gap-3">
        <Button
          variant="outline"
          size="sm"
          onClick={() => navigate({ to: '/agents', search: { org: orgId } })}
        >
          <ArrowLeft className="h-4 w-4" />
          Back
        </Button>
        <h1 className="text-2xl font-bold">Test an Event</h1>
      </div>
      <p className="text-muted-foreground max-w-2xl">
        Compose a synthetic event and see what your agents would do with it. Dry run reports each
        agent&apos;s matching trigger and the tasks the event reaches without touching anything;
        firing routes the event for real.
      </p>

      <Card>
        <CardContent>
          <TestEventForm />
        </CardContent>
      </Card>
    </div>
  )
}
