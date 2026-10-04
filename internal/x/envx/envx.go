// Package envx works with environments in the KEY=value list form used by
// os.Environ and exec.Cmd.Env.
package envx

import (
	"maps"
	"os"
	"slices"
	"strings"
)

// Expand replaces ${var} or $var in s with the value of var in env, a list of
// KEY=value pairs, the way os.ExpandEnv does against the process environment.
// As with exec.Cmd, the last entry for a name wins. Undefined variables expand
// to the empty string.
func Expand(s string, env []string) string {
	return os.Expand(s, func(key string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
				return v
			}
		}
		return ""
	})
}

// FromMap returns m as a list of KEY=value pairs, sorted by key so the result
// is deterministic.
func FromMap(m map[string]string) []string {
	env := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		env = append(env, k+"="+m[k])
	}
	return env
}
