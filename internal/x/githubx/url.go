package githubx

import (
	"fmt"
	"net/url"
	"regexp"
)

var (
	webIssueRe = regexp.MustCompile(`^/([^/]+)/([^/]+)/issues/(\d+)`)
	webPullRe  = regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/(\d+)`)
	webRunRe   = regexp.MustCompile(`^/([^/]+)/([^/]+)/actions/runs/(\d+)`)
	apiIssueRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)`)
	apiPullRe  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)`)
)

// CanonicalIssue returns https://github.com/{o}/{r}/issues/{n} for an issue's
// web or API URL, and false if raw isn't one. Trailing segments, comment
// anchors and queries are dropped. API comment URLs
// (/issues/comments/{id}) lack the issue number, so they aren't accepted.
//
//	github.com/o/r/issues/5#issuecomment-9  -> github.com/o/r/issues/5
//	api.github.com/repos/o/r/issues/5       -> github.com/o/r/issues/5
func CanonicalIssue(raw string) (string, bool) {
	return canonical(raw, webIssueRe, apiIssueRe, "issues")
}

// CanonicalPullRequest returns https://github.com/{o}/{r}/pull/{n} for a pull
// request's web or API URL, and false if raw isn't one. Trailing segments
// (/files, /commits/…), anchors and queries are dropped.
//
//	github.com/o/r/pull/5/files#discussion_r1  -> github.com/o/r/pull/5
//	api.github.com/repos/o/r/pulls/5           -> github.com/o/r/pull/5
func CanonicalPullRequest(raw string) (string, bool) {
	return canonical(raw, webPullRe, apiPullRe, "pull")
}

// CanonicalWorkflowRun returns https://github.com/{o}/{r}/actions/runs/{id}
// for an Actions workflow run URL, and false if raw isn't one. Every job and
// attempt of a run shares the run's URL.
//
//	github.com/o/r/actions/runs/7/job/8       -> github.com/o/r/actions/runs/7
//	github.com/o/r/actions/runs/7/attempts/2  -> github.com/o/r/actions/runs/7
func CanonicalWorkflowRun(raw string) (string, bool) {
	return canonical(raw, webRunRe, nil, "actions/runs")
}

// canonical matches raw's path against webRe on github.com or apiRe on
// api.github.com, and builds the web URL from the owner, repo and id they
// capture. url.Parse strips the fragment and query, so only the path matters.
func canonical(raw string, webRe, apiRe *regexp.Regexp, segment string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	var m []string
	switch {
	case u.Host == "github.com":
		m = webRe.FindStringSubmatch(u.Path)
	case u.Host == "api.github.com" && apiRe != nil:
		m = apiRe.FindStringSubmatch(u.Path)
	}
	if m == nil {
		return "", false
	}
	return fmt.Sprintf("https://github.com/%s/%s/%s/%s", m[1], m[2], segment, m[3]), true
}
