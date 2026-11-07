---
name: webui
description: Web UI development guidelines for the v2 React UI in webui/. Apply when working on files in webui/, creating React components, or using TanStack Router/Query.
---

# Web UI v2 Development Guide

A modern React-based UI is being developed in `webui/` to replace the Go template-based UI in `internal/server/templates/`.

## Stack

- **React 19** with TypeScript
- **Vite** for development and build tooling
- **TanStack Router** for file-based routing
- **TanStack Query** for server state management
- **Connect-Query** for type-safe API calls (see `grpc` skill for details)
- **Tailwind CSS v4** for styling
- **shadcn/ui** for component library

## Development

```bash
cd webui
pnpm install
pnpm run dev  # Runs on http://localhost:5173
```

The v2 UI runs independently on the Vite dev server and can be developed incrementally alongside the existing Go template UI.

## Linting

**Always run `pnpm lint` in `webui/` before finishing any task that touches frontend code.** It runs ESLint and the Prettier check (`prettier --check .`), exactly as CI does on every webui PR. Fix all lint errors before opening a PR — do not push code with known lint failures. `pnpm format` fixes formatting.

```bash
cd webui
pnpm lint    # ESLint + Prettier check
pnpm format  # fix formatting
```

## Routing (TanStack Router)

**File-based routing** - Routes are defined as files in `src/routes/`. The TanStack Router Vite plugin auto-generates `routeTree.gen.ts` (gitignored).

**Route file naming**:
- `index.tsx` → `/`
- `tasks.tsx` → `/tasks`
- `tasks/$id.tsx` → `/tasks/:id` (dynamic param)
- `tasks.$id.edit.tsx` → `/tasks/:id/edit`
- `__root.tsx` → Root layout (wraps all routes)

**Creating a route**:
```tsx
// src/routes/tasks.tsx
import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/tasks')({
  component: TasksPage,
})

function TasksPage() {
  return <div>Tasks</div>
}
```

**Accessing route params**:
```tsx
// src/routes/tasks/$id.tsx
import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/tasks/$id')({
  component: TaskDetail,
})

function TaskDetail() {
  const { id } = Route.useParams()
  return <div>Task {id}</div>
}
```

**Navigation**:
```tsx
import { Link, useNavigate } from '@tanstack/react-router'

// Declarative
<Link to="/tasks/$id" params={{ id: '123' }}>View Task</Link>

// Programmatic
const navigate = useNavigate()
navigate({ to: '/tasks/$id', params: { id: '123' } })
```

**Type safety** - The router instance is registered via module augmentation in `main.tsx`, enabling full TypeScript autocomplete for routes, params, and navigation throughout the app.

## State Management (TanStack Query + Connect-Query)

**Use Connect-Query for ALL API calls** - The webui uses `@connectrpc/connect-query` which provides type-safe hooks generated from protobuf definitions. Do NOT use raw `fetch` or manual `useQuery` with fetch.

**Generated code** - Run `pnpm run generate` in the `webui/` directory to regenerate TypeScript types and hooks from proto definitions. Generated files are in `src/gen/` (gitignored).

**Fetching data with useQuery**:
```tsx
import { useQuery } from '@connectrpc/connect-query'
import { listTasks } from '@/gen/gritz/v1/gritz-GritzService_connectquery'

function TaskList() {
  const { data, isLoading, error } = useQuery(listTasks, {
    statuses: ['pending', 'running'],
  })

  if (isLoading) return <div>Loading...</div>
  if (error) return <div>Error: {error.message}</div>

  return (
    <ul>
      {data?.tasks.map(task => (
        <li key={String(task.id)}>{task.name}</li>
      ))}
    </ul>
  )
}
```

**Mutations with useMutation**:
```tsx
import { useMutation, useQueryClient } from '@connectrpc/connect-query'
import { createTask, listTasks } from '@/gen/gritz/v1/gritz-GritzService_connectquery'

function CreateTaskButton() {
  const queryClient = useQueryClient()

  const mutation = useMutation(createTask, {
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [listTasks.service.typeName] })
    },
  })

  return (
    <button onClick={() => mutation.mutate({ name: 'New Task', workspace: 'default' })}>
      Create
    </button>
  )
}
```

**Benefits**:
- Full type safety from proto → TypeScript → React
- Automatic caching and background updates
- Built-in loading and error states
- Request deduplication
- See the `grpc` skill for complete documentation on TypeScript client usage

## UI Development Guidelines

**Use shadcn/ui components** - Always prefer off-the-shelf shadcn components with their default styles. Avoid writing custom UI components unless absolutely necessary. Browse available components at https://ui.shadcn.com/docs/components

**Add components as needed** - Install shadcn components with `npx shadcn@latest add <component-name>` (e.g., `button`, `card`, `table`, `dialog`)

**Reference v1 for data, not design** - The Go template UI in `internal/server/templates/` can be referenced to understand:
- What API calls are made
- What data is displayed
- What information appears on each page

**Do NOT copy v1 implementation** - Do not replicate v1's layout, styles, HTML structure, or templates. The v2 is a complete rewrite with modern components and UX patterns.

**API Access** - The v2 UI will call the same Connect RPC API at `/gritz.v1.GritzService/*` that the v1 UI uses.

## Component Organization

**Directory structure**:
- `src/components/ui/` - **shadcn/ui primitives only** (Button, Badge, Card, Table, etc.)
- `src/components/` - **Custom project components** (StatusBadge, RelativeTime, CommandBadge, etc.)

**shadcn/ui philosophy** - shadcn/ui is NOT a traditional npm package. It's a code distribution system that copies component source code into your project (`src/components/ui/`). You own the code and can modify it directly.

**Important**: Only shadcn components belong in `components/ui/`. Custom components that build on top of shadcn primitives go in `components/`.

**Examples**:
```
src/components/ui/badge.tsx       ← shadcn primitive
src/components/ui/button.tsx      ← shadcn primitive
src/components/status-badge.tsx   ← custom component (uses Badge)
src/components/command-badge.tsx  ← custom component (uses Badge)
src/components/relative-time.tsx  ← custom component (uses Tooltip)
```

## Component Customization

**One component, multiple variants** - Do NOT create multiple copies of the same component (e.g., `button-primary.tsx`, `button-secondary.tsx`). Instead, use class-variance-authority (CVA) to add variants to a single component:

```tsx
// src/components/ui/button.tsx
const buttonVariants = cva(
  "inline-flex items-center justify-center...",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground...",
        destructive: "bg-destructive text-destructive-foreground...",
        success: "bg-green-600 text-white hover:bg-green-700", // Custom variant
      }
    }
  }
)
```

Usage: `<Button variant="success">Save</Button>`

**When to create custom components** (in `src/components/`):
- Building composite/wrapper components (e.g., `TaskCard` that uses `Card` + `Badge` + `Button`)
- Creating domain-specific components (e.g., `StatusBadge`, `CommandBadge`)
- Reusing styled combinations across multiple routes

**Don't create**:
- Multiple variations of the same primitive component
- Wrapper components just to override styles (edit the source directly instead)
- Custom components in `components/ui/` - that directory is reserved for shadcn primitives

**Pattern**: One source of truth per primitive component, compose for complexity.
