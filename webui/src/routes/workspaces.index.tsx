import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import { listWorkspaces, clearWorkspaces } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/relative-time'
import { Trash2, Loader2 } from 'lucide-react'

export const Route = createFileRoute('/workspaces/')({
  component: WorkspacesPage,
})

const ALL_RUNNERS = '\x00'

function WorkspacesPage() {
  const [selectedRunner, setSelectedRunner] = useState(ALL_RUNNERS)
  const { data, isLoading, error, refetch } = useQuery(
    listWorkspaces,
    {},
    {
      refetchInterval: 6000,
    },
  )
  const clearMutation = useMutation(clearWorkspaces, { onSuccess: () => refetch() })

  const handleClear = async () => {
    await clearMutation.mutateAsync({
      runnerId: selectedRunner === ALL_RUNNERS ? '' : selectedRunner,
    })
    setSelectedRunner(ALL_RUNNERS)
  }

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-muted-foreground">Loading workspaces...</div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-destructive">Error: {error.message}</div>
      </div>
    )
  }

  const allWorkspaces = data?.workspaces ?? []
  const runners = [...new Set(allWorkspaces.map((w) => w.runnerId).filter(Boolean))]
  const workspaces = allWorkspaces.filter(
    (w) => selectedRunner === ALL_RUNNERS || w.runnerId === selectedRunner,
  )

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <h1 className="text-2xl font-bold">Workspaces</h1>
        <div className="flex flex-wrap items-center gap-4">
          <Select value={selectedRunner} onValueChange={setSelectedRunner}>
            <SelectTrigger className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_RUNNERS}>All runners</SelectItem>
              {runners.map((runner) => (
                <SelectItem key={runner} value={runner!}>
                  {runner}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            onClick={handleClear}
            disabled={clearMutation.isPending || allWorkspaces.length === 0}
          >
            {clearMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Trash2 className="h-4 w-4" />
            )}
            Clear
          </Button>
        </div>
      </div>
      {workspaces.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">No workspaces registered</div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Description</TableHead>
              <TableHead>Features</TableHead>
              <TableHead>Runner</TableHead>
              <TableHead>Last Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {workspaces.map((workspace) => (
              <TableRow key={workspace.name}>
                <TableCell className="font-medium">{workspace.name}</TableCell>
                <TableCell className="text-muted-foreground">
                  {workspace.description || '-'}
                </TableCell>
                <TableCell>
                  {workspace.features.length === 0 ? (
                    <span className="text-muted-foreground">-</span>
                  ) : (
                    <dl className="space-y-1 text-sm">
                      {workspace.features.map((f) => (
                        <div key={f.name}>
                          <dt className="font-mono">{f.name}</dt>
                          {f.description && (
                            <dd className="text-muted-foreground text-xs">{f.description}</dd>
                          )}
                        </div>
                      ))}
                    </dl>
                  )}
                </TableCell>
                <TableCell className="text-muted-foreground font-mono text-sm">
                  {workspace.runnerId || '-'}
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {workspace.updatedAt ? (
                    <RelativeTime date={timestampDate(workspace.updatedAt)} />
                  ) : (
                    '-'
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}
