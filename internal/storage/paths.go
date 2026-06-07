package storage

import (
	"path"
	"strings"
)

// Paths builds and validates the URL paths exposed by the CalDAV server. Every
// path is absolute and carries the configured prefix, because go-webdav passes
// the raw request path (prefix included) to the backend and compares the values
// returned by CurrentUserPrincipal / CalendarHomeSetPath against it directly.
//
// go-webdav classifies a resource purely by the number of path segments after
// the prefix is stripped (see caldav resourceTypeAtPath): depth 1 = user
// principal, depth 2 = calendar home set, depth 3 = calendar, depth 4 = object.
// The layout below is chosen to land each resource at its required depth:
//
//	<prefix>/principals/                       – the single user principal (depth 1)
//	<prefix>/calendars/user/                   – the calendar home set      (depth 2)
//	<prefix>/calendars/user/<name>/            – a calendar collection      (depth 3)
//	<prefix>/calendars/user/<name>/<file>.ics  – a calendar object          (depth 4)
type Paths struct {
	prefix string // normalised: empty or "/foo", never a trailing slash
}

// NewPaths returns a Paths helper for the given URL prefix. A trailing slash is
// stripped; the empty prefix means the handler is mounted at the root.
func NewPaths(prefix string) Paths {
	prefix = strings.TrimRight(prefix, "/")
	return Paths{prefix: prefix}
}

// Principal returns the current user principal path (depth 1).
func (p Paths) Principal() string { return p.prefix + "/principals/" }

// HomeSet returns the calendar home set path (depth 2).
func (p Paths) HomeSet() string { return p.prefix + "/calendars/user/" }

// DefaultCalendar returns the path of the pre-seeded default calendar (depth 3).
func (p Paths) DefaultCalendar() string { return p.prefix + "/calendars/user/default/" }

// IsCalendar reports whether urlPath is a calendar collection path, i.e.
// <prefix>/calendars/user/<name>/ with a single non-empty <name> segment (the
// depth-3 layout documented above). This replicates the one classification
// go-webdav makes internally but does not export, so MKCALENDAR can be rejected
// at any other location just as MKCOL is.
func (p Paths) IsCalendar(urlPath string) bool {
	rest, ok := strings.CutPrefix(urlPath, p.HomeSet())
	if !ok {
		return false
	}
	name := strings.TrimSuffix(rest, "/")
	return name != "" && !strings.Contains(name, "/")
}

// CalendarOf returns the calendar collection path that owns the given object
// path, e.g. "/calendars/default/foo.ics" -> "/calendars/default/". The result
// always ends with a slash.
func CalendarOf(objectPath string) string {
	dir := path.Dir(strings.TrimRight(objectPath, "/"))
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	return dir
}
