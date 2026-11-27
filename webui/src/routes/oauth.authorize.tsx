import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getProfile } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useAuthTransport } from '@/lib/services'
import { OAuthAuthorization } from '@/lib/oauth'
import { useOrgId } from '@/hooks/use-org-id'
import { AgentMonogram } from '@/components/agent-overview'
import { DetailList, DetailRow } from '@/components/detail-list'
import { Button } from '@/components/ui/button'
import { Loader2, ShieldCheck } from 'lucide-react'

export const Route = createFileRoute('/oauth/authorize')({
  component: OAuthAuthorizePage,
})

function OAuthAuthorizePage() {
  const authz = new OAuthAuthorization(new URLSearchParams(window.location.search))

  const { data: profileData, isLoading } = useQuery(getProfile, {})
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const auth = useAuthTransport()

  const profile = profileData?.profile
  const orgs = profileData?.orgs ?? []
  const currentOrgId = useOrgId()
  const currentOrg = orgs.find((o) => String(o.id) === currentOrgId)

  const handleApprove = async () => {
    setError(null)
    setSubmitting(true)
    try {
      const token = await auth.fetchToken()
      window.location.href = await authz.approve(token)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Unknown error')
      setSubmitting(false)
    }
  }

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading...</div>
      </div>
    )
  }

  if (!authz.isValid) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-destructive">Missing required OAuth parameters.</div>
      </div>
    )
  }

  const who = profile?.name || profile?.email || 'Unknown'

  return (
    <div className="mx-auto max-w-md space-y-8 px-6 py-12">
      <header className="space-y-3">
        <div className="flex items-center gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card">
            <ShieldCheck className="size-5" />
          </span>
          <h1 className="min-w-0 truncate text-2xl font-semibold tracking-tight">
            Authorize application
          </h1>
        </div>
        <p className="text-sm text-muted-foreground">
          An application is requesting access to your account.
        </p>
      </header>

      <DetailList>
        <DetailRow label="Signed in as">
          <AgentMonogram name={who} className="size-6 rounded-full text-[10px]" />
          <div className="min-w-0">
            <div className="truncate text-sm font-medium">{who}</div>
            {profile?.email && profile?.name && (
              <div className="truncate text-xs text-muted-foreground">{profile.email}</div>
            )}
          </div>
        </DetailRow>
        {currentOrg && (
          <DetailRow label="Organisation">
            <AgentMonogram name={currentOrg.name} className="size-6 text-[10px]" />
            <div className="truncate text-sm font-medium">{currentOrg.name}</div>
          </DetailRow>
        )}
        {authz.redirectHost && (
          <DetailRow label="Returns to">
            <code className="truncate font-mono text-sm">{authz.redirectHost}</code>
          </DetailRow>
        )}
      </DetailList>

      {error && <div className="text-destructive text-sm">{error}</div>}

      <div className="flex gap-2">
        <Button onClick={handleApprove} disabled={submitting}>
          {submitting && <Loader2 className="h-4 w-4 animate-spin" />}
          Approve
        </Button>
        <Button
          variant="ghost"
          onClick={() => {
            window.location.href = authz.denyRedirectUri
          }}
          disabled={submitting}
        >
          Deny
        </Button>
      </div>
    </div>
  )
}
