export class OAuthAuthorization {
  private readonly clientId: string
  private readonly redirectUri: string
  private readonly state: string
  private readonly codeChallenge: string
  private readonly codeChallengeMethod: string
  private readonly responseType: string

  constructor(params: URLSearchParams) {
    this.clientId = params.get('client_id') ?? ''
    this.redirectUri = params.get('redirect_uri') ?? ''
    this.state = params.get('state') ?? ''
    this.codeChallenge = params.get('code_challenge') ?? ''
    this.codeChallengeMethod = params.get('code_challenge_method') ?? ''
    this.responseType = params.get('response_type') ?? ''
  }

  // redirectHost is the host the browser is sent back to once the request is
  // approved or denied, or '' when the redirect URI doesn't parse.
  get redirectHost(): string {
    try {
      return new URL(this.redirectUri).host
    } catch {
      return ''
    }
  }

  get isValid(): boolean {
    return !!(this.clientId && this.redirectUri && this.codeChallenge)
  }

  async approve(token: string): Promise<string> {
    const body = new URLSearchParams({
      token,
      client_id: this.clientId,
      redirect_uri: this.redirectUri,
      state: this.state,
      code_challenge: this.codeChallenge,
      code_challenge_method: this.codeChallengeMethod,
      response_type: this.responseType,
    })
    const resp = await fetch('/oauth/authorize', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString(),
    })
    if (!resp.ok) {
      const text = await resp.text()
      throw new Error(text || `Authorization failed (${resp.status})`)
    }
    const data = await resp.json()
    return data.redirect_uri
  }

  get denyRedirectUri(): string {
    const url = new URL(this.redirectUri)
    url.searchParams.set('error', 'access_denied')
    if (this.state) url.searchParams.set('state', this.state)
    return url.toString()
  }
}
