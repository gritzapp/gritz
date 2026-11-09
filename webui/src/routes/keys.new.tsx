import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { createKey } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { CreateKeyResponse } from '@/gen/gritz/v1/gritz_pb'
import { useOrgId } from '@/hooks/use-org-id'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Copy, Check } from 'lucide-react'

export const Route = createFileRoute('/keys/new')({
  component: NewKeyPage,
})

function NewKeyPage() {
  const navigate = useNavigate()
  const orgId = useOrgId()
  const [name, setName] = useState('')
  const [created, setCreated] = useState<CreateKeyResponse | null>(null)
  const [copied, setCopied] = useState(false)

  const mutation = useMutation(createKey, {
    onSuccess: (data) => {
      setCreated(data)
    },
  })

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    await mutation.mutateAsync({ name: name.trim() })
  }

  const handleCopy = async () => {
    if (!created?.rawToken) return
    await navigator.clipboard.writeText(created.rawToken)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  if (created) {
    return (
      <div className="container mx-auto py-8 px-4 space-y-6">
        <h1 className="text-2xl font-bold mb-6">API Key Created</h1>

        <Card>
          <CardContent className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Your API key has been created. Copy the token below — it will not be shown again.
            </p>

            <div className="space-y-2">
              <Label>Name</Label>
              <div className="text-sm">{created.key?.name}</div>
            </div>

            <div className="space-y-2">
              <Label>Token</Label>
              <div className="flex items-center gap-2">
                <code className="text-sm bg-muted px-3 py-2 rounded flex-1 break-all">
                  {created.rawToken}
                </code>
                <Button variant="ghost" size="icon" onClick={handleCopy}>
                  {copied ? (
                    <Check className="h-4 w-4 text-green-500" />
                  ) : (
                    <Copy className="h-4 w-4" />
                  )}
                </Button>
              </div>
            </div>

            <div className="pt-2">
              <Button onClick={() => navigate({ to: '/keys', search: { org: orgId } })}>
                Done
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="container mx-auto py-8 px-4 space-y-6">
      <h1 className="text-2xl font-bold mb-6">Create API Key</h1>

      <Card>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-6">
            <div className="space-y-2">
              <Label htmlFor="name">Name</Label>
              <Input
                id="name"
                placeholder="e.g. CI Pipeline"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </div>

            {mutation.error && (
              <div className="text-destructive text-sm">Error: {mutation.error.message}</div>
            )}

            <div className="flex gap-2">
              <Button type="submit" disabled={mutation.isPending}>
                {mutation.isPending ? 'Creating...' : 'Create Key'}
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => navigate({ to: '/keys', search: { org: orgId } })}
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
