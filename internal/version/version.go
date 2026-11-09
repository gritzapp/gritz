package version

import "runtime/debug"

// Version is set via -ldflags at build time for releases. When set, it takes
// precedence over the information from debug.ReadBuildInfo().
var Version string

// String returns the build version. It prefers the version injected at build
// time, falling back to the short VCS revision (with a "-dirty" suffix for
// modified trees) and "(unknown)" when neither is available.
//
// The module version from the build info is ignored: the module path has no
// major version suffix, so Go stamps a pseudo-version after the last v1 tag
// rather than the release tag.
func String() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "(unknown)"
	}
	if len(revision) > 8 {
		revision = revision[:8]
	}
	if modified {
		revision += "-dirty"
	}
	return revision
}
