// @vitest-environment happy-dom
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { AuthTransport, NO_ORG } from './transport'

// MSW intercepts the global fetch the transport uses. happy-dom (selected via
// the docblock above) supplies localStorage/sessionStorage and a
// window.location origin so the transport's relative URLs resolve.
const server = setupServer()

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  localStorage.clear()
  sessionStorage.clear()
})
afterAll(() => server.close())

describe('AuthTransport', () => {
  it('lazily fetches a token and attaches auth headers to the request', async () => {
    server.use(
      http.get('/auth/token', () => HttpResponse.json({ token: 'tok-123', org_id: 0 })),
      http.get('/api/test', ({ request }) =>
        HttpResponse.json({
          authorization: request.headers.get('Authorization'),
          clientId: request.headers.get('X-Client-ID'),
        }),
      ),
    )

    const transport = new AuthTransport('client-abc')
    const resp = await transport.fetch('/api/test')
    const body = await resp.json()

    expect(body.authorization).toBe('Bearer tok-123')
    expect(body.clientId).toBe('client-abc')
    expect(transport.getToken()).toBe('tok-123')
  })

  it('drops a stale org on 403 and retries against the default org', async () => {
    localStorage.setItem('gritz_org_id', '5')

    server.use(
      http.get('/auth/token', ({ request }) => {
        const orgId = new URL(request.url).searchParams.get('org_id')
        if (orgId === '5') return new HttpResponse(null, { status: 403 })
        return HttpResponse.json({ token: 'tok-default', org_id: 7 })
      }),
    )

    const transport = new AuthTransport('client-abc')
    const token = await transport.fetchToken()

    expect(token).toBe('tok-default')
    expect(transport.getOrgId()).toBe('7')
    expect(transport.getOrgId()).not.toBe(NO_ORG)
  })

  it('keeps the org and reloads the page when the session expired', async () => {
    localStorage.setItem('gritz_org_id', '5')
    localStorage.setItem('gritz_token', 'tok-stale')
    const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {})

    server.use(http.get('/auth/token', () => new HttpResponse(null, { status: 401 })))

    const transport = new AuthTransport('client-abc')
    await expect(transport.fetchToken()).rejects.toThrow('session expired')

    expect(reload).toHaveBeenCalledOnce()
    expect(transport.getOrgId()).toBe('5')
    expect(transport.getToken()).toBeNull()
  })
})
