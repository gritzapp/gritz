import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { listAgents, listWorkspaces } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Agent } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { Box, ChevronLeft } from 'lucide-react'
import { SectionLabel } from '@/components/agent-overview'
import { AgentBadge } from '@/components/agent-select'
import { ListHead, ListTable } from '@/components/list-table'
import { RelativeTime } from '@/components/relative-time'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/workspaces/$runner/$name')({
  staticData: { orgSwitchRedirect: '/workspaces' },
  component: WorkspaceDetail,
})

// WorkspaceDetail is one runner's workspace: what it declares, and the agents
// that use it. A workspace is identified by its runner and name together, and
// is read-only here: it is defined in the runner's workspaces.yaml.
function WorkspaceDetail() {
  const orgId = useOrgId()
  const { runner, name } = Route.useParams()

  const { data, isLoading, error } = useQuery(listWorkspaces, {}, { refetchInterval: 6000 })
  const { data: agentsData } = useQuery(listAgents, {})

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading workspace...</div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-destructive">Error: {error.message}</div>
      </div>
    )
  }

  const workspace = data?.workspaces.find((w) => w.runnerId === runner && w.name === name)

  if (!workspace) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Workspace not found</div>
      </div>
    )
  }

  const agents = (agentsData?.agents ?? []).filter(
    (a) => a.workspace === workspace.name && a.runner === workspace.runnerId,
  )

  return (
    <div className="mx-auto max-w-3xl space-y-8 px-6 py-8">
      <header className="space-y-3">
        <Link
          to="/workspaces"
          search={{ org: orgId }}
          className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronLeft className="h-4 w-4" />
          Workspaces
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card">
            <Box className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-2xl font-semibold tracking-tight">{workspace.name}</h1>
            <div className="truncate font-mono text-xs text-muted-foreground">
              {workspace.runnerId}
            </div>
          </div>
        </div>
        {workspace.description && (
          <p className="text-sm text-muted-foreground">{workspace.description}</p>
        )}
      </header>

      <dl className="grid grid-cols-2 gap-x-8 gap-y-5">
        <Field label="Runner">
          <span className="font-mono">{workspace.runnerId}</span>
        </Field>
        <Field label="Updated">
          {workspace.updatedAt ? <RelativeTime date={timestampDate(workspace.updatedAt)} /> : '-'}
        </Field>
      </dl>

      <section className="space-y-2">
        <SectionLabel>Features</SectionLabel>
        {workspace.features.length === 0 ? (
          <p className="text-sm text-muted-foreground">This workspace declares no features.</p>
        ) : (
          <ListTable
            header={
              <>
                <ListHead>Feature</ListHead>
                <ListHead>Description</ListHead>
                <ListHead className="hidden sm:table-cell">Used by</ListHead>
              </>
            }
          >
            <TableBody>
              {workspace.features.map((feature) => (
                <TableRow key={feature.name}>
                  <TableCell className="py-2.5 pl-4 align-top font-mono text-xs">
                    {feature.name}
                  </TableCell>
                  <TableCell className="py-2.5 align-top whitespace-normal">
                    <Description text={feature.description} />
                  </TableCell>
                  <TableCell className="hidden py-2.5 pr-4 align-top whitespace-normal sm:table-cell">
                    <AgentList agents={agents.filter((a) => a.features.includes(feature.name))} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </ListTable>
        )}
      </section>

      <section className="space-y-2">
        <SectionLabel>Variables</SectionLabel>
        {workspace.variables.length === 0 ? (
          <p className="text-sm text-muted-foreground">This workspace declares no variables.</p>
        ) : (
          <ListTable
            header={
              <>
                <ListHead>Variable</ListHead>
                <ListHead>Description</ListHead>
                <ListHead>Default</ListHead>
              </>
            }
          >
            <TableBody>
              {workspace.variables.map((variable) => (
                <TableRow key={variable.name}>
                  <TableCell className="py-2.5 pl-4 align-top font-mono text-xs">
                    {variable.name}
                  </TableCell>
                  <TableCell className="py-2.5 align-top whitespace-normal">
                    <Description text={variable.description} />
                  </TableCell>
                  <TableCell className="py-2.5 pr-4 align-top whitespace-normal">
                    {variable.required ? (
                      <span className="text-xs font-medium">Required</span>
                    ) : variable.default ? (
                      <span className="font-mono text-xs break-all">{variable.default}</span>
                    ) : (
                      <span className="text-muted-foreground/70">-</span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </ListTable>
        )}
      </section>

      <section className="space-y-2">
        <SectionLabel>Used by</SectionLabel>
        {agents.length === 0 ? (
          <p className="text-sm text-muted-foreground">No agents use this workspace.</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {agents.map((agent) => (
              <li key={String(agent.id)} className="px-4 py-2.5 text-sm">
                <AgentBadge agentId={agent.id} />
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 space-y-1">
      <dt>
        <SectionLabel>{label}</SectionLabel>
      </dt>
      <dd className="text-sm">{children}</dd>
    </div>
  )
}

function Description({ text }: { text: string }) {
  if (!text) return <span className="text-muted-foreground/70">-</span>
  return <span className="text-muted-foreground">{text}</span>
}

// AgentList is the agents that list a feature, one to a line.
function AgentList({ agents }: { agents: Agent[] }) {
  if (agents.length === 0) return <span className="text-muted-foreground/70">No agents</span>
  return (
    <div className="flex flex-col gap-1">
      {agents.map((agent) => (
        <AgentBadge key={String(agent.id)} agentId={agent.id} />
      ))}
    </div>
  )
}
