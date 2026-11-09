import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { TestEventForm } from '@/components/test-event-form'
import { useOrgId } from '@/hooks/use-org-id'
import { ArrowLeft } from 'lucide-react'

export const Route = createFileRoute('/routing/testevent')({
  staticData: { orgSwitchRedirect: '/routing/testevent' },
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
          onClick={() => navigate({ to: '/events', search: { org: orgId } })}
        >
          <ArrowLeft className="h-4 w-4" />
          Back
        </Button>
        <h1 className="text-2xl font-bold">Test a Routing Rule</h1>
      </div>
      <p className="text-muted-foreground max-w-2xl">
        Compose a synthetic event and see what your routing rules would do with it. Dry run reports
        which rule matches without touching anything; firing routes the event for real.
      </p>

      <Card>
        <CardContent>
          <TestEventForm />
        </CardContent>
      </Card>
    </div>
  )
}
