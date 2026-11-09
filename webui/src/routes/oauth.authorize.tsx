import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getProfile } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useAuthTransport } from '@/lib/services'
import { OAuthAuthorization } from '@/lib/oauth'
import { useOrgId } from '@/hooks/use-org-id'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'

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
      <div className="container mx-auto py-8 px-4">
        <p className="text-muted-foreground">Loading...</p>
      </div>
    )
  }

  if (!authz.isValid) {
    return (
      <div className="container mx-auto py-8 px-4">
        <Card>
          <CardContent>
            <p className="text-destructive">Missing required OAuth parameters.</p>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="container mx-auto py-8 px-4 max-w-md">
      <h1 className="text-2xl font-bold mb-6">Authorize Application</h1>

      <Card>
        <CardContent className="space-y-4">
          <p className="text-sm text-muted-foreground">
            An application is requesting access to your account.
          </p>

          <div className="space-y-2">
            <div className="text-sm">
              <span className="text-muted-foreground">Signed in as: </span>
              <span className="font-medium">{profile?.name || profile?.email || 'Unknown'}</span>
            </div>
            {profile?.email && profile?.name && (
              <div className="text-sm">
                <span className="text-muted-foreground">Email: </span>
                <span>{profile.email}</span>
              </div>
            )}
            {currentOrg && (
              <div className="text-sm">
                <span className="text-muted-foreground">Organization: </span>
                <span className="font-medium">{currentOrg.name}</span>
              </div>
            )}
          </div>

          {error && <div className="text-destructive text-sm">{error}</div>}

          <div className="flex gap-2 pt-2">
            <Button onClick={handleApprove} disabled={submitting}>
              {submitting ? 'Authorizing...' : 'Approve'}
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                window.location.href = authz.denyRedirectUri
              }}
              disabled={submitting}
            >
              Deny
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
