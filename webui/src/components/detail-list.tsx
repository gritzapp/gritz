import { SectionLabel } from '@/components/agent-overview'

// DetailList is a bordered list of labelled facts, one DetailRow each. The
// consent pages use it to say who is approving what.
export function DetailList({ children }: { children: React.ReactNode }) {
  return <dl className="divide-y rounded-lg border">{children}</dl>
}

export function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-4 px-4 py-3">
      <dt className="w-28 shrink-0">
        <SectionLabel>{label}</SectionLabel>
      </dt>
      <dd className="flex min-w-0 flex-1 items-center gap-2.5">{children}</dd>
    </div>
  )
}
