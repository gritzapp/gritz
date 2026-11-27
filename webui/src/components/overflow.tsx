import { HoverCard, HoverCardContent, HoverCardTrigger } from '@/components/ui/hover-card'
import { cn } from '@/lib/utils'

// OverflowItem is one entry of an Overflow: node is how it shows in the cell,
// and full, when it differs, how it shows in the card that hovering opens.
export interface OverflowItem {
  key: string
  node: React.ReactNode
  full?: React.ReactNode
}

// Overflow keeps a list cell to a fixed size however many entries it has: it
// shows the first max of them, counts the rest as "+N", and lists every entry
// in full in a card when the cell is hovered or focused.
//
// layout is how the shown entries sit: 'stack' overlaps them like a pile of
// avatars, for marks; 'row' puts them on a line, for short text; 'column' puts
// one per line.
export function Overflow({
  items,
  max,
  layout,
  label,
  className,
}: {
  items: OverflowItem[]
  max: number
  layout: 'stack' | 'row' | 'column'
  // Text after the entries, saying what they are when their marks alone don't.
  label?: string
  className?: string
}) {
  const shown = items.slice(0, max)
  const hidden = items.length - shown.length
  const hasCard = hidden > 0 || items.some((item) => item.full !== undefined)

  const cell = (
    <div
      className={cn(
        layout === 'stack' && 'flex w-fit items-center',
        layout === 'row' && 'flex w-fit flex-wrap items-center gap-x-3 gap-y-1',
        layout === 'column' && 'w-fit space-y-2',
        className,
      )}
    >
      {shown.map((item, i) =>
        layout === 'stack' ? (
          // Earlier entries sit on top, so an entry's right edge, where a mark
          // may carry a badge, is never under its neighbour.
          <span
            key={item.key}
            style={{ zIndex: shown.length - i }}
            className="ring-background relative -ml-1 rounded-md ring-2 first:ml-0"
          >
            {item.node}
          </span>
        ) : (
          <div key={item.key} className="min-w-0">
            {item.node}
          </div>
        ),
      )}
      {hidden > 0 && (
        <span
          className={cn(
            'text-muted-foreground text-xs whitespace-nowrap tabular-nums',
            layout === 'stack' && 'ml-1.5',
          )}
        >
          +{hidden}
          {layout === 'column' && ' more'}
        </span>
      )}
      {label && (
        <span className={cn('text-muted-foreground text-xs', layout === 'stack' && 'ml-2')}>
          {label}
        </span>
      )}
    </div>
  )

  if (!hasCard) return cell
  return (
    <HoverCard openDelay={150} closeDelay={100}>
      <HoverCardTrigger asChild>{cell}</HoverCardTrigger>
      <HoverCardContent
        align="start"
        className="max-h-96 w-auto max-w-md min-w-56 space-y-3 overflow-y-auto p-3"
      >
        {items.map((item) => (
          <div key={item.key} className="min-w-0">
            {item.full ?? item.node}
          </div>
        ))}
      </HoverCardContent>
    </HoverCard>
  )
}
