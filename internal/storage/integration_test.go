//go:build integration

// Package storage_test contains an end-to-end integration test that runs the
// real CalDAV handler against a throwaway PostgreSQL instance started with
// testcontainers. It exercises the whole stack — HTTP handler, storage backend,
// and database — the way a CalDAV client would.
//
// It requires a working Docker daemon and is excluded from the default test
// run. Enable it with:
//
//	go test -tags=integration ./internal/storage/
package storage_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/caldav"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"medav/internal/caldavx"
	"medav/internal/httpx"
	"medav/internal/storage"
)

// testServer holds the running HTTP server and a handle to the backing pool.
type testServer struct {
	base string
	pool *pgxpool.Pool
}

// startServer spins up a PostgreSQL container, applies migrations, seeds the
// default calendar, and serves the CalDAV handler from an httptest server. The
// container and pool are torn down via t.Cleanup.
func startServer(t *testing.T) *testServer {
	t.Helper()
	ctx := context.Background()

	pgc, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("medav"),
		postgres.WithUsername("medav"),
		postgres.WithPassword("medav"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(pgc) })

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	backend := storage.New(pool, "", 0, logger)
	if err := backend.EnsureDefaultCalendar(ctx); err != nil {
		t.Fatalf("seed default calendar: %v", err)
	}

	root := caldavx.Wrap(&caldav.Handler{Backend: backend, Prefix: ""}, backend, storage.NewPaths(""))
	handler := httpx.New(root, logger, httpx.Options{})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &testServer{base: srv.URL, pool: pool}
}

const sampleEvent = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//medav//test//EN
BEGIN:VEVENT
UID:%s
DTSTAMP:20260101T120000Z
DTSTART:20260601T100000Z
DTEND:20260601T110000Z
SUMMARY:Hello medav
END:VEVENT
END:VCALENDAR
`

func TestCalDAVIntegration(t *testing.T) {
	ts := startServer(t)

	t.Run("discovery", func(t *testing.T) {
		// current-user-principal at the root
		body := ts.propfind(t, "/", 0, `<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`)
		assertContains(t, body, "<href>/principals/</href>")

		// calendar-home-set on the principal
		body = ts.propfind(t, "/principals/", 0, `<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-home-set/></d:prop></d:propfind>`)
		assertContains(t, body, "/calendars/user/")

		// the seeded default calendar shows up under the home set
		body = ts.propfind(t, "/calendars/user/", 1, `<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:displayname/></d:prop></d:propfind>`)
		assertContains(t, body, "/calendars/user/default/")
		assertContains(t, body, "Personal")
	})

	t.Run("mkcalendar", func(t *testing.T) {
		// Create a calendar via MKCALENDAR, then confirm it surfaces under the
		// home set.
		body := `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
			`<D:set><D:prop><D:displayname>Work</D:displayname>` +
			`<C:calendar-description>My work calendar</C:calendar-description>` +
			`<C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>` +
			`</D:prop></D:set></C:mkcalendar>`
		resp, _ := ts.do(t, "MKCALENDAR", "/calendars/user/work/", body, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("MKCALENDAR status = %d, want 201", resp.StatusCode)
		}

		// Idempotent: a repeat is still 201, not an error.
		resp, _ = ts.do(t, "MKCALENDAR", "/calendars/user/work/", body, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("repeat MKCALENDAR status = %d, want 201", resp.StatusCode)
		}

		list := ts.propfind(t, "/calendars/user/", 1, `<d:propfind xmlns:d="DAV:"><d:prop><d:displayname/></d:prop></d:propfind>`)
		assertContains(t, list, "/calendars/user/work/")
		assertContains(t, list, "Work")

		// Wrong location (the home set itself) must be rejected.
		resp, _ = ts.do(t, "MKCALENDAR", "/calendars/user/", body, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("MKCALENDAR at home set status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("well-known redirect", func(t *testing.T) {
		// Don't follow the redirect; assert on the 308 + Location.
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		resp, err := client.Get(ts.base + "/.well-known/caldav")
		if err != nil {
			t.Fatalf("get well-known: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPermanentRedirect {
			t.Fatalf("well-known status = %d, want 308", resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/principals/" {
			t.Fatalf("well-known Location = %q, want /principals/", loc)
		}
	})

	t.Run("put-get-roundtrip", func(t *testing.T) {
		path := "/calendars/user/default/rt.ics"
		etag := ts.putEvent(t, path, "rt@medav", http.StatusCreated)
		if etag == "" {
			t.Fatal("PUT did not return an ETag")
		}

		resp, body := ts.do(t, http.MethodGet, path, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get("ETag"); got != etag {
			t.Fatalf("GET ETag = %q, want %q (must match PUT)", got, etag)
		}
		assertContains(t, body, "UID:rt@medav")
	})

	t.Run("conditional-create-conflict", func(t *testing.T) {
		path := "/calendars/user/default/cond.ics"
		ts.putEvent(t, path, "cond@medav", http.StatusCreated)

		// If-None-Match: * on an existing resource must fail with 412.
		resp, _ := ts.do(t, http.MethodPut, path, formatEvent("cond@medav"), http.Header{
			"Content-Type":  {"text/calendar"},
			"If-None-Match": {"*"},
		})
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("conditional create status = %d, want 412", resp.StatusCode)
		}
	})

	t.Run("uid-conflict", func(t *testing.T) {
		ts.putEvent(t, "/calendars/user/default/uid-a.ics", "shared-uid@medav", http.StatusCreated)
		// Same UID at a different path must be rejected (no-uid-conflict -> 409).
		resp, _ := ts.do(t, http.MethodPut, "/calendars/user/default/uid-b.ics", formatEvent("shared-uid@medav"), http.Header{
			"Content-Type": {"text/calendar"},
		})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("UID conflict status = %d, want 409", resp.StatusCode)
		}
	})

	t.Run("report-time-range", func(t *testing.T) {
		path := "/calendars/user/default/tr.ics"
		ts.putEvent(t, path, "tr@medav", http.StatusCreated)

		// The event runs 2026-06-01 10:00–11:00Z. An overlapping window matches.
		overlapping := ts.report(t, "/calendars/user/default/",
			timeRangeQuery("20260601T000000Z", "20260602T000000Z"))
		assertContains(t, overlapping, path)

		// A window in 2027 must not match.
		disjoint := ts.report(t, "/calendars/user/default/",
			timeRangeQuery("20270101T000000Z", "20270102T000000Z"))
		assertNotContains(t, disjoint, path)
	})

	t.Run("delete", func(t *testing.T) {
		path := "/calendars/user/default/del.ics"
		ts.putEvent(t, path, "del@medav", http.StatusCreated)

		resp, _ := ts.do(t, http.MethodDelete, path, "", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
		}
		resp, _ = ts.do(t, http.MethodGet, path, "", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET after delete = %d, want 404", resp.StatusCode)
		}
		resp, _ = ts.do(t, http.MethodDelete, path, "", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("second DELETE = %d, want 404", resp.StatusCode)
		}

		// A tombstone was recorded for the deleted object.
		var tombstones int
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM tombstones WHERE path = $1`, path).Scan(&tombstones); err != nil {
			t.Fatalf("query tombstones: %v", err)
		}
		if tombstones != 1 {
			t.Fatalf("tombstones for %s = %d, want 1", path, tombstones)
		}
	})
}

// --- request helpers ------------------------------------------------------

func formatEvent(uid string) string {
	return strings.Replace(sampleEvent, "%s", uid, 1)
}

func timeRangeQuery(start, end string) string {
	return `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">` +
		`<d:prop><d:getetag/></d:prop>` +
		`<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">` +
		`<c:time-range start="` + start + `" end="` + end + `"/>` +
		`</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`
}

func (ts *testServer) do(t *testing.T, method, path, body string, header http.Header) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.base+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func (ts *testServer) putEvent(t *testing.T, path, uid string, wantStatus int) string {
	t.Helper()
	resp, _ := ts.do(t, http.MethodPut, path, formatEvent(uid), http.Header{"Content-Type": {"text/calendar"}})
	if resp.StatusCode != wantStatus {
		t.Fatalf("PUT %s status = %d, want %d", path, resp.StatusCode, wantStatus)
	}
	return resp.Header.Get("ETag")
}

func (ts *testServer) propfind(t *testing.T, path string, depth int, body string) string {
	t.Helper()
	resp, out := ts.do(t, "PROPFIND", path, body, http.Header{
		"Content-Type": {"application/xml"},
		"Depth":        {strconv.Itoa(depth)},
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %s status = %d, want 207\n%s", path, resp.StatusCode, out)
	}
	return out
}

func (ts *testServer) report(t *testing.T, path, body string) string {
	t.Helper()
	resp, out := ts.do(t, "REPORT", path, body, http.Header{
		"Content-Type": {"application/xml"},
		"Depth":        {"1"},
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT %s status = %d, want 207\n%s", path, resp.StatusCode, out)
	}
	return out
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("response does not contain %q:\n%s", needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("response unexpectedly contains %q:\n%s", needle, haystack)
	}
}
