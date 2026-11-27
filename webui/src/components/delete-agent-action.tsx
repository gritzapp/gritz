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

// DeleteAgentAction is the agent page's delete button and its confirmation
// dialog. It returns to the agents list once the agent is gone.
export function DeleteAgentAction({ agentId, name }: { agentId: bigint; name: string }) {
  const orgId = useOrgId()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const invalidateAgents = useInvalidateAgents()
  const mutation = useMutation(deleteAgent, {
    onSuccess: async () => {
      await navigate({ to: '/agents', search: { org: orgId } })
      await invalidateAgents()
    },
  })

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="text-destructive hover:text-destructive"
        onClick={() => {
          mutation.reset()
          setOpen(true)
        }}
      >
        <Trash2 className="h-4 w-4" />
        Delete
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete agent?</DialogTitle>
            <DialogDescription>
              This deletes the agent “{name}”. An agent can&apos;t be deleted while an unarchived
              task or a schedule belongs to it: archive its tasks and repoint or delete its
              schedules first.
            </DialogDescription>
          </DialogHeader>
          {mutation.error && (
            <div className="text-destructive text-sm">{mutation.error.message}</div>
          )}
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
    </>
  )
}
