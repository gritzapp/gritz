// variablesToSet returns the variables a form sends: the fields the user
// filled in. An empty field is left unset, so the workspace's default (if any)
// applies rather than an empty string.
export function variablesToSet(values: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(values).filter(([, value]) => value !== ''))
}
