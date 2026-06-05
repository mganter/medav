package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func decode(t *testing.T, s string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cal
}

const timedEvent = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//medav//test//EN
BEGIN:VEVENT
UID:e1@medav
DTSTAMP:20260101T120000Z
DTSTART:20260601T100000Z
DTEND:20260601T113000Z
SUMMARY:Timed
END:VEVENT
END:VCALENDAR
`

const allDayRecurring = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//medav//test//EN
BEGIN:VEVENT
UID:e2@medav
DTSTAMP:20260101T120000Z
DTSTART;VALUE=DATE:20260601
RRULE:FREQ=WEEKLY;COUNT=5
SUMMARY:Standup
END:VEVENT
END:VCALENDAR
`

func TestExtractMetadataTimed(t *testing.T) {
	m := extractMetadata(decode(t, timedEvent))
	wantStart := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 1, 11, 30, 0, 0, time.UTC)
	if !m.dtStart.Equal(wantStart) {
		t.Errorf("dtStart = %v, want %v", m.dtStart, wantStart)
	}
	if !m.dtEnd.Equal(wantEnd) {
		t.Errorf("dtEnd = %v, want %v", m.dtEnd, wantEnd)
	}
	if m.hasRRULE {
		t.Errorf("hasRRULE = true, want false")
	}
}

func TestExtractMetadataAllDayRecurring(t *testing.T) {
	m := extractMetadata(decode(t, allDayRecurring))
	wantStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !m.dtStart.Equal(wantStart) {
		t.Errorf("dtStart = %v, want %v", m.dtStart, wantStart)
	}
	// DATE-only start with no DTEND/DURATION => treated as all-day (24h).
	wantEnd := wantStart.Add(24 * time.Hour)
	if !m.dtEnd.Equal(wantEnd) {
		t.Errorf("dtEnd = %v, want %v", m.dtEnd, wantEnd)
	}
	if !m.hasRRULE {
		t.Errorf("hasRRULE = false, want true")
	}
}
