// Package puppet is the Server side of an agent with no behaviour of its own.
// The puppet agent (agent.PuppetAgent) forwards each Prompt call to a Server,
// one HTTP request held open until the Server's owner (the e2e harness) ends
// the Run it became, so a test decides how every run ends.
package puppet

// Options configures a puppet agent.
type Options struct {
	// URL is the Server endpoint each Prompt call is forwarded to.
	URL string
}

// Request is the body of a forwarded Prompt call.
type Request struct {
	TaskID int64  `json:"task_id"`
	Prompt string `json:"prompt"`
	Resume bool   `json:"resume"`
}

// Response is the Server's answer; Error empty means success.
type Response struct {
	Error string `json:"error,omitempty"`
}
