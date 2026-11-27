import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import { ConnectError, Code } from '@connectrpc/connect'
import { getProfile, linkGitHubInstallation } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useOrgId } from '@/hooks/use-org-id'
import { AgentMonogram } from '@/components/agent-overview'
import { DetailList, DetailRow } from '@/components/detail-list'
import { GithubIcon } from '@/components/github-icon'
import { Button } from '@/components/ui/button'
import { Link2, Loader2, TriangleAlert } from 'lucide-react'

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
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading...</div>
      </div>
    )
  }

  if (!installationId) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-destructive">Missing installation_id query parameter.</div>
      </div>
    )
  }

  const showGitHubLinkPrompt = needsGitHubLink || !githubAccount
  const who = profile?.name || profile?.email || 'Unknown'

  return (
    <div className="mx-auto max-w-md space-y-8 px-6 py-12">
      <header className="space-y-3">
        <div className="flex items-center gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card">
            <GithubIcon className="size-5" />
          </span>
          <h1 className="min-w-0 truncate text-2xl font-semibold tracking-tight">
            Link GitHub App
          </h1>
        </div>
        <p className="text-sm text-muted-foreground">
          Link this GitHub App installation to your organisation.
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
        <DetailRow label="GitHub account">
          {githubAccount ? (
            <>
              <GithubIcon className="size-4 shrink-0" />
              <div className="truncate text-sm font-medium">{githubAccount.githubUsername}</div>
            </>
          ) : (
            <div className="text-sm text-muted-foreground">Not linked</div>
          )}
        </DetailRow>
        {currentOrg && (
          <DetailRow label="Organisation">
            <AgentMonogram name={currentOrg.name} className="size-6 text-[10px]" />
            <div className="truncate text-sm font-medium">{currentOrg.name}</div>
          </DetailRow>
        )}
        <DetailRow label="Installation">
          <code className="truncate font-mono text-sm">{String(installationId)}</code>
        </DetailRow>
      </DetailList>

      {showGitHubLinkPrompt && (
        <div className="flex items-start gap-2.5 rounded-md border border-l-2 border-l-amber-500 bg-muted/40 px-4 py-3 text-sm dark:border-l-amber-400">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <div className="space-y-2">
            <p>You need to link your GitHub account before completing this step.</p>
            <Button asChild variant="outline" size="sm">
              <a href="/github/login">
                <Link2 className="h-4 w-4" />
                Link GitHub account
              </a>
            </Button>
          </div>
        </div>
      )}

      {error && <div className="text-destructive text-sm">{error}</div>}

      <div className="flex gap-2">
        <Button onClick={handleLink} disabled={submitting || showGitHubLinkPrompt}>
          {submitting && <Loader2 className="h-4 w-4 animate-spin" />}
          Approve
        </Button>
        <Button
          variant="ghost"
          onClick={() => {
            window.location.href = '/ui/settings?tab=organisation'
          }}
          disabled={submitting}
        >
          Cancel
        </Button>
      </div>
    </div>
  )
}
