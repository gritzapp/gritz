import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import { createTask, listWorkspaces } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useOrgId } from '@/hooks/use-org-id'
import { useOrgLocalStorage } from '@/hooks/use-org-local-storage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { durationFromHours } from '@/lib/duration'

export const Route = createFileRoute('/tasks/new')({
  component: NewTaskPage,
})

function NewTaskPage() {
  const navigate = useNavigate()
  const orgId = useOrgId()

  const [name, setName] = useState('')
  const [runner, setRunner] = useOrgLocalStorage('gritz-last-runner', '')
  const [workspace, setWorkspace] = useOrgLocalStorage('gritz-last-workspace', '')
  const [namespace, setNamespace] = useState('')
  const [autoArchive, setAutoArchive] = useState<string>('') // empty = never

  const { data: workspacesData } = useQuery(listWorkspaces, {})

  // Derive unique runners from workspaces
  const runners = [...new Set(workspacesData?.workspaces.map((ws) => ws.runnerId) ?? [])]

  // Filter workspaces by selected runner
  const workspaces = workspacesData?.workspaces.filter((ws) => ws.runnerId === runner) ?? []

  const handleRunnerChange = (newRunner: string) => {
    setRunner(newRunner)
    setWorkspace('')
  }

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
    if (!runner.trim() || !workspace.trim()) return

    // No instructions: the task is created empty (completed, with no command)
    // and nothing is launched for it. The first instruction is sent from the
    // composer on the task page, which is where every instruction after it goes
    // anyway. See proposals/draft/create-empty-task.md.
    await mutation.mutateAsync({
      name: name.trim(),
      runner: runner.trim(),
      workspace: workspace.trim(),
      namespace: namespace.trim(),
      autoArchive: durationFromHours(autoArchive),
    })
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">Create New Task</h1>

      <Card>
        <CardContent className="pt-6">
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
              <Label htmlFor="runner">Runner</Label>
              <Select value={runner} onValueChange={handleRunnerChange} required>
                <SelectTrigger id="runner">
                  <SelectValue placeholder="Select a runner" />
                </SelectTrigger>
                <SelectContent>
                  {runners.map((r) => (
                    <SelectItem key={r} value={r}>
                      {r}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="workspace">Workspace</Label>
              <Select value={workspace} onValueChange={setWorkspace} required disabled={!runner}>
                <SelectTrigger id="workspace">
                  <SelectValue
                    placeholder={runner ? 'Select a workspace' : 'Select a runner first'}
                  />
                </SelectTrigger>
                <SelectContent>
                  {workspaces.map((ws) => (
                    <SelectItem key={ws.name} value={ws.name}>
                      <span>{ws.name}</span>
                      {ws.description && (
                        <span className="text-muted-foreground text-xs ml-2">{ws.description}</span>
                      )}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="namespace">Namespace (optional)</Label>
              <Input
                id="namespace"
                placeholder="Default namespace"
                value={namespace}
                onChange={(e) => setNamespace(e.target.value)}
              />
              <p className="text-muted-foreground text-xs">
                Partitions subscription matching for routing rules. Leave empty for the default
                namespace.
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="auto-archive">Auto-archive (optional)</Label>
              <Select value={autoArchive} onValueChange={setAutoArchive}>
                <SelectTrigger id="auto-archive">
                  <SelectValue placeholder="Never (default)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="never">Never</SelectItem>
                  <SelectItem value="1">1 hour</SelectItem>
                  <SelectItem value="24">24 hours</SelectItem>
                  <SelectItem value="168">7 days</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-xs">
                Once the task reaches a terminal status (completed, failed, cancelled), the server
                will archive it after this delay so the container is reclaimed.
              </p>
            </div>

            {mutation.error && (
              <div className="text-destructive text-sm">Error: {mutation.error.message}</div>
            )}

            <div className="flex gap-2">
              <Button type="submit" disabled={mutation.isPending}>
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
