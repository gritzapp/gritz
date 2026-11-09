package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// BadgeRoute serves a task's shields.io endpoint badge. The mux matches only
// whole path segments, so the handler strips the ".json" suffix itself.
const BadgeRoute = "GET /badge/{file}"

// badgeLogo is the gritz logo in white, for the black badge.
const badgeLogo = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="#fff"><rect x="8" y="2" width="16" height="7" rx="2"/><rect x="2" y="8" width="7" height="15" rx="2"/><rect x="17" y="15" width="13" height="6" rx="2"/><rect x="8" y="23" width="9" height="7" rx="2"/></svg>`

// badge is the shields.io endpoint badge schema:
// https://shields.io/badges/endpoint-badge
type badge struct {
	SchemaVersion int    `json:"schemaVersion"`
	Label         string `json:"label"`
	Message       string `json:"message"`
	Color         string `json:"color"`
	LabelColor    string `json:"labelColor"`
	LogoSvg       string `json:"logoSvg"`
}

// BadgeHandler serves /badge/{id}.json: a black badge showing the gritz logo
// and the task id. It is public and reads nothing but the path, so it reveals
// nothing about the task, not even whether it exists. The id must be a
// positive integer in canonical form; anything else is a 404.
func BadgeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutSuffix(r.PathValue("file"), ".json")
		if !ok {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The body is a function of the id alone, so it never changes.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(badge{
			SchemaVersion: 1,
			Message:       raw,
			Color:         "black",
			LabelColor:    "black",
			LogoSvg:       badgeLogo,
		})
	})
}
