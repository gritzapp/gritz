import { create } from '@bufbuild/protobuf'
import { describe, it, expect } from 'vitest'
import { AgentSchema } from '@/gen/gritz/v1/gritz_pb'
import { agentCloneValues, agentFormValues, buildAgent } from './agents'

const agent = create(AgentSchema, {
  id: 42n,
  name: 'implementer',
  runner: 'local',
  workspace: 'gritz',
  prompt: 'Do the thing.',
  autoArchive: { seconds: 3600n },
  variables: { BRANCH: 'main' },
  features: ['github', 'jira'],
  triggers: [
    {
      source: 'github',
      type: 'issue_comment',
      conditions: [{ attr: 'body', op: 'contains', value: '/gritz' }],
      yieldTo: ['owner'],
      link: 'github.pr',
    },
  ],
  links: [
    {
      type: 'github.pr',
      kind: 'github.pr',
      claim: 'owner',
      events: [{ type: 'pull_request_review', wake: true }],
    },
  ],
  schedules: [
    {
      id: 7n,
      name: 'nightly',
      cronExpr: '0 3 * * *',
      timezone: 'UTC',
      enabled: true,
      prompt: 'Triage.',
      lastTaskId: 99n,
    },
  ],
})

describe('agentCloneValues', () => {
  it('copies the agent under a new name, with new schedules', () => {
    const original = agentFormValues(agent)
    expect(agentCloneValues(agent)).toEqual({
      ...original,
      name: 'implementer (copy)',
      schedules: [{ ...original.schedules[0], id: '' }],
    })
  })

  it('builds an agent that CreateAgent adds anew', () => {
    const built = buildAgent(agentCloneValues(agent), 0n)
    expect(built.id).toBe(0n)
    expect(built.name).toBe('implementer (copy)')
    expect(built.schedules).toHaveLength(1)
    expect(built.schedules[0].id).toBe(0n)
    expect(built.schedules[0].lastTaskId).toBe(0n)
    expect(built.schedules[0].cronExpr).toBe('0 3 * * *')
    expect(built.triggers).toEqual(agent.triggers)
    expect(built.links).toEqual(agent.links)
    expect(built.features).toEqual(agent.features)
    expect(built.variables).toEqual(agent.variables)
    expect(built.autoArchive?.seconds).toBe(3600n)
  })
})
