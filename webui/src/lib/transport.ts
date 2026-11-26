const TOKEN_KEY = 'gritz_token'
const ORG_ID_KEY = 'gritz_org_id'

// Sentinel returned by getOrgId() when no org is selected (pre-login or
// after clearToken). The backend's ResolveOrg treats org_id=0 as "use
// the user's default org".
export const NO_ORG = '0'

export class AuthTransport {
  readonly clientId: string
  private refreshPromise: Promise<string> | null = null
  private events = new EventTarget()
  private lastOrgId: string

  constructor(clientId: string) {
    this.clientId = clientId
    this.lastOrgId = this.getOrgId()
  }

  // The listener's `internal` arg is true when the org changed inside
  // AuthTransport (a token fetch resolving the default or falling back), and
  // false when it came from an explicit setOrgId. Internal changes have no
  // caller updating the URL, so a listener may want to sync it.
  onOrgChange(listener: (orgId: string, internal: boolean) => void): () => void {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<{ orgId: string; internal: boolean }>).detail
      listener(detail.orgId, detail.internal)
    }
    this.events.addEventListener('orgchange', handler)
    return () => this.events.removeEventListener('orgchange', handler)
  }

  getToken(): string | null {
    return this.getItem(TOKEN_KEY)
  }

  getOrgId(): string {
    return this.getItem(ORG_ID_KEY) ?? NO_ORG
  }

  // setOrgId selects the active org. It's a no-op when the org is unchanged.
  // Otherwise it drops the current token (a fresh, org-scoped one is fetched
  // lazily on the next request) and notifies listeners, which is what triggers
  // query-cache invalidation.
  setOrgId(orgId: string): void {
    const current = this.getOrgId()
    if (orgId === current) return
    this.setItem(ORG_ID_KEY, orgId)
    this.removeItem(TOKEN_KEY)
    this.notifyOrgChange(false)
  }

  private setItem(key: string, value: string): void {
    localStorage.setItem(key, value)
    sessionStorage.setItem(key, value)
  }

  private getItem(key: string): string | null {
    const value = sessionStorage.getItem(key)
    return value ?? localStorage.getItem(key)
  }

  private removeItem(key: string): void {
    localStorage.removeItem(key)
    sessionStorage.removeItem(key)
  }

  private notifyOrgChange(internal: boolean): void {
    const next = this.getOrgId()
    if (next === this.lastOrgId) return
    this.lastOrgId = next
    this.events.dispatchEvent(new CustomEvent('orgchange', { detail: { orgId: next, internal } }))
  }

  private storeToken(token: string, orgId: string): void {
    this.setItem(TOKEN_KEY, token)
    this.setItem(ORG_ID_KEY, orgId)
    this.notifyOrgChange(true)
  }

  clearToken(): void {
    this.removeItem(TOKEN_KEY)
    this.removeItem(ORG_ID_KEY)
    this.notifyOrgChange(false)
  }

  async fetchToken(orgId?: string): Promise<string> {
    const id = orgId ?? this.getOrgId()
    const resp = await fetch(`/auth/token?org_id=${encodeURIComponent(id)}`)
    // 401 means the session expired. Reload rather than going to /auth/login:
    // the UI route requires a session, so the server starts the login itself
    // and returns to the requested URL afterwards, ?org included. The org is
    // kept so a URL without ?org still lands in it.
    if (resp.status === 401) {
      this.removeItem(TOKEN_KEY)
      window.location.reload()
      throw new Error('session expired')
    }
    // 403 means the user isn't a member of the requested org (or has no
    // default). Drop the stale org and retry against the user's default.
    if (resp.status === 403 && id !== NO_ORG) {
      this.removeItem(ORG_ID_KEY)
      return this.fetchToken()
    }
    if (!resp.ok) {
      throw new Error(`failed to fetch token: ${resp.status}`)
    }
    const data = await resp.json()
    this.storeToken(data.token, String(data.org_id))
    return data.token
  }

  private async refreshToken(): Promise<string> {
    if (!this.refreshPromise) {
      this.refreshPromise = this.fetchToken().finally(() => {
        this.refreshPromise = null
      })
    }
    return this.refreshPromise
  }

  fetch: typeof globalThis.fetch = async (input, init) => {
    let token = this.getToken()
    if (!token) {
      token = await this.refreshToken()
    }

    const headers = new Headers(init?.headers)
    headers.set('Authorization', `Bearer ${token}`)
    headers.set('X-Client-ID', this.clientId)

    let resp = await fetch(input, { ...init, headers })

    // On 401, try refreshing the token once
    if (resp.status === 401) {
      try {
        token = await this.refreshToken()
      } catch {
        return resp
      }
      headers.set('Authorization', `Bearer ${token}`)
      resp = await fetch(input, { ...init, headers })
    }

    return resp
  }
}
