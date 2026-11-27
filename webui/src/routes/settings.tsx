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
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { AgentMonogram, SectionLabel } from '@/components/agent-overview'
import { SectionHeading } from '@/components/form-layout'
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
  Building2,
  Ellipsis,
  ExternalLink,
  KeyRound,
  Link2,
  Loader2,
  Plus,
  RefreshCw,
  Trash2,
  Unlink,
  User,
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
    <div className="mx-auto max-w-3xl px-6 py-8">
      <Tabs
        value={tab}
        onValueChange={(value) =>
          navigate({
            to: '/settings',
            search: { tab: toSettingsTab(value), org: orgId },
            replace: true,
          })
        }
        className="gap-10"
      >
        <header className="flex flex-wrap items-center gap-3">
          <ProfileHeader />
          <TabsList className="ml-auto">
            <TabsTrigger value="account" className="px-2.5">
              <User />
              Account
            </TabsTrigger>
            <TabsTrigger value="organisation" className="px-2.5">
              <Building2 />
              Organisation
            </TabsTrigger>
          </TabsList>
        </header>
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
  return (
    <div className="mt-12 border-t pt-4 font-mono text-xs text-muted-foreground">
      {data.version}
    </div>
  )
}

// ProfileHeader is who is signed in: their monogram, name and email.
function ProfileHeader() {
  const { data: profileData } = useQuery(getProfile, {})
  const profile = profileData?.profile

  if (!profile) return <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>

  return (
    <div className="flex min-w-0 items-center gap-3">
      <AgentMonogram
        name={profile.name || profile.email}
        className="size-10 rounded-full text-sm"
      />
      <div className="min-w-0">
        <h1 className="truncate text-2xl font-semibold tracking-tight">
          {profile.name || profile.email}
        </h1>
        {profile.name && (
          <div className="truncate text-xs text-muted-foreground">{profile.email}</div>
        )}
      </div>
    </div>
  )
}

function AccountSettings() {
  return (
    <div className="space-y-12">
      <OrgsSection />
      <section className="space-y-5">
        <SectionHeading
          title="Connected accounts"
          description="Link your GitHub and Atlassian accounts to receive notifications for your tasks."
        />
        <div className="divide-y rounded-lg border">
          <GitHubAccountRow />
          <AtlassianAccountRow />
        </div>
      </section>
    </div>
  )
}

// AccountRow is one external account: its icon and name, who it is linked as,
// and the button that links or unlinks it.
function AccountRow({
  icon,
  name,
  linkedAs,
  isLoading,
  loginHref,
  onUnlink,
  unlinkPending,
}: {
  icon: React.ReactNode
  name: string
  // The linked account's username; undefined when none is linked.
  linkedAs: string | undefined
  isLoading: boolean
  loginHref: string
  onUnlink: () => void
  unlinkPending: boolean
}) {
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-card">
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{name}</div>
        <div className="truncate text-xs text-muted-foreground">
          {isLoading ? 'Loading...' : linkedAs ? `Linked as ${linkedAs}` : 'Not linked'}
        </div>
      </div>
      {!isLoading &&
        (linkedAs ? (
          <Button variant="outline" size="sm" onClick={onUnlink} disabled={unlinkPending}>
            {unlinkPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Unlink className="h-4 w-4" />
            )}
            Unlink
          </Button>
        ) : (
          <Button asChild size="sm">
            <a href={loginHref}>
              <Link2 className="h-4 w-4" />
              Link
            </a>
          </Button>
        ))}
    </div>
  )
}

function GitHubAccountRow() {
  const { data, isLoading, refetch } = useQuery(getProfile, {})
  const unlinkMutation = useMutation(unlinkGitHubAccount, { onSuccess: () => refetch() })
  return (
    <AccountRow
      icon={<GithubIcon className="size-4" />}
      name="GitHub"
      linkedAs={data?.githubAccount?.githubUsername}
      isLoading={isLoading}
      loginHref="/github/login"
      onUnlink={() => unlinkMutation.mutate({})}
      unlinkPending={unlinkMutation.isPending}
    />
  )
}

function AtlassianAccountRow() {
  const { data, isLoading, refetch } = useQuery(getProfile, {})
  const unlinkMutation = useMutation(unlinkAtlassianAccount, { onSuccess: () => refetch() })
  const account = data?.atlassianAccount
  return (
    <AccountRow
      icon={<AtlassianIcon className="size-4" />}
      name="Atlassian"
      linkedAs={account ? account.atlassianUsername || account.atlassianAccountId : undefined}
      isLoading={isLoading}
      loginHref="/atlassian/login"
      onUnlink={() => unlinkMutation.mutate({})}
      unlinkPending={unlinkMutation.isPending}
    />
  )
}

// CopyField is a value to copy elsewhere, such as a URL: the value in a
// monospace panel with a copy button. display replaces what is shown, for a
// secret, while the button still copies the value.
function CopyField({ value, display, label }: { value: string; display?: string; label: string }) {
  return (
    <div className="flex items-center gap-2">
      <code className="min-w-0 flex-1 truncate rounded-md border bg-muted/40 px-3 py-1.5 font-mono text-sm">
        {display ?? value}
      </code>
      <CopyButton text={value} label={label} />
    </div>
  )
}

function OrgSettings() {
  const orgId = useOrgId()
  const { data, isLoading, refetch } = useQuery(getOrgSettings, {})
  const generateMutation = useMutation(generateAtlassianWebhookSecret, {
    onSuccess: () => refetch(),
  })

  if (isLoading) return <div className="text-muted-foreground">Loading...</div>

  return (
    <div className="space-y-12">
      <section className="space-y-5">
        <SectionHeading
          title="MCP server"
          description="gritz provides an MCP server that you can connect to from any MCP-compatible client."
        />
        {data?.mcpUrl && <CopyField value={data.mcpUrl} label="Copy MCP server URL" />}
      </section>

      <section className="space-y-5">
        <SectionHeading
          title="Custom events"
          description="Your own systems submit events of the org's custom event types to this webhook."
        />
        {data?.customWebhookUrl && (
          <div className="space-y-2">
            <CopyField value={data.customWebhookUrl} label="Copy webhook URL" />
            <p className="text-xs text-muted-foreground">
              Senders POST with{' '}
              <code className="font-mono text-foreground">
                Authorization: Bearer &lt;api key&gt;
              </code>
              , using a key from the{' '}
              <Link to="/keys" search={{ org: orgId }} className="underline">
                Keys
              </Link>{' '}
              page.
            </p>
          </div>
        )}
      </section>

      {data?.githubAppUrl && (
        <section className="space-y-5">
          <SectionHeading
            title="GitHub App"
            description="Install the GitHub App to receive webhook notifications for pull requests on your tasks."
          />
          <div className="flex items-center gap-3 rounded-lg border px-4 py-3">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-card">
              <GithubIcon className="size-4" />
            </span>
            {data.githubInstallationId ? (
              <>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5 text-sm font-medium">
                    <span className="size-2 rounded-full bg-green-500" />
                    Installed
                  </div>
                  <div className="truncate font-mono text-xs text-muted-foreground">
                    installation {String(data.githubInstallationId)}
                  </div>
                </div>
                <Button asChild variant="outline" size="sm">
                  <a
                    href={`https://github.com/settings/installations/${data.githubInstallationId}`}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    <ExternalLink className="h-4 w-4" />
                    Manage
                  </a>
                </Button>
              </>
            ) : (
              <>
                <div className="min-w-0 flex-1 text-sm text-muted-foreground">Not installed</div>
                <GitHubAppNotInstalled appUrl={data.githubAppUrl} onLinked={refetch} />
              </>
            )}
          </div>
        </section>
      )}

      <section className="space-y-5">
        <SectionHeading
          title="Atlassian webhook"
          description="Register this URL and secret in your Atlassian admin settings to receive Jira events."
          action={
            <Button
              variant="outline"
              size="sm"
              onClick={() => generateMutation.mutate({})}
              disabled={generateMutation.isPending}
            >
              {generateMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : data?.atlassianWebhookSecret ? (
                <RefreshCw className="h-4 w-4" />
              ) : (
                <KeyRound className="h-4 w-4" />
              )}
              {data?.atlassianWebhookSecret ? 'Regenerate secret' : 'Generate secret'}
            </Button>
          }
        />
        <div className="space-y-2">
          <SectionLabel>Webhook URL</SectionLabel>
          {data?.atlassianWebhookUrl ? (
            <CopyField value={data.atlassianWebhookUrl} label="Copy webhook URL" />
          ) : (
            <p className="text-sm text-muted-foreground">None</p>
          )}
        </div>
        <div className="space-y-2">
          <SectionLabel>Secret</SectionLabel>
          {data?.atlassianWebhookSecret ? (
            <CopyField
              value={data.atlassianWebhookSecret}
              display={`${data.atlassianWebhookSecret.slice(0, 8)}${'•'.repeat(24)}`}
              label="Copy secret"
            />
          ) : (
            <p className="text-sm text-muted-foreground">No secret configured</p>
          )}
        </div>
      </section>
    </div>
  )
}

function GitHubAppNotInstalled({ appUrl, onLinked }: { appUrl: string; onLinked: () => void }) {
  const [open, setOpen] = useState(false)

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger asChild>
          <Button variant="outline" size="sm">
            <Link2 className="h-4 w-4" />
            Link existing
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
      <Button asChild size="sm">
        <a href={appUrl} target="_blank" rel="noopener noreferrer">
          <ExternalLink className="h-4 w-4" />
          Install
        </a>
      </Button>
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

function OrgsSection() {
  const { data: profileData, refetch } = useQuery(getProfile, {})
  const userId = profileData?.profile?.id
  const orgs = (profileData?.orgs ?? []).filter((org) => org.owner === userId)

  return (
    <section className="space-y-5">
      <SectionHeading
        title="My organisations"
        description="Create and manage the organisations you own."
      />
      <CreateOrgForm onCreate={refetch} />
      {orgs.length > 0 && (
        <ListTable
          header={
            <>
              <ListHead>Organisation</ListHead>
              <ListHead className="hidden sm:table-cell">Created</ListHead>
              <ListHead />
            </>
          }
        >
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
        </ListTable>
      )}
    </section>
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
      <TableCell className="py-2.5 pl-4">
        <div className="flex min-w-0 items-center gap-3">
          <AgentMonogram name={org.name} />
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{org.name}</span>
              {isDefault && <span className="text-xs text-muted-foreground">default</span>}
            </div>
            <div className="font-mono text-xs text-muted-foreground">#{String(org.id)}</div>
          </div>
        </div>
      </TableCell>
      <TableCell className="hidden py-2.5 text-muted-foreground sm:table-cell">
        {org.createdAt ? <RelativeTime date={timestampDate(org.createdAt)} /> : '-'}
      </TableCell>
      <TableCell className="w-px py-2.5 pr-3">
        {!isDefault && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                aria-label={`More actions for ${org.name}`}
                disabled={deleteMutation.isPending}
              >
                {deleteMutation.isPending ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <Ellipsis className="size-4" />
                )}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                variant="destructive"
                onSelect={() => deleteMutation.mutate({ id: org.id })}
              >
                <Trash2 />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </TableCell>
    </TableRow>
  )
}
