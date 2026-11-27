import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { deleteAgent } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useOrgId } from '@/hooks/use-org-id'
import { useInvalidateAgents } from '@/lib/agents'
import { Loader2, Trash2 } from 'lucide-react'

// DeleteAgentDialog confirms deleting an agent and deletes it. onDeleted runs
// once the agent is gone, before the cached agents are refetched.
export function DeleteAgentDialog({
  agentId,
  name,
  open,
  onOpenChange,
  onDeleted,
}: {
  agentId: bigint
  name: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onDeleted: () => void | Promise<void>
}) {
  const invalidateAgents = useInvalidateAgents()
  const mutation = useMutation(deleteAgent, {
    onSuccess: async () => {
      await onDeleted()
      await invalidateAgents()
    },
  })

  // A failed delete's error is cleared on close, so reopening starts clean.
  const setOpen = (next: boolean) => {
    if (!next) mutation.reset()
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete agent?</DialogTitle>
          <DialogDescription>
            This deletes the agent “{name}”. Its schedules are deleted with it. An agent can&apos;t
            be deleted while an unarchived task belongs to it: archive its tasks first.
          </DialogDescription>
        </DialogHeader>
        {mutation.error && <div className="text-destructive text-sm">{mutation.error.message}</div>}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            onClick={() => mutation.mutate({ id: agentId })}
            disabled={mutation.isPending}
          >
            {mutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Trash2 className="h-4 w-4" />
            )}
            Delete
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// DeleteAgentAction is the agent page's delete button and its confirmation
// dialog. It returns to the agents list once the agent is gone.
export function DeleteAgentAction({ agentId, name }: { agentId: bigint; name: string }) {
  const orgId = useOrgId()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="text-destructive hover:text-destructive"
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-4 w-4" />
        Delete
      </Button>
      <DeleteAgentDialog
        agentId={agentId}
        name={name}
        open={open}
        onOpenChange={setOpen}
        onDeleted={() => navigate({ to: '/agents', search: { org: orgId } })}
      />
    </>
  )
}
