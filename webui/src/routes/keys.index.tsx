import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import { listKeys, deleteKey } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Key } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/relative-time'
import { Ellipsis, KeyRound, Loader2, Plus, Trash2 } from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/keys/')({
  component: KeysPage,
})

function KeysPage() {
  const orgId = useOrgId()
  const { data, isLoading, error, refetch } = useQuery(
    listKeys,
    {},
    {
      refetchInterval: 6000,
    },
  )

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading API keys...</div>
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

  const keys = data?.keys ?? []

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <div className="flex items-baseline gap-3">
          <h1 className="text-2xl font-bold">Keys</h1>
          {keys.length > 0 && (
            <span className="text-muted-foreground text-sm tabular-nums">{keys.length}</span>
          )}
        </div>
        <Link to="/keys/new" search={{ org: orgId }}>
          <Button>
            <Plus className="h-4 w-4" />
            Key
          </Button>
        </Link>
      </div>
      {keys.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">No API keys found</div>
      ) : (
        <ListTable
          header={
            <>
              <ListHead>Key</ListHead>
              <ListHead>Expires</ListHead>
              <ListHead className="hidden sm:table-cell">Created</ListHead>
              <ListHead />
            </>
          }
        >
          <TableBody>
            {keys.map((key) => (
              <KeyRow key={key.id} apiKey={key} onDelete={refetch} />
            ))}
          </TableBody>
        </ListTable>
      )}
    </div>
  )
}

function KeyRow({ apiKey, onDelete }: { apiKey: Key; onDelete: () => void }) {
  const deleteMutation = useMutation(deleteKey, {
    onSuccess: () => onDelete(),
  })

  const isExpired = apiKey.expiresAt && timestampDate(apiKey.expiresAt) < new Date()

  return (
    <TableRow>
      <TableCell className="w-1/2 py-3 pl-4">
        <div className="flex min-w-0 items-center gap-2.5">
          <KeyRound className="text-muted-foreground size-4 shrink-0" />
          <span className="truncate font-medium">{apiKey.name}</span>
        </div>
      </TableCell>
      <TableCell className="py-3">
        {apiKey.expiresAt ? (
          isExpired ? (
            <span className="text-destructive inline-flex items-center gap-1.5">
              <span className="bg-destructive size-2 rounded-full" />
              Expired <RelativeTime date={timestampDate(apiKey.expiresAt)} />
            </span>
          ) : (
            <RelativeTime date={timestampDate(apiKey.expiresAt)} />
          )
        ) : (
          <span className="text-muted-foreground/70">Never</span>
        )}
      </TableCell>
      <TableCell className="text-muted-foreground hidden py-3 sm:table-cell">
        {apiKey.createdAt ? <RelativeTime date={timestampDate(apiKey.createdAt)} /> : '-'}
      </TableCell>
      <TableCell className="w-px py-3 pr-3">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              aria-label={`More actions for ${apiKey.name}`}
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
              onSelect={() => deleteMutation.mutate({ id: apiKey.id })}
            >
              <Trash2 />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  )
}
