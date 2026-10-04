package agent

import "strings"

// cmdEnv returns env followed by extra, for use as an exec.Cmd's Env. The result
// is never nil: a nil Env makes the child inherit the driver's own environment,
// and nothing a run starts may do that (see Driver.Env).
func cmdEnv(env []string, extra ...string) []string {
	out := make([]string, 0, len(env)+len(extra))
	out = append(out, env...)
	return append(out, extra...)
}

// envLookup returns the value of key in env, a list of KEY=value pairs. As with
// exec.Cmd, the last entry for a key wins.
func envLookup(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}
