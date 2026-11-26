package atlassian

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	apiIssueRe = regexp.MustCompile(`^/rest/api/\d+/issue/([^/]+)`)
	digitsRe   = regexp.MustCompile(`^\d+$`)
)

// CanonicalIssue returns https://{site}/browse/{KEY} for a Jira issue's browse
// or REST API URL on an *.atlassian.net site, and false if raw isn't one.
// Comment queries and trailing segments are dropped. REST URLs that use a
// numeric issue id can't be mapped to a key without a lookup, so they aren't
// accepted.
//
//	site.atlassian.net/browse/X-1?focusedCommentId=2  -> site.atlassian.net/browse/X-1
//	site.atlassian.net/rest/api/2/issue/X-1           -> site.atlassian.net/browse/X-1
func CanonicalIssue(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(u.Host, ".atlassian.net") {
		return "", false
	}
	key, ok := issueKey(u.Path)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("https://%s/browse/%s", u.Host, key), true
}

// issueKey extracts the issue key from a browse path (/browse/{KEY}) or a REST
// API issue path (/rest/api/{v}/issue/{KEY}).
func issueKey(path string) (string, bool) {
	if rest := strings.TrimPrefix(path, "/browse/"); rest != path {
		if key, _, _ := strings.Cut(rest, "/"); key != "" {
			return key, true
		}
	}
	if m := apiIssueRe.FindStringSubmatch(path); m != nil && !digitsRe.MatchString(m[1]) {
		return m[1], true
	}
	return "", false
}
