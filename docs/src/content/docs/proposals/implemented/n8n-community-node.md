---
title: "n8n Community Node for gritz"
description: "There is no way to integrate gritz with n8n workflows, so users cannot create tasks, monitor status or react to completion in automation pipelines."
---

Issue: https://github.com/icholy/gritz/issues/561

## Problem

There is no way to integrate gritz with n8n workflows. Users who automate processes with n8n cannot create gritz tasks, monitor their status, or react to task completion as part of larger automation pipelines.

## Design

A community node package (`n8n-nodes-gritz`) that exposes gritz's Connect RPC API as n8n node operations. The package uses the **programmatic style** (execute method) since gritz uses Connect RPC (JSON-over-HTTP POST) rather than a traditional REST API with distinct HTTP methods/paths per resource.

### Primary Use Case: Create Task and Wait for Completion

The core workflow is: create a task, poll until it reaches a terminal status, and output the full task details (including links and logs). This is implemented as a single "Create and Wait" operation that blocks the n8n workflow execution until the task finishes:

```
[Trigger] → [gritz: Create and Wait] → [Process Results]
```

The node creates the task, then polls `GetTaskDetails` at a configurable interval until the task reaches a terminal status (COMPLETED, FAILED, or CANCELLED). The output includes the full task details: task metadata, child tasks, links (PRs, issues created by the agent), and events.

**Output schema:**

```json
{
  "task": {
    "id": 123,
    "name": "Deploy staging",
    "status": "COMPLETED",
    "workspace": "default",
    "createdAt": "2026-05-16T10:00:00Z",
    "updatedAt": "2026-05-16T10:05:00Z"
  },
  "children": [...],
  "links": [
    { "title": "PR: fix bug", "url": "https://github.com/...", "relevance": "..." }
  ],
  "events": [...],
  "logs": [
    { "type": "report", "content": "Deployed successfully", "createdAt": "..." }
  ]
}
```

This lets downstream n8n nodes access task outputs — e.g., extract PR URLs from links, parse log messages, or branch on success/failure.

### Package Structure

```
n8n-nodes-gritz/
├── package.json
├── tsconfig.json
├── credentials/
│   └── GritzApi.credentials.ts
├── nodes/
│   └── Gritz/
│       ├── Gritz.node.ts
│       └── Gritz.node.json      # codex file (metadata)
└── README.md
```

### Credentials

Authentication uses API keys (issued via `CreateKey` RPC). The credential definition:

```typescript
import { ICredentialType, INodeProperties } from 'n8n-workflow';

export class GritzApi implements ICredentialType {
  name = 'gritzApi';
  displayName = 'gritz API';
  properties: INodeProperties[] = [
    {
      displayName: 'Server URL',
      name: 'serverUrl',
      type: 'string',
      default: '',
      placeholder: 'https://gritz.example.com',
      required: true,
    },
    {
      displayName: 'API Key',
      name: 'apiKey',
      type: 'string',
      typeOptions: { password: true },
      default: '',
      required: true,
    },
  ];
}
```

The node sends requests with `Authorization: Bearer <apiKey>` header, matching gritz's existing key auth.

### Node Operations

The node is task-focused with a single **Operation** dropdown:

| Operation        | RPC Method(s)              | Description                                      |
|------------------|----------------------------|--------------------------------------------------|
| Create and Wait  | CreateTask + GetTaskDetails + ListLogs | Create a task, poll until done, return details |
| Create           | CreateTask                 | Create a task (fire-and-forget)                  |
| Get Details      | GetTaskDetails + ListLogs  | Get full task details including logs             |
| Update           | UpdateTask                 | Add instructions to a task and optionally start  |
| Cancel           | CancelTask                 | Cancel a running task                            |

### Connect RPC HTTP Mapping

gritz uses Connect RPC which exposes unary RPCs as HTTP POST endpoints:

```
POST /gritz.v1.GritzService/<MethodName>
Content-Type: application/json
Authorization: Bearer <key>

{ ...request fields... }
```

For example, `CreateTask` maps to:

```
POST /gritz.v1.GritzService/CreateTask
Content-Type: application/json

{
  "name": "Deploy staging",
  "workspace": "default",
  "instructions": [{ "text": "Deploy the app to staging" }]
}
```

### Execute Method Implementation

The core operation is "Create and Wait" which creates a task and polls until completion. The node also supports fire-and-forget "Create" and individual operations (Get, Cancel, etc.) for advanced workflows.

```typescript
import {
  IExecuteFunctions,
  INodeExecutionData,
  INodeType,
  INodeTypeDescription,
} from 'n8n-workflow';

export class Gritz implements INodeType {
  description: INodeTypeDescription = {
    displayName: 'gritz',
    name: 'gritz',
    icon: 'file:gritz.svg',
    group: ['transform'],
    version: 1,
    subtitle: '={{$parameter["operation"] + ": " + $parameter["resource"]}}',
    description: 'Create and run gritz tasks',
    defaults: { name: 'gritz' },
    inputs: ['main'],
    outputs: ['main'],
    credentials: [
      { name: 'gritzApi', required: true },
    ],
    properties: [
      {
        displayName: 'Operation',
        name: 'operation',
        type: 'options',
        noDataExpression: true,
        options: [
          { name: 'Create and Wait', value: 'createAndWait', action: 'Create a task and wait for completion' },
          { name: 'Create', value: 'create', action: 'Create a task (fire and forget)' },
          { name: 'Get Details', value: 'getDetails', action: 'Get task details' },
          { name: 'Update', value: 'update', action: 'Add instructions and start a task' },
          { name: 'Cancel', value: 'cancel', action: 'Cancel a task' },
        ],
        default: 'createAndWait',
      },
      // Task Create fields (shown for both create and createAndWait)
      {
        displayName: 'Workspace',
        name: 'workspace',
        type: 'string',
        default: '',
        required: true,
        displayOptions: { show: { operation: ['create', 'createAndWait'] } },
        description: 'Workspace to run the task in',
      },
      {
        displayName: 'Instruction',
        name: 'instruction',
        type: 'string',
        typeOptions: { rows: 4 },
        default: '',
        required: true,
        displayOptions: { show: { operation: ['create', 'createAndWait'] } },
        description: 'The instruction text for the task',
      },
      {
        displayName: 'Name',
        name: 'taskName',
        type: 'string',
        default: '',
        displayOptions: { show: { operation: ['create', 'createAndWait'] } },
        description: 'Optional name for the task',
      },
      {
        displayName: 'Parent Task ID',
        name: 'parentId',
        type: 'number',
        default: 0,
        displayOptions: { show: { operation: ['create', 'createAndWait'] } },
        description: 'Optional parent task ID',
      },
      // Polling config for createAndWait
      {
        displayName: 'Poll Interval (seconds)',
        name: 'pollInterval',
        type: 'number',
        default: 10,
        displayOptions: { show: { operation: ['createAndWait'] } },
        description: 'How often to check task status',
      },
      {
        displayName: 'Timeout (seconds)',
        name: 'timeout',
        type: 'number',
        default: 3600,
        displayOptions: { show: { operation: ['createAndWait'] } },
        description: 'Maximum time to wait before failing (0 = no timeout)',
      },
      // Task ID field (shared by getDetails, update, cancel)
      {
        displayName: 'Task ID',
        name: 'taskId',
        type: 'number',
        default: 0,
        required: true,
        displayOptions: { show: { operation: ['getDetails', 'update', 'cancel'] } },
      },
      // Update operation fields
      {
        displayName: 'Instruction',
        name: 'updateInstruction',
        type: 'string',
        typeOptions: { rows: 4 },
        default: '',
        required: true,
        displayOptions: { show: { operation: ['update'] } },
        description: 'Instruction to add to the task',
      },
      {
        displayName: 'Start',
        name: 'start',
        type: 'boolean',
        default: true,
        displayOptions: { show: { operation: ['update'] } },
        description: 'Start the task after adding instructions (non-interrupting, waits for current run to finish)',
      },
    ],
  };

  async execute(this: IExecuteFunctions): Promise<INodeExecutionData[][]> {
    const items = this.getInputData();
    const returnData: INodeExecutionData[] = [];
    const credentials = await this.getCredentials('gritzApi');
    const serverUrl = (credentials.serverUrl as string).replace(/\/$/, '');

    const rpc = async (method: string, body: Record<string, unknown> = {}) => {
      return this.helpers.httpRequest({
        method: 'POST',
        url: `${serverUrl}/gritz.v1.GritzService/${method}`,
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${credentials.apiKey}`,
          'Connect-Protocol-Version': '1',
        },
        body,
        json: true,
      });
    };

    const TERMINAL_STATUSES = ['COMPLETED', 'FAILED', 'CANCELLED'];

    for (let i = 0; i < items.length; i++) {
      const operation = this.getNodeParameter('operation', i) as string;

      if (operation === 'createAndWait') {
        // Create the task
        const createBody: Record<string, unknown> = {
          name: this.getNodeParameter('taskName', i) as string,
          workspace: this.getNodeParameter('workspace', i) as string,
          instructions: [{ text: this.getNodeParameter('instruction', i) as string }],
        };
        const parentId = this.getNodeParameter('parentId', i) as number;
        if (parentId) createBody.parent = parentId;

        const createResp = await rpc('CreateTask', createBody);
        const taskId = createResp.task.id;

        // Poll until terminal status
        const pollInterval = this.getNodeParameter('pollInterval', i) as number;
        const timeout = this.getNodeParameter('timeout', i) as number;
        const startTime = Date.now();

        let details: Record<string, unknown>;
        while (true) {
          await new Promise((resolve) => setTimeout(resolve, pollInterval * 1000));

          if (timeout > 0 && Date.now() - startTime > timeout * 1000) {
            throw new Error(`Task ${taskId} did not complete within ${timeout}s`);
          }

          details = await rpc('GetTaskDetails', { id: taskId });
          if (TERMINAL_STATUSES.includes(details.task.status)) {
            break;
          }
        }

        // Fetch logs to include in output
        const logsResp = await rpc('ListLogs', { task_id: taskId });

        returnData.push({
          json: { ...details, logs: logsResp.entries || [] },
          pairedItem: { item: i },
        });
      } else if (operation === 'create') {
        // Fire-and-forget: just create and return immediately
        const body: Record<string, unknown> = {
          name: this.getNodeParameter('taskName', i) as string,
          workspace: this.getNodeParameter('workspace', i) as string,
          instructions: [{ text: this.getNodeParameter('instruction', i) as string }],
        };
        const parentId = this.getNodeParameter('parentId', i) as number;
        if (parentId) body.parent = parentId;

        const resp = await rpc('CreateTask', body);
        returnData.push({ json: resp, pairedItem: { item: i } });
      } else if (operation === 'getDetails') {
        const resp = await rpc('GetTaskDetails', { id: this.getNodeParameter('taskId', i) });
        const logsResp = await rpc('ListLogs', { task_id: this.getNodeParameter('taskId', i) });
        returnData.push({ json: { ...resp, logs: logsResp.entries || [] }, pairedItem: { item: i } });
      } else if (operation === 'update') {
        const body: Record<string, unknown> = {
          id: this.getNodeParameter('taskId', i),
          add_instructions: [{ text: this.getNodeParameter('updateInstruction', i) as string }],
          start: this.getNodeParameter('start', i) as boolean,
        };
        const resp = await rpc('UpdateTask', body);
        returnData.push({ json: resp, pairedItem: { item: i } });
      } else if (operation === 'cancel') {
        const resp = await rpc('CancelTask', { id: this.getNodeParameter('taskId', i) });
        returnData.push({ json: resp, pairedItem: { item: i } });
      }
    }

    return [returnData];
  }
}
```

The "Create and Wait" operation is the default and handles the most common workflow: dispatch work to an agent and continue the n8n workflow once the agent finishes. The output includes task details, links (PRs/issues the agent created), child tasks, events, and logs — giving downstream nodes full access to the agent's work products.

### Future: Trigger Node

A polling trigger node could be added later to start workflows when external tasks reach a terminal status. For now, the "Create and Wait" operation covers the primary use case of dispatching work and waiting for results within a single workflow execution.

### Package Metadata

```json
{
  "name": "n8n-nodes-gritz",
  "version": "0.1.0",
  "description": "n8n community node for gritz task orchestration",
  "keywords": ["n8n-community-node-package"],
  "license": "MIT",
  "n8n": {
    "n8nNodesApiVersion": 1,
    "credentials": ["dist/credentials/GritzApi.credentials.js"],
    "nodes": [
      "dist/nodes/Gritz/Gritz.node.js"
    ]
  }
}
```

### Repository Location

The node lives in a separate repository (`icholy/n8n-nodes-gritz`) since it's an npm package with its own release cycle. It has no dependency on the gritz Go codebase.

## Trade-offs

**Programmatic vs. Declarative style**: Chose programmatic because Connect RPC uses a single URL pattern (`POST /service/Method`) with method-specific JSON bodies. The declarative style works best with REST APIs that use distinct HTTP methods and URL paths per operation.

**Polling for "Create and Wait"**: The node polls `GetTaskDetails` at a configurable interval (default 10s). This is simple and requires no server-side changes. A future optimization could use SSE to avoid polling overhead, but the Connect RPC API doesn't currently expose a task status stream.

**Separate repo vs. monorepo**: The n8n node is a TypeScript/npm package with no shared code with the Go server. Keeping it separate avoids polluting the Go module and lets it follow npm publishing conventions independently.

**Minimal surface area**: Rather than exposing every RPC method (Links, Events, Workspaces, Logs), the node focuses on the core task lifecycle. The "Create and Wait" output already includes links, events, and logs from `GetTaskDetails` + `ListLogs`, so separate operations for those aren't needed. More operations can be added later if there's demand.

## Open Questions

1. **Timeout behavior**: Should a timed-out "Create and Wait" cancel the task, or leave it running and just error the n8n execution? Currently it throws an error without cancelling.

2. **Verified node submission**: n8n requires zero runtime dependencies for verified nodes. Since the node only uses `this.helpers.httpRequest()` (built-in), this constraint is satisfied. Should we pursue verification?

3. **Error on failure**: Should "Create and Wait" throw an error (failing the n8n execution) when the task status is FAILED/CANCELLED, or should it always output the result and let downstream nodes decide?
