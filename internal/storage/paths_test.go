package storage

import (
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	cases := []struct {
		prefix               string
		principal, home, def string
	}{
		{"", "/principals/", "/calendars/user/", "/calendars/user/default/"},
		{"/dav", "/dav/principals/", "/dav/calendars/user/", "/dav/calendars/user/default/"},
		{"/dav/", "/dav/principals/", "/dav/calendars/user/", "/dav/calendars/user/default/"},
	}
	for _, c := range cases {
		p := NewPaths(c.prefix)
		if got := p.Principal(); got != c.principal {
			t.Errorf("prefix %q: Principal() = %q, want %q", c.prefix, got, c.principal)
		}
		if got := p.HomeSet(); got != c.home {
			t.Errorf("prefix %q: HomeSet() = %q, want %q", c.prefix, got, c.home)
		}
		if got := p.DefaultCalendar(); got != c.def {
			t.Errorf("prefix %q: DefaultCalendar() = %q, want %q", c.prefix, got, c.def)
		}
	}
}

func TestIsCalendar(t *testing.T) {
	cases := []struct {
		prefix, path string
		want         bool
	}{
		{"", "/calendars/user/work/", true},
		{"", "/calendars/user/default/", true},
		{"/dav", "/dav/calendars/user/work/", true},
		{"", "/calendars/user/", false},           // home set (depth 2)
		{"", "/principals/", false},               // principal (depth 1)
		{"", "/calendars/user/work/x.ics", false}, // object (depth 4)
		{"", "/", false},
		{"/dav", "/calendars/user/work/", false}, // missing prefix
		{"", "/calendars/user/../", false},       // parent traversal
		{"", "/calendars/user/./", false},        // current-dir token
		{"", "/calendars/user/a..b/", false},     // embedded ".."
		{"", "/calendars/user/a\\b/", false},     // backslash separator
		{"", "/calendars/user/a\x00b/", false},   // control character
	}
	for _, c := range cases {
		if got := NewPaths(c.prefix).IsCalendar(c.path); got != c.want {
			t.Errorf("prefix %q: IsCalendar(%q) = %v, want %v", c.prefix, c.path, got, c.want)
		}
	}

	// A name segment over the length bound is rejected.
	long := "/calendars/user/" + strings.Repeat("a", maxCalendarName+1) + "/"
	if NewPaths("").IsCalendar(long) {
		t.Errorf("IsCalendar(<%d-char name>) = true, want false", maxCalendarName+1)
	}
}

func TestCalendarOf(t *testing.T) {
	cases := map[string]string{
		"/calendars/user/default/foo.ics": "/calendars/user/default/",
		"/dav/calendars/user/work/x.ics":  "/dav/calendars/user/work/",
	}
	for obj, want := range cases {
		if got := CalendarOf(obj); got != want {
			t.Errorf("CalendarOf(%q) = %q, want %q", obj, got, want)
		}
	}
}
