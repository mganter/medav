// Package storage implements the go-webdav caldav.Backend interface on top of
// PostgreSQL for a single-user CalDAV service. Authentication is intentionally
// out of scope: a reverse proxy / ingress controller authenticates upstream and
// this backend trusts the request, always serving the same static principal.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgUniqueViolation is the SQLSTATE returned by PostgreSQL on a unique
// constraint violation.
const pgUniqueViolation = "23505"

// ErrTooManyCalendars is returned by CreateCalendar when creating another
// calendar would exceed the configured cap. It is wrapped in a 507 HTTP error
// so go-webdav serialises it correctly, and callers can match it with
// errors.Is.
var ErrTooManyCalendars = errors.New("calendar limit reached")

// Backend stores calendars and calendar objects in PostgreSQL.
type Backend struct {
	pool         *pgxpool.Pool
	paths        Paths
	logger       *slog.Logger
	maxCalendars int
}

// New returns a Backend backed by the given pool and URL prefix. maxCalendars
// caps how many calendars may exist (zero or negative disables the cap). The
// logger is used for audit records of mutating operations; if nil, slog.Default
// is used.
func New(pool *pgxpool.Pool, prefix string, maxCalendars int, logger *slog.Logger) *Backend {
	if logger == nil {
		logger = slog.Default()
	}
	return &Backend{pool: pool, paths: NewPaths(prefix), logger: logger, maxCalendars: maxCalendars}
}

// EnsureDefaultCalendar creates the pre-seeded default calendar if it does not
// already exist. It is prefix-aware, which is why seeding lives here rather than
// in a SQL migration.
func (b *Backend) EnsureDefaultCalendar(ctx context.Context) error {
	_, err := b.pool.Exec(ctx, `
		INSERT INTO calendars (path, name, description, supported_component_set)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (path) DO NOTHING`,
		b.paths.DefaultCalendar(), "Personal", "Personal calendar", []string{"VEVENT", "VTODO"})
	if err != nil {
		return fmt.Errorf("seed default calendar: %w", err)
	}
	return nil
}

// --- principal / home set -------------------------------------------------

func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return b.paths.Principal(), nil
}

func (b *Backend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return b.paths.HomeSet(), nil
}

// --- calendars ------------------------------------------------------------

func (b *Backend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT path, name, description, max_resource_size, supported_component_set
		FROM calendars ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cals []caldav.Calendar
	for rows.Next() {
		var c caldav.Calendar
		if err := rows.Scan(&c.Path, &c.Name, &c.Description, &c.MaxResourceSize, &c.SupportedComponentSet); err != nil {
			return nil, err
		}
		cals = append(cals, c)
	}
	return cals, rows.Err()
}

func (b *Backend) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	var c caldav.Calendar
	err := b.pool.QueryRow(ctx, `
		SELECT path, name, description, max_resource_size, supported_component_set
		FROM calendars WHERE path = $1`, path).
		Scan(&c.Path, &c.Name, &c.Description, &c.MaxResourceSize, &c.SupportedComponentSet)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, webdav.NewHTTPError(http.StatusNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (b *Backend) CreateCalendar(ctx context.Context, cal *caldav.Calendar) error {
	comps := cal.SupportedComponentSet
	if len(comps) == 0 {
		comps = []string{"VEVENT", "VTODO"}
	}

	// Enforce the calendar cap atomically: the count subquery and the insert run
	// in a single statement, so concurrent MKCALENDARs cannot race past the
	// limit. A non-positive cap means unlimited.
	limit := b.maxCalendars
	if limit <= 0 {
		limit = math.MaxInt32
	}
	tag, err := b.pool.Exec(ctx, `
		INSERT INTO calendars (path, name, description, max_resource_size, supported_component_set)
		SELECT $1, $2, $3, $4, $5
		WHERE (SELECT count(*) FROM calendars) < $6
		ON CONFLICT (path) DO NOTHING`,
		cal.Path, cal.Name, cal.Description, cal.MaxResourceSize, comps, limit)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		b.logger.Info("audit: calendar created", "op", "MKCALENDAR",
			"path", sanitizeLog(cal.Path), "name", sanitizeLog(cal.Name))
		return nil
	}

	// No row was inserted: either the calendar already exists (idempotent
	// success) or the cap blocked it. Distinguish so the caller can return the
	// right status instead of a misleading 201.
	var exists bool
	if err := b.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM calendars WHERE path = $1)`, cal.Path).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return webdav.NewHTTPError(http.StatusInsufficientStorage, ErrTooManyCalendars)
	}
	return nil
}

// --- calendar objects -----------------------------------------------------

func (b *Backend) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	var (
		data       []byte
		etag       string
		modTime    time.Time
		contentLen int64
	)
	err := b.pool.QueryRow(ctx, `
		SELECT data, etag, modified_at, content_length
		FROM calendar_objects WHERE path = $1`, path).
		Scan(&data, &etag, &modTime, &contentLen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, webdav.NewHTTPError(http.StatusNotFound, err)
	}
	if err != nil {
		return nil, err
	}

	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		return nil, fmt.Errorf("decode stored calendar %q: %w", path, err)
	}
	return &caldav.CalendarObject{
		Path:          path,
		ModTime:       modTime,
		ContentLength: contentLen,
		ETag:          etag,
		Data:          cal,
	}, nil
}

func (b *Backend) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT path, data, etag, modified_at, content_length
		FROM calendar_objects WHERE calendar_path = $1 ORDER BY path`, path)
	if err != nil {
		return nil, err
	}
	return b.scanObjects(rows)
}

func (b *Backend) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	// Stage A: coarse, permissive SQL pre-filter over the indexed columns. It
	// must never drop an object the exact filter would keep, so NULL bounds are
	// treated as open-ended and recurring rows are never bounded by either end
	// of the window (their stored span covers only the first occurrence).
	sql := `SELECT path, data, etag, modified_at, content_length
		FROM calendar_objects WHERE calendar_path = $1`
	args := []any{path}

	if query != nil {
		for _, sub := range query.CompFilter.Comps {
			if sub.Name != "" {
				args = append(args, sub.Name)
				sql += fmt.Sprintf(" AND component_type = $%d", len(args))
			}
			if !sub.Start.IsZero() {
				args = append(args, sub.Start.UTC())
				// A recurring row's stored dtend covers only its first
				// occurrence; later occurrences may fall after the window
				// start, so recurring rows must never be excluded by the
				// lower bound (mirroring the upper bound below). Stage B does
				// the exact UNTIL/COUNT-aware expansion.
				sql += fmt.Sprintf(" AND (dtend IS NULL OR dtend > $%d OR has_rrule)", len(args))
			}
			if !sub.End.IsZero() {
				args = append(args, sub.End.UTC())
				sql += fmt.Sprintf(" AND (dtstart IS NULL OR dtstart < $%d OR has_rrule)", len(args))
			}
		}
	}
	sql += " ORDER BY path"

	rows, err := b.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	cos, err := b.scanObjects(rows)
	if err != nil {
		return nil, err
	}

	// Stage B: exact filtering, delegated to the library so RFC 4791 semantics
	// are not reimplemented. A nil query returns the full list unchanged.
	return caldav.Filter(query, cos)
}

func (b *Backend) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	compType, uid, err := caldav.ValidateCalendarObject(cal)
	if err != nil {
		return nil, caldav.NewPreconditionError(caldav.PreconditionValidCalendarObjectResource)
	}

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, fmt.Errorf("encode calendar: %w", err)
	}
	raw := buf.Bytes()
	etag := computeETag(raw)
	meta := extractMetadata(cal)
	calendarPath := CalendarOf(path)

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Inspect any existing object at this path to evaluate conditional headers.
	var existingETag string
	existErr := tx.QueryRow(ctx, `SELECT etag FROM calendar_objects WHERE path = $1 FOR UPDATE`, path).Scan(&existingETag)
	exists := !errors.Is(existErr, pgx.ErrNoRows)
	if existErr != nil && exists {
		return nil, existErr
	}

	if opts != nil {
		if opts.IfNoneMatch.IsWildcard() && exists {
			return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("resource already exists"))
		}
		if opts.IfMatch.IsSet() {
			want, err := opts.IfMatch.ETag()
			if err != nil {
				return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
			}
			if !exists || want != existingETag {
				return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("ETag precondition failed"))
			}
		}
	}

	var modTime time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO calendar_objects
			(path, calendar_path, uid, component_type, etag, data, content_length, dtstart, dtend, has_rrule, modified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (path) DO UPDATE SET
			uid = EXCLUDED.uid,
			component_type = EXCLUDED.component_type,
			etag = EXCLUDED.etag,
			data = EXCLUDED.data,
			content_length = EXCLUDED.content_length,
			dtstart = EXCLUDED.dtstart,
			dtend = EXCLUDED.dtend,
			has_rrule = EXCLUDED.has_rrule,
			change_seq = nextval('calendar_objects_change_seq'),
			modified_at = now()
		RETURNING modified_at`,
		path, calendarPath, uid, compType, etag, raw, int64(len(raw)),
		nullableTime(meta.dtStart), nullableTime(meta.dtEnd), meta.hasRRULE).
		Scan(&modTime)
	if err != nil {
		if isUniqueViolation(err) {
			// (calendar_path, uid) collision with a different resource.
			return nil, caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)
		}
		return nil, err
	}

	if err := bumpCtag(ctx, tx, calendarPath); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	b.logger.Info("audit: calendar object stored",
		"op", "PUT", "path", sanitizeLog(path), "calendar", sanitizeLog(calendarPath),
		"uid", sanitizeLog(uid), "etag", etag, "bytes", len(raw))

	return &caldav.CalendarObject{
		Path:          path,
		ModTime:       modTime,
		ContentLength: int64(len(raw)),
		ETag:          etag,
		Data:          cal,
	}, nil
}

func (b *Backend) DeleteCalendarObject(ctx context.Context, path string) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	calendarPath := CalendarOf(path)
	tag, err := tx.Exec(ctx, `DELETE FROM calendar_objects WHERE path = $1`, path)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return webdav.NewHTTPError(http.StatusNotFound, errors.New("calendar object not found"))
	}

	// Record a tombstone and bump the collection ctag so future sync logic and
	// clients observe the change.
	if _, err := tx.Exec(ctx,
		`INSERT INTO tombstones (calendar_path, path) VALUES ($1, $2)`, calendarPath, path); err != nil {
		return err
	}
	if err := bumpCtag(ctx, tx, calendarPath); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	b.logger.Info("audit: calendar object deleted", "op", "DELETE",
		"path", sanitizeLog(path), "calendar", sanitizeLog(calendarPath))
	return nil
}

// --- helpers --------------------------------------------------------------

// scanObjects materialises calendar object rows, decoding the stored raw bytes
// back into *ical.Calendar. It closes rows before returning.
func (b *Backend) scanObjects(rows pgx.Rows) ([]caldav.CalendarObject, error) {
	defer rows.Close()

	out := make([]caldav.CalendarObject, 0)
	for rows.Next() {
		var (
			path       string
			data       []byte
			etag       string
			modTime    time.Time
			contentLen int64
		)
		if err := rows.Scan(&path, &data, &etag, &modTime, &contentLen); err != nil {
			return nil, err
		}
		cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
		if err != nil {
			return nil, fmt.Errorf("decode stored calendar %q: %w", path, err)
		}
		out = append(out, caldav.CalendarObject{
			Path:          path,
			ModTime:       modTime,
			ContentLength: contentLen,
			ETag:          etag,
			Data:          cal,
		})
	}
	return out, rows.Err()
}

func bumpCtag(ctx context.Context, tx pgx.Tx, calendarPath string) error {
	_, err := tx.Exec(ctx, `UPDATE calendars SET ctag = ctag + 1 WHERE path = $1`, calendarPath)
	return err
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == pgUniqueViolation
}

// sanitizeLog strips control characters (including CR/LF) from an
// attacker-controlled value so it cannot forge or split audit log lines
// (CWE-117). The shipped slog TextHandler already quotes such values, so this is
// defence-in-depth that survives a future handler change.
func sanitizeLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
