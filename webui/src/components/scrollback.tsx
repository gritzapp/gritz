import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { ChevronsDown, ChevronsUp, Loader2 } from 'lucide-react'

// Scrollback is a chat-style infinite scroll container over a bidirectional
// tail-first stream (the timeline, the driver log): it opens pinned to the
// bottom (newest), pulls older pages when the top scrolls into view, restores
// the viewport after a prepend (otherwise the list jumps to the top), and
// follows the tail when new content arrives while pinned. contentLength must
// grow whenever content is appended or prepended — it drives the tail-follow
// detection.
export function Scrollback({
  contentLength,
  hasOlder,
  loadOlder,
  isLoadingOlder,
  className,
  children,
}: {
  contentLength: number
  hasOlder: boolean
  loadOlder: () => void
  isLoadingOlder: boolean
  className?: string
  children: React.ReactNode
}) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const topSentinelRef = useRef<HTMLDivElement>(null)
  // Whether the user is pinned to the bottom; new tail content auto-scrolls only then.
  const atBottomRef = useRef(true)
  // When an older page is prepended, remember the pre-fetch scroll height so we
  // can restore the viewport (otherwise the list jumps to the top).
  const prevScrollHeightRef = useRef<number | null>(null)
  const prevLenRef = useRef(contentLength)
  // Drive the top scroll-shadow and the jump-button opacity (a button fades when
  // it's a no-op — you're already at that edge).
  const [scrolledFromTop, setScrolledFromTop] = useState(false)
  const [atBottom, setAtBottom] = useState(true)

  const syncScrollState = () => {
    const el = scrollRef.current
    if (!el) return
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    atBottomRef.current = bottom
    setAtBottom(bottom)
    setScrolledFromTop(el.scrollTop > 0)
  }

  // Open at the bottom (newest), like a chat.
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
    syncScrollState()
  }, [])

  // On each content change: restore position after an older-page prepend, or
  // follow the tail when new content arrives and the user is already at the
  // bottom.
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const grew = contentLength - prevLenRef.current
    prevLenRef.current = contentLength
    if (prevScrollHeightRef.current !== null) {
      el.scrollTop += el.scrollHeight - prevScrollHeightRef.current
      prevScrollHeightRef.current = null
    } else if (grew > 0 && atBottomRef.current) {
      el.scrollTop = el.scrollHeight
    }
    syncScrollState()
  }, [contentLength])

  // Infinite scroll up: pull older pages when the top sentinel enters view.
  useEffect(() => {
    const sentinel = topSentinelRef.current
    const el = scrollRef.current
    if (!sentinel || !el) return
    const obs = new IntersectionObserver(
      (entries) => {
        if (entries[0].isIntersecting && hasOlder && !isLoadingOlder) {
          prevScrollHeightRef.current = el.scrollHeight
          loadOlder()
        }
      },
      { root: el, threshold: 0 },
    )
    obs.observe(sentinel)
    return () => obs.disconnect()
  }, [hasOlder, isLoadingOlder, loadOlder])

  const jumpToTop = () => scrollRef.current?.scrollTo({ top: 0, behavior: 'smooth' })
  const jumpToBottom = () => {
    const el = scrollRef.current
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  }

  return (
    <div className="relative min-h-0 flex-1">
      {/* Top-edge scroll shadow: fades in once content is scrolled above the fold. */}
      <div
        className={cn(
          'pointer-events-none absolute inset-x-0 top-0 z-10 h-4 bg-gradient-to-b from-black/10 to-transparent dark:from-black/40 transition-opacity',
          scrolledFromTop ? 'opacity-100' : 'opacity-0',
        )}
      />
      <div
        ref={scrollRef}
        onScroll={syncScrollState}
        className={cn('absolute inset-0 overflow-y-auto', className)}
      >
        <div ref={topSentinelRef} className="h-px" />
        {isLoadingOlder && (
          <div className="mb-4 flex justify-center text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
          </div>
        )}
        {children}
      </div>

      {/* Jump-to-edge buttons overlay the scroll area's top-right / bottom-right
          corners, each rendered only when there's somewhere to jump. */}
      {scrolledFromTop && (
        <Button
          variant="secondary"
          size="icon"
          className="absolute right-3 top-3 z-20 rounded-full opacity-70 shadow transition-opacity hover:opacity-100"
          onClick={jumpToTop}
          title="Jump to top"
          aria-label="Jump to top"
        >
          <ChevronsUp className="h-4 w-4" />
        </Button>
      )}
      {!atBottom && (
        <Button
          variant="secondary"
          size="icon"
          className="absolute bottom-3 right-3 z-20 rounded-full opacity-70 shadow transition-opacity hover:opacity-100"
          onClick={jumpToBottom}
          title="Jump to bottom"
          aria-label="Jump to bottom"
        >
          <ChevronsDown className="h-4 w-4" />
        </Button>
      )}
    </div>
  )
}
