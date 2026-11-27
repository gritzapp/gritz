import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import {
  listOrgMembers,
  addOrgMember,
  removeOrgMember,
  getProfile,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { OrgMember } from '@/gen/gritz/v1/gritz_pb'
import { useOrgId } from '@/hooks/use-org-id'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { AgentMonogram } from '@/components/agent-overview'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { RelativeTime } from '@/components/relative-time'
import { Crown, Ellipsis, Loader2, Trash2, UserPlus } from 'lucide-react'
import { useState } from 'react'

export const Route = createFileRoute('/members/')({
  component: MembersPage,
})

function MembersPage() {
  const { data: profileData } = useQuery(getProfile, {})
  const { data, isLoading, error, refetch } = useQuery(
    listOrgMembers,
    {},
    {
      refetchInterval: 6000,
    },
  )

  const orgId = useOrgId()
  const isOwner =
    profileData?.orgs.some(
      (org) => String(org.id) === orgId && org.owner === profileData.profile?.id,
    ) ?? false

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading members...</div>
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

  const members = data?.members ?? []

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <div className="flex items-baseline gap-3">
          <h1 className="text-2xl font-bold">Members</h1>
          {members.length > 0 && (
            <span className="text-muted-foreground text-sm tabular-nums">{members.length}</span>
          )}
        </div>
        {isOwner && <AddMemberForm onAdd={refetch} />}
      </div>
      {members.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">No members found</div>
      ) : (
        <ListTable
          header={
            <>
              <ListHead>Member</ListHead>
              <ListHead>Role</ListHead>
              <ListHead className="hidden sm:table-cell">Added</ListHead>
              <ListHead />
            </>
          }
        >
          <TableBody>
            {members.map((member) => (
              <MemberRow
                key={member.userId}
                member={member}
                onRemove={refetch}
                isOwner={isOwner}
                isSelf={member.userId === profileData?.profile?.id}
              />
            ))}
          </TableBody>
        </ListTable>
      )}
    </div>
  )
}

function AddMemberForm({ onAdd }: { onAdd: () => void }) {
  const [email, setEmail] = useState('')
  const mutation = useMutation(addOrgMember, {
    onSuccess: () => {
      setEmail('')
      onAdd()
    },
  })

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!email.trim()) return
    await mutation.mutateAsync({ email: email.trim() })
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-wrap items-center gap-2">
      <Input
        type="email"
        placeholder="Email address"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="w-64"
      />
      <Button type="submit" disabled={mutation.isPending || !email.trim()}>
        {mutation.isPending ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : (
          <UserPlus className="h-4 w-4" />
        )}
        Add member
      </Button>
      {mutation.error && (
        <span className="text-destructive text-sm self-center">{mutation.error.message}</span>
      )}
    </form>
  )
}

function MemberRow({
  member,
  onRemove,
  isOwner,
  isSelf,
}: {
  member: OrgMember
  onRemove: () => void
  isOwner: boolean
  isSelf: boolean
}) {
  const removeMutation = useMutation(removeOrgMember, {
    onSuccess: () => onRemove(),
  })

  return (
    <TableRow>
      <TableCell className="w-1/2 py-2.5 pl-4">
        <div className="flex min-w-0 items-center gap-3">
          <AgentMonogram name={member.name || member.email} className="rounded-full" />
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{member.name || member.email}</span>
              {isSelf && <span className="text-muted-foreground text-xs">you</span>}
            </div>
            {member.name && (
              <div className="text-muted-foreground truncate text-xs">{member.email}</div>
            )}
          </div>
        </div>
      </TableCell>
      <TableCell className="py-2.5">
        <span
          className={cn(
            'inline-flex items-center gap-1.5',
            member.role !== 'owner' && 'text-muted-foreground',
          )}
        >
          {member.role === 'owner' && (
            <Crown className="size-3.5 text-amber-600 dark:text-amber-400" />
          )}
          {member.role}
        </span>
      </TableCell>
      <TableCell className="text-muted-foreground hidden py-2.5 sm:table-cell">
        {member.createdAt ? <RelativeTime date={timestampDate(member.createdAt)} /> : '-'}
      </TableCell>
      <TableCell className="w-px py-2.5 pr-3">
        {isOwner && member.role !== 'owner' && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                aria-label={`More actions for ${member.name || member.email}`}
                disabled={removeMutation.isPending}
              >
                {removeMutation.isPending ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <Ellipsis className="size-4" />
                )}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                variant="destructive"
                onSelect={() => removeMutation.mutate({ userId: member.userId })}
              >
                <Trash2 />
                Remove
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </TableCell>
    </TableRow>
  )
}
