package agent

import "strings"

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
