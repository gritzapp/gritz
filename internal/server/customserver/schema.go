package customserver

import (
	"net/url"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
)

// LinkTypeURL is the link type a custom event type may declare: the resource
// is whatever the event's URL names, so the URL is its own canonical form.
const LinkTypeURL = "custom.url"

// init registers customserver's link type on the process-wide default registry.
func init() {
	RegisterSchemas(eventrouter.DefaultSchemaRegistry)
}

// RegisterSchemas records the link type custom event types may name on reg.
// The event types themselves are per org and added with
// eventrouter.SchemaRegistry.With.
func RegisterSchemas(reg *eventrouter.SchemaRegistry) {
	reg.MustRegisterLinkType(model.LinkTypeDef{
		Name:      LinkTypeURL,
		Source:    eventrouter.SourceCustom,
		Label:     "Custom: URL",
		Canonical: canonicalURL,
	})
}

// canonicalURL accepts any absolute http or https URL and returns it unchanged.
func canonicalURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return raw, true
}
