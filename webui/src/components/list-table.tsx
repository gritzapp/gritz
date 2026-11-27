import { Table, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'

// ListTable is the table the list pages share: a bordered, rounded frame with
// a muted header row. header is its ListHead cells, children its TableBody.
export function ListTable({
  header,
  children,
}: {
  header: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="overflow-hidden rounded-lg border">
      <Table>
        <TableHeader className="bg-muted/50">
          <TableRow className="hover:bg-transparent">{header}</TableRow>
        </TableHeader>
        {children}
      </Table>
    </div>
  )
}

// ListHead is a ListTable column heading. The first one lines up with the
// first cell's pl-4.
export function ListHead({ className, ...props }: React.ComponentProps<typeof TableHead>) {
  return (
    <TableHead
      className={cn(
        'text-muted-foreground h-9 text-xs font-medium tracking-wide uppercase first:pl-4',
        className,
      )}
      {...props}
    />
  )
}
