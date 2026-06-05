package storage

import "testing"

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
