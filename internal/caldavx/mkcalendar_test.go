package caldavx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/caldav"

	"medav/internal/storage"
)

// fakeCreator records the last CreateCalendar call.
type fakeCreator struct {
	called bool
	cal    caldav.Calendar
	err    error
}

func (f *fakeCreator) CreateCalendar(_ context.Context, cal *caldav.Calendar) error {
	f.called = true
	f.cal = *cal
	return f.err
}

const fullBody = `<?xml version="1.0" encoding="utf-8"?>
<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set>
    <D:prop>
      <D:displayname>Work</D:displayname>
      <C:calendar-description>My work calendar</C:calendar-description>
      <C:supported-calendar-component-set>
        <C:comp name="VEVENT"/>
        <C:comp name="VTODO"/>
      </C:supported-calendar-component-set>
    </D:prop>
  </D:set>
</C:mkcalendar>`

func TestMkcalendarFullBody(t *testing.T) {
	fc := &fakeCreator{}
	h := Wrap(passthrough(t), fc, storage.NewPaths(""))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("MKCALENDAR", "/calendars/user/work/", strings.NewReader(fullBody))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if !fc.called {
		t.Fatal("CreateCalendar was not called")
	}
	if fc.cal.Path != "/calendars/user/work/" {
		t.Errorf("Path = %q", fc.cal.Path)
	}
	if fc.cal.Name != "Work" {
		t.Errorf("Name = %q, want Work", fc.cal.Name)
	}
	if fc.cal.Description != "My work calendar" {
		t.Errorf("Description = %q", fc.cal.Description)
	}
	if len(fc.cal.SupportedComponentSet) != 2 ||
		fc.cal.SupportedComponentSet[0] != "VEVENT" ||
		fc.cal.SupportedComponentSet[1] != "VTODO" {
		t.Errorf("SupportedComponentSet = %v, want [VEVENT VTODO]", fc.cal.SupportedComponentSet)
	}
}

func TestMkcalendarEmptyBody(t *testing.T) {
	fc := &fakeCreator{}
	h := Wrap(passthrough(t), fc, storage.NewPaths(""))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("MKCALENDAR", "/calendars/user/work/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if !fc.called {
		t.Fatal("CreateCalendar was not called")
	}
	if fc.cal.Name != "" || fc.cal.SupportedComponentSet != nil {
		t.Errorf("expected defaults, got %+v", fc.cal)
	}
}

func TestMkcalendarWrongPath(t *testing.T) {
	fc := &fakeCreator{}
	h := Wrap(passthrough(t), fc, storage.NewPaths(""))

	rec := httptest.NewRecorder()
	// Home set, not a calendar collection.
	req := httptest.NewRequest("MKCALENDAR", "/calendars/user/", strings.NewReader(fullBody))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if fc.called {
		t.Fatal("CreateCalendar should not be called for a non-calendar path")
	}
}

func TestMkcalendarValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"long-displayname", `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><D:displayname>` + strings.Repeat("x", maxDisplayNameLen+1) + `</D:displayname></D:prop></D:set></C:mkcalendar>`},
		{"long-description", `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><C:calendar-description>` + strings.Repeat("x", maxDescriptionLen+1) + `</C:calendar-description></D:prop></D:set></C:mkcalendar>`},
		{"bad-component", `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><C:supported-calendar-component-set><C:comp name="VEVIL"/></C:supported-calendar-component-set></D:prop></D:set></C:mkcalendar>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeCreator{}
			h := Wrap(passthrough(t), fc, storage.NewPaths(""))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("MKCALENDAR", "/calendars/user/work/", strings.NewReader(c.body))
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if fc.called {
				t.Fatal("CreateCalendar should not be called for an invalid body")
			}
		})
	}
}

func TestMkcalendarLimitReached(t *testing.T) {
	fc := &fakeCreator{err: storage.ErrTooManyCalendars}
	h := Wrap(passthrough(t), fc, storage.NewPaths(""))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("MKCALENDAR", "/calendars/user/work/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", rec.Code)
	}
}

func TestOptionsAllowAugmented(t *testing.T) {
	// next simulates go-webdav: sets an Allow header then writes 200.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "OPTIONS, PROPFIND, MKCOL")
		w.WriteHeader(http.StatusOK)
	})
	h := Wrap(next, &fakeCreator{}, storage.NewPaths(""))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/calendars/user/work/", nil)
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Allow"); !strings.Contains(got, "MKCALENDAR") {
		t.Errorf("Allow = %q, want it to contain MKCALENDAR", got)
	}
}

func TestPassthrough(t *testing.T) {
	hit := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	})
	h := Wrap(next, &fakeCreator{}, storage.NewPaths(""))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/calendars/user/work/", nil)
	h.ServeHTTP(rec, req)

	if !hit {
		t.Fatal("GET should pass through to next")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// passthrough returns a handler that fails the test if reached; MKCALENDAR
// requests must never fall through to the wrapped handler.
func passthrough(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request unexpectedly passed through to next handler")
	})
}
