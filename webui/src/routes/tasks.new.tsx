import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { createTask } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useOrgId } from '@/hooks/use-org-id'
import { useOrgLocalStorage } from '@/hooks/use-org-local-storage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { AgentSelect, useAgent } from '@/components/agent-select'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'

export const Route = createFileRoute('/tasks/new')({
  component: NewTaskPage,
})

function NewTaskPage() {
  const navigate = useNavigate()
  const orgId = useOrgId()

  const [name, setName] = useState('')
  const [agentId, setAgentId] = useOrgLocalStorage('gritz-last-agent', '')
  // The remembered agent may have been deleted since.
  const agent = useAgent(/^\d+$/.test(agentId) ? BigInt(agentId) : 0n)

  const mutation = useMutation(createTask, {
    onSuccess: (data) => {
      if (data.task) {
        navigate({
          to: '/tasks/$id',
          params: { id: String(data.task.id) },
          search: { org: orgId },
        })
      } else {
        navigate({ to: '/tasks', search: { org: orgId } })
      }
    },
  })

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!agent) return

    // Created as a draft: nothing is launched for it. The first instruction is
    // sent from the composer on the task page, which is where every instruction
    // after it goes anyway.
    await mutation.mutateAsync({
      name: name.trim(),
      agentId: agent.id,
      draft: true,
    })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">Create New Task</h1>

      <Card>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-6">
            <div className="space-y-2">
              <Label htmlFor="name">Name (optional)</Label>
              <Input
                id="name"
                placeholder="Enter task name"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="agent">Agent</Label>
              <AgentSelect id="agent" value={agentId} onChange={setAgentId} />
              <p className="text-muted-foreground text-xs">
                The task runs with the agent&apos;s workspace, runner, auto-archive delay and
                variables.
              </p>
            </div>

            {mutation.error && (
              <div className="text-destructive text-sm">Error: {mutation.error.message}</div>
            )}

            <div className="flex gap-2">
              <Button type="submit" disabled={mutation.isPending || !agent}>
                {mutation.isPending ? 'Creating...' : 'Create Task'}
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => navigate({ to: '/tasks', search: { org: orgId } })}
              >
                Cancel
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
