import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import { ConnectError, Code } from '@connectrpc/connect'
import { getProfile, linkGitHubInstallation } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useOrgId } from '@/hooks/use-org-id'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'

export const Route = createFileRoute('/github/setup')({
  component: GitHubSetupPage,
})

function GitHubSetupPage() {
  const installationIdParam = new URLSearchParams(window.location.search).get('installation_id')
  const installationId = installationIdParam ? BigInt(installationIdParam) : null

  const { data: profileData, isLoading } = useQuery(getProfile, {})
  const profile = profileData?.profile
  const githubAccount = profileData?.githubAccount
  const orgs = profileData?.orgs ?? []
  const currentOrgId = useOrgId()
  const currentOrg = orgs.find((o) => String(o.id) === currentOrgId)

  const [error, setError] = useState<string | null>(null)
  const [needsGitHubLink, setNeedsGitHubLink] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  const mutation = useMutation(linkGitHubInstallation)

  const handleLink = async () => {
    if (!installationId) return
    setError(null)
    setNeedsGitHubLink(false)
    setSubmitting(true)
    try {
      await mutation.mutateAsync({ installationId })
      window.location.href = '/ui/settings?tab=organisation'
    } catch (e) {
      if (e instanceof ConnectError && e.code === Code.FailedPrecondition) {
        setNeedsGitHubLink(true)
      } else {
        setError(e instanceof Error ? e.message : 'Unknown error')
      }
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

  if (!installationId) {
    return (
      <div className="container mx-auto py-8 px-4 max-w-md">
        <Card>
          <CardContent>
            <p className="text-destructive">Missing installation_id query parameter.</p>
          </CardContent>
        </Card>
      </div>
    )
  }

  const showGitHubLinkPrompt = needsGitHubLink || !githubAccount

  return (
    <div className="container mx-auto py-8 px-4 max-w-md">
      <h1 className="text-2xl font-bold mb-6">Link GitHub App Installation</h1>

      <Card>
        <CardContent className="space-y-4">
          <p className="text-sm text-muted-foreground">
            Link this GitHub App installation to your organisation.
          </p>

          <div className="space-y-2">
            <div className="text-sm">
              <span className="text-muted-foreground">Signed in as: </span>
              <span className="font-medium">{profile?.name || profile?.email || 'Unknown'}</span>
            </div>
            {githubAccount && (
              <div className="text-sm">
                <span className="text-muted-foreground">GitHub account: </span>
                <span className="font-medium">{githubAccount.githubUsername}</span>
              </div>
            )}
            {currentOrg && (
              <div className="text-sm">
                <span className="text-muted-foreground">Organisation: </span>
                <span className="font-medium">{currentOrg.name}</span>
              </div>
            )}
          </div>

          {showGitHubLinkPrompt && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm space-y-2">
              <p className="text-destructive">
                You need to link your GitHub account before completing this step.
              </p>
              <a href="/github/login" className="inline-block">
                <Button variant="outline" size="sm">
                  Link GitHub Account
                </Button>
              </a>
            </div>
          )}

          {error && <div className="text-destructive text-sm">{error}</div>}

          <div className="flex gap-2 pt-2">
            <Button onClick={handleLink} disabled={submitting || showGitHubLinkPrompt}>
              {submitting ? 'Linking...' : 'Approve'}
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                window.location.href = '/ui/settings?tab=organisation'
              }}
              disabled={submitting}
            >
              Cancel
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
