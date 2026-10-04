package agent

import (
	"os"
	"strings"
)

// cmdEnv returns the driver's environment, then env, then extra, for use as an
// exec.Cmd's Env. As with exec.Cmd, a later entry for a name wins, so the run's
// values override the driver's (see Driver.Env).
func cmdEnv(env []string, extra ...string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(env)+len(extra))
	out = append(out, base...)
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
