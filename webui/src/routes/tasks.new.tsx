import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { createTask } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useOrgId } from '@/hooks/use-org-id'
import { useOrgLocalStorage } from '@/hooks/use-org-local-storage'
import { useTaskSidebarCollapsed } from '@/hooks/use-task-sidebar-collapsed'
import { AgentSelect, useAgent } from '@/components/agent-select'
import { NewTaskSidebar } from '@/components/task-sidebar'
import { Label } from '@/components/ui/label'
import { TaskComposer } from '@/components/task-composer'

// agent and instruction pre-select an agent and pre-fill the first
// instruction, as the Agents list's run button does.
type NewTaskSearch = { agent?: string; instruction?: string }

export const Route = createFileRoute('/tasks/new')({
  // Another org's agent doesn't apply.
  staticData: { orgSwitchRedirect: '/tasks/new' },
  validateSearch: (search: Record<string, unknown>): NewTaskSearch => ({
    agent: typeof search.agent === 'string' ? search.agent : undefined,
    instruction: typeof search.instruction === 'string' ? search.instruction : undefined,
  }),
  component: NewTaskPage,
})

// NewTaskPage is the task page before its task exists: the same sidebar
// and composer, with the agent picker where the timeline will be. Sending
// the first instruction creates the task and swaps in its own page.
function NewTaskPage() {
  const navigate = useNavigate()
  const orgId = useOrgId()
  const [sidebarCollapsed, toggleSidebar] = useTaskSidebarCollapsed()

  const [name, setName] = useState('')
  const search = Route.useSearch()
  const [lastAgentId, setLastAgentId] = useOrgLocalStorage('gritz-last-agent', '')
  // The agent from the URL wins over the remembered one until another is picked.
  const [pickedAgentId, setPickedAgentId] = useState(search.agent)
  const agentId = pickedAgentId ?? lastAgentId
  const setAgentId = (id: string) => {
    setPickedAgentId(undefined)
    setLastAgentId(id)
  }
  // The remembered agent may have been deleted since.
  const agent = useAgent(/^\d+$/.test(agentId) ? BigInt(agentId) : 0n)

  const mutation = useMutation(createTask, {
    onSuccess: (data) => {
      // Replace, so Back from the task doesn't return to an empty new-task page.
      navigate({
        to: '/tasks/$id',
        params: { id: String(data.task!.id) },
        search: { org: orgId },
        replace: true,
      })
    },
  })

  return (
    <div className="relative flex h-full min-h-0">
      <NewTaskSidebar
        name={name}
        onNameChange={setName}
        agent={agent}
        collapsed={sidebarCollapsed}
        onToggleCollapse={toggleSidebar}
      />

      <main className="flex min-w-0 flex-1 flex-col">
        {/* The agent picker takes the place of the timeline the task doesn't have yet. */}
        <div className="flex min-h-0 flex-1 overflow-y-auto p-6">
          <div className="m-auto w-full max-w-md space-y-6">
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
          </div>
        </div>

        <div className="shrink-0 border-t p-4">
          <TaskComposer
            onSend={(text) =>
              mutation.mutateAsync({
                name: name.trim(),
                agentId: agent!.id,
                instructions: [{ text, url: '' }],
              })
            }
            pending={mutation.isPending}
            disabled={!agent}
            initialInstruction={search.instruction}
          />
        </div>
      </main>
    </div>
  )
}
