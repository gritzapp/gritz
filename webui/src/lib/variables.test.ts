import { describe, it, expect } from 'vitest'
import { variablesToSet } from './variables'

describe('variablesToSet', () => {
  it('drops empty fields so declared defaults apply', () => {
    expect(variablesToSet({ JIRA_ISSUE: 'APP-142', BRANCH: '', LABEL: ' ' })).toEqual({
      JIRA_ISSUE: 'APP-142',
      LABEL: ' ',
    })
  })
})
