import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import {
  getProfile,
  ping,
  unlinkGitHubAccount,
  unlinkAtlassianAccount,
  createOrg,
  deleteOrg,
  getOrgSettings,
  generateAtlassianWebhookSecret,
  linkGitHubInstallation,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Org } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { RelativeTime } from '@/components/relative-time'
import { CopyButton } from '@/components/copy-button'
import { GithubIcon } from '@/components/github-icon'
import { AtlassianIcon } from '@/components/atlassian-icon'
import {
  Cable,
  Check,
  Copy,
  ExternalLink,
  KeyRound,
  Link2,
  Loader2,
  Mail,
  Plus,
  RefreshCw,
  Trash2,
  Unlink,
  User,
  Webhook,
} from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'

type SettingsTab = 'account' | 'organisation'

function toSettingsTab(value: unknown): SettingsTab {
  if (value === 'organisation') return value
  return 'account'
}

export const Route = createFileRoute('/settings')({
  component: SettingsPage,
  validateSearch: (search: Record<string, unknown>): { tab: SettingsTab } => ({
    tab: toSettingsTab(search.tab),
  }),
})

function SettingsPage() {
  const { tab } = Route.useSearch()
  const orgId = useOrgId()
  const navigate = useNavigate()

  return (
    <div className="container mx-auto py-8 px-4">
      <h1 className="text-2xl font-bold mb-6">Settings</h1>
      <Tabs
        value={tab}
        onValueChange={(value) =>
          navigate({
            to: '/settings',
            search: { tab: toSettingsTab(value), org: orgId },
            replace: true,
          })
        }
      >
        <div className="flex items-center mb-4">
          <ProfileCard />
          <TabsList className="ml-auto">
            <TabsTrigger value="account">Account</TabsTrigger>
            <TabsTrigger value="organisation">Organisation</TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="account">
          <AccountSettings />
        </TabsContent>
        <TabsContent value="organisation">
          <OrgSettings />
        </TabsContent>
      </Tabs>
      <VersionFooter />
    </div>
  )
}

function VersionFooter() {
  const { data } = useQuery(ping, {}, { staleTime: Infinity })
  if (!data?.version) return null
  return <div className="mt-6 text-right text-xs text-muted-foreground">{data.version}</div>
}

function AccountSettings() {
  const { data, isLoading, refetch } = useQuery(getProfile, {})
  const unlinkMutation = useMutation(unlinkGitHubAccount, {
    onSuccess: () => refetch(),
  })

  const account = data?.githubAccount

  return (
    <div className="space-y-6">
      <OrgsCard />
      <AtlassianAccountCard />
      <Card>
        <CardHeader>
          <CardTitle>GitHub Account</CardTitle>
          <CardDescription>
            Link your GitHub account to receive webhook notifications for your tasks.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : account ? (
            <div className="flex items-center gap-4">
              <div className="flex items-center gap-2">
                <GithubIcon className="h-5 w-5" />
                <span className="font-medium">{account.githubUsername}</span>
              </div>
              <Button
                variant="outline"
                size="sm"
                onClick={() => unlinkMutation.mutateAsync({})}
                disabled={unlinkMutation.isPending}
              >
                {unlinkMutation.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <Unlink className="h-4 w-4" />
                )}
                Unlink
              </Button>
            </div>
          ) : (
            <a href="/github/login">
              <Button>
                <GithubIcon className="h-4 w-4" />
                Link GitHub Account
              </Button>
            </a>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function OrgSettings() {
  const orgId = useOrgId()
  const { data, isLoading, refetch } = useQuery(getOrgSettings, {})
  const generateMutation = useMutation(generateAtlassianWebhookSecret, {
    onSuccess: () => refetch(),
  })
  const [copied, setCopied] = useState<'secret' | 'url' | null>(null)

  const copyToClipboard = (text: string, field: 'secret' | 'url') => {
    navigator.clipboard.writeText(text)
    setCopied(field)
    setTimeout(() => setCopied(null), 2000)
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>MCP Server</CardTitle>
          <CardDescription>
            gritz provides an MCP server that you can connect to from any MCP-compatible client.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : data?.mcpUrl ? (
            <div className="flex items-center gap-2">
              <Cable className="h-5 w-5 text-muted-foreground" />
              <code className="text-sm bg-muted px-2 py-1 rounded">{data.mcpUrl}</code>
            </div>
          ) : null}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Custom Events</CardTitle>
          <CardDescription>
            Your own systems can submit events of the org's custom event types to this webhook URL.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : data?.customWebhookUrl ? (
            <>
              <div className="flex items-center gap-2">
                <Webhook className="h-5 w-5 text-muted-foreground" />
                <code className="text-sm bg-muted px-2 py-1 rounded">{data.customWebhookUrl}</code>
                <CopyButton text={data.customWebhookUrl} label="Copy webhook URL" />
              </div>
              <p className="text-sm text-muted-foreground">
                Senders POST with <code>Authorization: Bearer &lt;api key&gt;</code>, using a key
                from the{' '}
                <Link to="/keys" search={{ org: orgId }} className="underline">
                  Keys
                </Link>{' '}
                page.
              </p>
            </>
          ) : null}
        </CardContent>
      </Card>
      {data?.githubAppUrl && (
        <Card>
          <CardHeader>
            <CardTitle>GitHub App</CardTitle>
            <CardDescription>
              Install the GitHub App to receive webhook notifications for pull requests on your
              tasks.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {data.githubInstallationId ? (
              <div className="flex items-center gap-4">
                <div className="flex items-center gap-2">
                  <Check className="h-5 w-5 text-green-500" />
                  <span className="font-medium">Installed</span>
                </div>
                <a
                  href={`https://github.com/settings/installations/${data.githubInstallationId}`}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  <Button variant="outline" size="sm">
                    <ExternalLink className="h-4 w-4" />
                    Manage
                  </Button>
                </a>
              </div>
            ) : (
              <GitHubAppNotInstalled appUrl={data.githubAppUrl} onLinked={refetch} />
            )}
          </CardContent>
        </Card>
      )}
      <Card>
        <CardHeader>
          <CardTitle>Atlassian Webhook</CardTitle>
          <CardDescription>
            Configure a webhook secret to receive Atlassian events (e.g. Jira issue comments) for
            your tasks. Register this webhook URL in your Atlassian admin settings.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {isLoading ? (
            <div className="text-muted-foreground">Loading...</div>
          ) : (
            <>
              <div className="space-y-3">
                <div>
                  <label className="text-sm font-medium">Webhook URL</label>
                  <div className="flex items-center gap-2 mt-1">
                    <code className="text-sm bg-muted px-2 py-1 rounded flex-1 truncate">
                      {data?.atlassianWebhookUrl || '—'}
                    </code>
                    {data?.atlassianWebhookUrl && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => copyToClipboard(data.atlassianWebhookUrl, 'url')}
                      >
                        {copied === 'url' ? (
                          <Check className="h-4 w-4" />
                        ) : (
                          <Copy className="h-4 w-4" />
                        )}
                      </Button>
                    )}
                  </div>
                </div>
                <div>
                  <label className="text-sm font-medium">Secret</label>
                  <div className="flex items-center gap-2 mt-1">
                    {data?.atlassianWebhookSecret ? (
                      <>
                        <code className="text-sm bg-muted px-2 py-1 rounded flex-1 truncate">
                          {data.atlassianWebhookSecret.slice(0, 8)}
                          {'•'.repeat(24)}
                        </code>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => copyToClipboard(data.atlassianWebhookSecret, 'secret')}
                        >
                          {copied === 'secret' ? (
                            <Check className="h-4 w-4" />
                          ) : (
                            <Copy className="h-4 w-4" />
                          )}
                        </Button>
                      </>
                    ) : (
                      <span className="text-sm text-muted-foreground">No secret configured</span>
                    )}
                  </div>
                </div>
              </div>
              <Button
                onClick={() => generateMutation.mutateAsync({})}
                disabled={generateMutation.isPending}
              >
                {generateMutation.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : data?.atlassianWebhookSecret ? (
                  <RefreshCw className="h-4 w-4" />
                ) : (
                  <KeyRound className="h-4 w-4" />
                )}
                {data?.atlassianWebhookSecret ? 'Regenerate Secret' : 'Generate Secret'}
              </Button>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function GitHubAppNotInstalled({ appUrl, onLinked }: { appUrl: string; onLinked: () => void }) {
  const [open, setOpen] = useState(false)

  return (
    <div className="flex items-center gap-2">
      <a href={appUrl} target="_blank" rel="noopener noreferrer">
        <Button variant="outline">
          <ExternalLink className="h-4 w-4" />
          Install GitHub App
        </Button>
      </a>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger asChild>
          <Button variant="outline">
            <Link2 className="h-4 w-4" />
            Link existing installation
          </Button>
        </DialogTrigger>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Link existing installation</DialogTitle>
            <DialogDescription>
              Link a GitHub App installation that's already installed on your organisation.
            </DialogDescription>
          </DialogHeader>
          <LinkInstallationForm
            onLinked={() => {
              setOpen(false)
              onLinked()
            }}
          />
        </DialogContent>
      </Dialog>
    </div>
  )
}

function LinkInstallationForm({ onLinked }: { onLinked: () => void }) {
  const [value, setValue] = useState('')
  const mutation = useMutation(linkGitHubInstallation, {
    onSuccess: () => {
      setValue('')
      onLinked()
    },
  })

  const valid = /^\d+$/.test(value.trim())

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    await mutation.mutateAsync({ installationId: BigInt(value.trim()) })
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-2">
      <div className="flex gap-2">
        <Input
          type="text"
          inputMode="numeric"
          placeholder="Installation ID"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          className="max-w-xs"
        />
        <Button type="submit" disabled={mutation.isPending || !valid}>
          {mutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Link2 className="h-4 w-4" />
          )}
          Link
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        Find the ID in the URL of your installation settings on GitHub:{' '}
        <code className="bg-muted px-1 py-0.5 rounded">
          github.com/settings/installations/&lt;id&gt;
        </code>
      </p>
      {mutation.error && <p className="text-destructive text-sm">{mutation.error.message}</p>}
    </form>
  )
}

function ProfileCard() {
  const { data: profileData } = useQuery(getProfile, {})
  const profile = profileData?.profile

  if (!profile) return null

  return (
    <div className="flex items-center gap-4 text-sm">
      <div className="flex items-center gap-1.5">
        <User className="h-4 w-4 text-muted-foreground" />
        <span className="font-medium">{profile.name}</span>
      </div>
      <div className="hidden md:flex items-center gap-1.5 text-muted-foreground">
        <Mail className="h-4 w-4" />
        <span>{profile.email}</span>
      </div>
    </div>
  )
}

function OrgsCard() {
  const { data: profileData, refetch } = useQuery(getProfile, {})
  const userId = profileData?.profile?.id
  const orgs = (profileData?.orgs ?? []).filter((org) => org.owner === userId)

  return (
    <Card>
      <CardHeader>
        <CardTitle>My Organisations</CardTitle>
        <CardDescription>Create and manage your organisations.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <CreateOrgForm onCreate={refetch} />
        {orgs.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Created</TableHead>
                <TableHead></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {orgs.map((org) => (
                <OrgRow
                  key={String(org.id)}
                  org={org}
                  onDelete={refetch}
                  isDefault={org.id === profileData?.defaultOrgId}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

function CreateOrgForm({ onCreate }: { onCreate: () => void }) {
  const [name, setName] = useState('')
  const mutation = useMutation(createOrg, {
    onSuccess: () => {
      setName('')
      onCreate()
    },
  })

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    await mutation.mutateAsync({ name: name.trim() })
  }

  return (
    <form onSubmit={handleSubmit} className="flex gap-2">
      <Input
        type="text"
        placeholder="Organisation name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="max-w-sm"
      />
      <Button type="submit" disabled={mutation.isPending || !name.trim()}>
        {mutation.isPending ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : (
          <Plus className="h-4 w-4" />
        )}
        Create
      </Button>
      {mutation.error && (
        <span className="text-destructive text-sm self-center">{mutation.error.message}</span>
      )}
    </form>
  )
}

function OrgRow({
  org,
  onDelete,
  isDefault,
}: {
  org: Org
  onDelete: () => void
  isDefault: boolean
}) {
  const deleteMutation = useMutation(deleteOrg, {
    onSuccess: () => onDelete(),
  })

  return (
    <TableRow>
      <TableCell className="text-muted-foreground">{String(org.id)}</TableCell>
      <TableCell className="font-medium">{org.name}</TableCell>
      <TableCell className="text-muted-foreground">
        {org.createdAt ? <RelativeTime date={timestampDate(org.createdAt)} /> : '-'}
      </TableCell>
      <TableCell>
        <Button
          variant="destructive"
          size="sm"
          onClick={() => deleteMutation.mutateAsync({ id: org.id })}
          disabled={deleteMutation.isPending || isDefault}
        >
          {deleteMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Trash2 className="h-4 w-4" />
          )}
          Delete
        </Button>
      </TableCell>
    </TableRow>
  )
}

function AtlassianAccountCard() {
  const { data, isLoading, refetch } = useQuery(getProfile, {})
  const unlinkMutation = useMutation(unlinkAtlassianAccount, {
    onSuccess: () => refetch(),
  })

  const account = data?.atlassianAccount

  return (
    <Card>
      <CardHeader>
        <CardTitle>Atlassian Account</CardTitle>
        <CardDescription>
          Link your Atlassian account to receive notifications for Jira issues on your tasks.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="text-muted-foreground">Loading...</div>
        ) : account ? (
          <div className="flex items-center gap-4">
            <div className="flex items-center gap-2">
              <AtlassianIcon className="h-5 w-5" />
              <span className="font-medium">
                {account.atlassianUsername || account.atlassianAccountId}
              </span>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => unlinkMutation.mutateAsync({})}
              disabled={unlinkMutation.isPending}
            >
              {unlinkMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Unlink className="h-4 w-4" />
              )}
              Unlink
            </Button>
          </div>
        ) : (
          <a href="/atlassian/login">
            <Button>
              <AtlassianIcon className="h-4 w-4" />
              Link Atlassian Account
            </Button>
          </a>
        )}
      </CardContent>
    </Card>
  )
}
