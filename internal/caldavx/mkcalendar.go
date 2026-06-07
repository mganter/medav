// Package caldavx supplies CalDAV protocol extensions that github.com/emersion/
// go-webdav does not implement, wrapping its handler with the missing pieces.
//
// go-webdav v0.7.0 has no MKCALENDAR support: its method switch falls through to
// 405 for any verb it does not know, and MKCALENDAR (RFC 4791 §5.3.1) is the verb
// standard clients (DAVx5, Apple Calendar, Thunderbird, Evolution) use to create
// a calendar collection. The library only exposes calendar creation via MKCOL.
// Wrap intercepts MKCALENDAR, routes it to the backend, and advertises the verb
// in the OPTIONS Allow header so clients that gate on it will attempt it.
package caldavx

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"

	"github.com/emersion/go-webdav/caldav"

	"medav/internal/storage"
)

// CalendarCreator is the slice of the storage backend MKCALENDAR needs. The
// concrete *storage.Backend already satisfies it.
type CalendarCreator interface {
	CreateCalendar(ctx context.Context, cal *caldav.Calendar) error
}

// mkcalendarReq mirrors the RFC 4791 §5.3.1 MKCALENDAR request body. Unlike a
// MKCOL body it carries no resourcetype (the verb itself implies a calendar).
// displayname lives in the DAV: namespace; the other properties in the caldav
// namespace. go-webdav's equivalent structs are unexported, so we declare our own.
type mkcalendarReq struct {
	XMLName     xml.Name `xml:"urn:ietf:params:xml:ns:caldav mkcalendar"`
	DisplayName string   `xml:"DAV: set>prop>displayname"`
	Description string   `xml:"urn:ietf:params:xml:ns:caldav set>prop>calendar-description"`
	Comps       []struct {
		Name string `xml:"name,attr"`
	} `xml:"urn:ietf:params:xml:ns:caldav set>prop>supported-calendar-component-set>comp"`
}

// Wrap returns next augmented with MKCALENDAR handling and OPTIONS Allow-header
// advertisement. All other requests pass through unchanged.
func Wrap(next http.Handler, backend CalendarCreator, paths storage.Paths) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "MKCALENDAR":
			handleMkcalendar(w, r, backend, paths)
		case http.MethodOptions:
			// Let go-webdav build the OPTIONS response, then add MKCALENDAR to
			// the Allow header it set just before the status is written.
			next.ServeHTTP(&allowAugmenter{ResponseWriter: w}, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func handleMkcalendar(w http.ResponseWriter, r *http.Request, backend CalendarCreator, paths storage.Paths) {
	// Reject calendar creation anywhere but a calendar-collection path, matching
	// go-webdav's Mkcol behaviour.
	if !paths.IsCalendar(r.URL.Path) {
		http.Error(w, "caldav: calendar creation not allowed at this location", http.StatusForbidden)
		return
	}

	cal := caldav.Calendar{Path: r.URL.Path}

	// The body is optional; clients may MKCALENDAR with no properties.
	var req mkcalendarReq
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		if !errors.Is(err, io.EOF) {
			http.Error(w, "caldav: invalid MKCALENDAR body: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		cal.Name = req.DisplayName
		cal.Description = req.Description
		for _, c := range req.Comps {
			if c.Name != "" {
				cal.SupportedComponentSet = append(cal.SupportedComponentSet, c.Name)
			}
		}
	}

	// CreateCalendar is idempotent (ON CONFLICT DO NOTHING) and emits its own
	// audit log line, so a repeated MKCALENDAR is a no-op 201.
	if err := backend.CreateCalendar(r.Context(), &cal); err != nil {
		http.Error(w, "caldav: failed to create calendar", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// allowAugmenter appends MKCALENDAR to the Allow header exactly once, just before
// the wrapped handler commits the response status.
type allowAugmenter struct {
	http.ResponseWriter
	wrote bool
}

func (a *allowAugmenter) WriteHeader(status int) {
	if !a.wrote {
		a.wrote = true
		if allow := a.Header().Get("Allow"); allow != "" {
			a.Header().Set("Allow", allow+", MKCALENDAR")
		}
	}
	a.ResponseWriter.WriteHeader(status)
}
