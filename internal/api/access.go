package api

import (
	"net/http"
	"regexp"
	"slices"
)

// RoleHeader is set by the auth middleware for requests made with the read-only token. It only
// ever restricts: a client sending it itself just loses access.
const RoleHeader = "X-Llamira-Role"

// readOnlyDenied are GET endpoints that reveal secrets.
var readOnlyDenied = []string{"/api/router/key", "/api/export", "/api/alerts"}

var reHostPrefix = regexp.MustCompile(`^/api/hosts/[^/]+(/.*)$`)

// ReadOnlyAllowed reports whether the read-only token may make this request: GET/HEAD only,
// and not the endpoints in readOnlyDenied (also under /api/hosts/<name>/).
func ReadOnlyAllowed(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if m := reHostPrefix.FindStringSubmatch(path); m != nil {
		path = "/api" + m[1]
	}
	return !slices.Contains(readOnlyDenied, path)
}

func readOnly(r *http.Request) bool { return r.Header.Get(RoleHeader) == "read" }
