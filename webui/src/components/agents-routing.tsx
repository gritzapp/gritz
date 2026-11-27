import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import {
  AgentMonogram,
  eventName,
  LinkTypeIcon,
  linkTypeName,
  nameHue,
  SourceIcon,
  type AgentsViewProps,
} from '@/components/agent-overview'
import { useOrgId } from '@/hooks/use-org-id'
import { eventTypeId } from '@/lib/event-types'
import { cn } from '@/lib/utils'
import { CalendarClock } from 'lucide-react'

// Node height and the gap between nodes of a column, in px. Edges are drawn
// from these, so the nodes are positioned from them too rather than measured.
const NODE = 44
const GAP = 10
const SCHEDULE = 'schedule'

interface SideNode {
  key: string
  icon: React.ReactNode
  label: string
}

// An edge joins a side node to the agent at index agent.
interface Edge {
  key: string
  agent: number
}

type Hover = { column: 'starts' | 'agents' | 'follows'; key: string } | null

// byAgentOrder sorts a column's nodes by the mean position of the agents they
// connect to, which keeps most edges from crossing.
function byAgentOrder(nodes: SideNode[], edges: Edge[]): SideNode[] {
  const mean = (key: string) => {
    const idx = edges.filter((e) => e.key === key).map((e) => e.agent)
    return idx.reduce((a, b) => a + b, 0) / idx.length
  }
  return [...nodes].sort((a, b) => mean(a.key) - mean(b.key))
}

// columnTop is where a column of count nodes starts so it is centred in height.
function columnTop(count: number, height: number): number {
  return (height - (count * (NODE + GAP) - GAP)) / 2
}

// AgentsRouting draws the org's routing: the events and schedules that start
// each agent's tasks on the left, and what those tasks follow on the right.
export function AgentsRouting({ agents, eventTypes, linkTypes }: AgentsViewProps) {
  const orgId = useOrgId()
  const [hover, setHover] = useState<Hover>(null)

  const graph = useMemo(() => {
    const starts = new Map<string, SideNode>()
    const follows = new Map<string, SideNode>()
    const startEdges = new Map<string, Edge>()
    const followEdges = new Map<string, Edge>()
    agents.forEach((agent, i) => {
      for (const t of agent.triggers) {
        const key = eventTypeId(t.source, t.type)
        starts.set(key, {
          key,
          icon: <SourceIcon source={t.source} />,
          label: eventName(eventTypes, t.source, t.type),
        })
        startEdges.set(`${key}>${i}`, { key, agent: i })
      }
      if (agent.schedules.length > 0) {
        starts.set(SCHEDULE, {
          key: SCHEDULE,
          icon: <CalendarClock className="size-4 shrink-0" />,
          label: 'Schedule',
        })
        startEdges.set(`${SCHEDULE}>${i}`, { key: SCHEDULE, agent: i })
      }
      for (const l of agent.links) {
        follows.set(l.type, {
          key: l.type,
          icon: <LinkTypeIcon type={l.type} />,
          label: linkTypeName(linkTypes, l.type),
        })
        followEdges.set(`${l.type}>${i}`, { key: l.type, agent: i })
      }
    })
    const se = [...startEdges.values()]
    const fe = [...followEdges.values()]
    return {
      starts: byAgentOrder([...starts.values()], se),
      follows: byAgentOrder([...follows.values()], fe),
      startEdges: se,
      followEdges: fe,
    }
  }, [agents, eventTypes, linkTypes])

  const rows = Math.max(agents.length, graph.starts.length, graph.follows.length)
  const height = rows * (NODE + GAP) - GAP
  const agentsTop = columnTop(agents.length, height)

  // An edge is lit when the hovered node is one of its ends; a node is lit when
  // it is hovered or at the other end of a lit edge.
  const edgeLit = (column: 'starts' | 'follows', e: Edge) =>
    hover !== null &&
    ((hover.column === column && hover.key === e.key) ||
      (hover.column === 'agents' && hover.key === String(e.agent)))
  const sideLit = (column: 'starts' | 'follows', key: string) => {
    if (hover === null) return true
    if (hover.column === column) return hover.key === key
    const edges = column === 'starts' ? graph.startEdges : graph.followEdges
    return edges.some((e) => e.key === key && edgeLit(column, e))
  }
  const agentLit = (i: number) => {
    if (hover === null) return true
    if (hover.column === 'agents') return hover.key === String(i)
    const edges = hover.column === 'starts' ? graph.startEdges : graph.followEdges
    return edges.some((e) => e.agent === i && e.key === hover.key)
  }

  const side = (column: 'starts' | 'follows', nodes: SideNode[]) => {
    const top = columnTop(nodes.length, height)
    return (
      <div className="relative">
        {nodes.map((n, i) => (
          <div
            key={n.key}
            onMouseEnter={() => setHover({ column, key: n.key })}
            onMouseLeave={() => setHover(null)}
            style={{ top: top + i * (NODE + GAP), height: NODE }}
            className={cn(
              'bg-card absolute inset-x-0 flex items-center gap-2 rounded-lg border px-3 text-sm transition-opacity',
              !sideLit(column, n.key) && 'opacity-30',
            )}
          >
            {n.icon}
            <span className="truncate">{n.label}</span>
          </div>
        ))}
      </div>
    )
  }

  // wires draws a column of edges. flip puts the agents on the left.
  const wires = (column: 'starts' | 'follows', nodes: SideNode[], edges: Edge[], flip: boolean) => {
    const top = columnTop(nodes.length, height)
    const centre = (start: number, i: number) => start + i * (NODE + GAP) + NODE / 2
    // Lit edges are drawn last so they sit on top.
    const sorted = [...edges].sort(
      (a, b) => Number(edgeLit(column, a)) - Number(edgeLit(column, b)),
    )
    return (
      <svg
        viewBox={`0 0 100 ${height}`}
        preserveAspectRatio="none"
        className="h-full w-full"
        aria-hidden="true"
      >
        {sorted.map((e) => {
          const ySide = centre(
            top,
            nodes.findIndex((n) => n.key === e.key),
          )
          const yAgent = centre(agentsTop, e.agent)
          const [y1, y2] = flip ? [yAgent, ySide] : [ySide, yAgent]
          const lit = edgeLit(column, e)
          return (
            <path
              key={`${e.key}>${e.agent}`}
              d={`M0,${y1} C50,${y1} 50,${y2} 100,${y2}`}
              fill="none"
              vectorEffect="non-scaling-stroke"
              strokeWidth={lit ? 2 : 1.5}
              stroke={lit ? `oklch(0.62 0.17 ${nameHue(agents[e.agent].name)})` : 'currentColor'}
              className={cn(
                'transition-opacity',
                !lit && (hover === null ? 'text-muted-foreground/40' : 'text-muted-foreground/15'),
              )}
            />
          )
        })}
      </svg>
    )
  }

  const columns =
    'grid grid-cols-[minmax(0,1fr)_minmax(60px,0.5fr)_minmax(0,1.1fr)_minmax(60px,0.5fr)_minmax(0,1fr)]'
  const heading = 'text-muted-foreground text-xs font-medium tracking-wide uppercase'

  return (
    <div className="overflow-x-auto">
      <div className="min-w-[760px]">
        <div className={cn(columns, 'mb-3')}>
          <div className={heading}>Starts on</div>
          <div />
          <div className={heading}>Agent</div>
          <div />
          <div className={heading}>Follows</div>
        </div>
        <div className={columns} style={{ height }}>
          {side('starts', graph.starts)}
          {wires('starts', graph.starts, graph.startEdges, false)}
          <div className="relative">
            {agents.map((agent, i) => (
              <Link
                key={String(agent.id)}
                to="/agents/$id"
                params={{ id: String(agent.id) }}
                search={{ org: orgId }}
                onMouseEnter={() => setHover({ column: 'agents', key: String(i) })}
                onMouseLeave={() => setHover(null)}
                style={{ top: agentsTop + i * (NODE + GAP), height: NODE }}
                className={cn(
                  'bg-card hover:border-foreground/30 absolute inset-x-0 flex items-center gap-2.5 rounded-lg border px-2 text-sm font-medium shadow-xs transition-opacity',
                  !agentLit(i) && 'opacity-30',
                )}
              >
                <AgentMonogram name={agent.name} className="size-7" />
                <span className="truncate">{agent.name}</span>
                {agent.triggers.length === 0 && agent.schedules.length === 0 && (
                  <span className="text-muted-foreground ml-auto pr-1 text-xs font-normal">
                    manual
                  </span>
                )}
              </Link>
            ))}
          </div>
          {wires('follows', graph.follows, graph.followEdges, true)}
          {side('follows', graph.follows)}
        </div>
      </div>
    </div>
  )
}
