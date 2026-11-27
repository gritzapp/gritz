import { useState } from 'react'
import { useMutation } from '@connectrpc/connect-query'
import { createKey } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Check, Copy, Loader2, Plus } from 'lucide-react'

// NewKeyDialog is the Keys page's "+ Key" button and the dialog it opens: a
// name to create the key with, then the key's token, which is only shown
// there. onCreated runs once the key exists.
export function NewKeyDialog({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [copied, setCopied] = useState(false)
  const mutation = useMutation(createKey, { onSuccess: () => onCreated() })
  const created = mutation.data

  const handleCopy = async (token: string) => {
    await navigator.clipboard.writeText(token)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  // Closing forgets the token, so reopening starts a new key.
  const handleOpenChange = (next: boolean) => {
    setOpen(next)
    if (!next) {
      setName('')
      setCopied(false)
      mutation.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger asChild>
        <Button>
          <Plus className="h-4 w-4" />
          Key
        </Button>
      </DialogTrigger>
      <DialogContent>
        {created ? (
          <>
            <DialogHeader>
              <DialogTitle>{created.key?.name}</DialogTitle>
              <DialogDescription>
                Copy the token now. It is not shown again once this closes.
              </DialogDescription>
            </DialogHeader>
            <code className="block rounded-md border bg-muted/40 px-3 py-2 font-mono text-sm break-all">
              {created.rawToken}
            </code>
            <DialogFooter>
              <Button variant="outline" onClick={() => handleCopy(created.rawToken)}>
                {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                {copied ? 'Copied' : 'Copy token'}
              </Button>
              <Button onClick={() => handleOpenChange(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form
            className="contents"
            onSubmit={(e) => {
              e.preventDefault()
              if (name.trim()) mutation.mutate({ name: name.trim() })
            }}
          >
            <DialogHeader>
              <DialogTitle>New key</DialogTitle>
              <DialogDescription>
                Name the key for what it is for. It is listed, and its actions attributed, under
                that name.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-2">
              <Label htmlFor="key-name">Name</Label>
              <Input
                id="key-name"
                placeholder="ci-pipeline"
                value={name}
                onChange={(e) => setName(e.target.value)}
                autoFocus
                required
              />
              {mutation.error && (
                <div className="text-destructive text-sm">{mutation.error.message}</div>
              )}
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => handleOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={mutation.isPending || !name.trim()}>
                {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
                Create key
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
