package storage

import (
	"time"

	"github.com/emersion/go-ical"
)

// metadata holds the indexed columns extracted from a calendar object so that
// time-range REPORT queries can be coarsely filtered in SQL.
type metadata struct {
	dtStart  time.Time // zero => NULL (no usable start, e.g. a VTODO without DTSTART)
	dtEnd    time.Time // zero => NULL (open-ended)
	hasRRULE bool
}

// extractMetadata walks the non-VTIMEZONE components of a calendar and returns
// the widest [min start, max end] span together with whether any component
// carries a recurrence rule. All times are normalised to UTC; the stored raw
// bytes are never rewritten, so the original TZID information is preserved.
func extractMetadata(cal *ical.Calendar) metadata {
	var m metadata
	for _, comp := range cal.Children {
		if comp.Name == ical.CompTimezone {
			continue
		}

		start, err := comp.Props.DateTime(ical.PropDateTimeStart, time.UTC)
		if err != nil || start.IsZero() {
			// No usable start (e.g. VTODO with only DUE). Recurrence still
			// matters for query handling below.
			if comp.Props.Get(ical.PropRecurrenceRule) != nil {
				m.hasRRULE = true
			}
			continue
		}
		if m.dtStart.IsZero() || start.Before(m.dtStart) {
			m.dtStart = start
		}

		end := componentEnd(comp, start)
		if m.dtEnd.IsZero() || end.After(m.dtEnd) {
			m.dtEnd = end
		}

		if comp.Props.Get(ical.PropRecurrenceRule) != nil {
			m.hasRRULE = true
		}
	}
	return m
}

// componentEnd mirrors ical.Event.DateTimeEnd for an arbitrary component: it
// prefers DTEND, falls back to DTSTART+DURATION, treats DATE-only starts as
// all-day (24h), and otherwise returns the start (a point in time).
func componentEnd(comp *ical.Component, start time.Time) time.Time {
	if prop := comp.Props.Get(ical.PropDateTimeEnd); prop != nil {
		if end, err := prop.DateTime(time.UTC); err == nil && !end.IsZero() {
			return end
		}
	}

	startProp := comp.Props.Get(ical.PropDateTimeStart)
	var dur time.Duration
	if durProp := comp.Props.Get(ical.PropDuration); durProp != nil {
		if d, err := durProp.Duration(); err == nil {
			dur = d
		}
	} else if startProp != nil && startProp.ValueType() == ical.ValueDate {
		dur = 24 * time.Hour
	}
	return start.Add(dur)
}
