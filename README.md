# medav

A small, single-user **CalDAV** server for personal calendars, backed by
PostgreSQL.

medav performs **no authentication of its own**. It is designed to run behind a
reverse proxy or ingress controller that authenticates requests and terminates
TLS; medav itself serves plain HTTP and trusts the upstream. It exposes one
static user principal and one or more calendars.

It builds on [`emersion/go-webdav`](https://github.com/emersion/go-webdav) for
the CalDAV protocol and [`jackc/pgx`](https://github.com/jackc/pgx) for
PostgreSQL. The dependency footprint is intentionally minimal — everything else
is the Go standard library.

## Features

- CalDAV server (RFC 4791): create / read / update / delete events (VEVENT) and
  tasks (VTODO).
- Calendar collections with `MKCALENDAR` support; a `default` calendar is
  seeded automatically.
- Raw iCalendar is stored verbatim (TZIDs preserved); start/end/component-type
  are indexed for efficient `time-range` REPORT queries.
- Strong ETags and `If-Match` / `If-None-Match` conditional writes.
- Embedded SQL migrations applied automatically on startup (advisory-locked).
- `/healthz` liveness endpoint.

## Configuration

All configuration is via environment variables (see [`.env.example`](.env.example)):

| Variable       | Required | Default  | Description |
| -------------- | -------- | -------- | ----------- |
| `DATABASE_URL` | yes      | —        | pgx/libpq connection string |
| `LISTEN_ADDR`  | no       | `:8080`  | HTTP listen address |
| `PREFIX`       | no       | `""`     | URL path prefix (no trailing slash); keep stable once data exists |
| `LOG_LEVEL`    | no       | `info`   | `debug` \| `info` \| `warn` \| `error` |

## Running locally

```sh
# 1. Start Postgres
docker compose up -d

# 2. Run the server (migrations + default calendar are created on startup)
DATABASE_URL=postgres://medav:medav@localhost:5432/medav?sslmode=disable \
  LOG_LEVEL=debug go run ./cmd/medav
```

The CalDAV root is then at `http://localhost:8080/`. Point a client at it (with
your proxy's auth in front in production):

- Principal:    `/principals/main/`
- Calendar home: `/calendars/`
- Default calendar: `/calendars/default/`

Autodiscovery via `/.well-known/caldav` is supported (it redirects to the
principal).

### Smoke test with curl

```sh
BASE=http://localhost:8080

# Discover the principal
curl -s -X PROPFIND "$BASE/" -H 'Depth: 0'

# Create an event
curl -s -X PUT "$BASE/calendars/default/test.ics" \
  -H 'Content-Type: text/calendar' --data-binary @- <<'ICS'
BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//medav//test//EN
BEGIN:VEVENT
UID:test-1@medav
DTSTAMP:20260101T120000Z
DTSTART:20260601T100000Z
DTEND:20260601T110000Z
SUMMARY:Hello medav
END:VEVENT
END:VCALENDAR
ICS

# Read it back and delete it
curl -s "$BASE/calendars/default/test.ics"
curl -s -X DELETE "$BASE/calendars/default/test.ics"
```

## Deployment

Container images are published as multi-arch (amd64/arm64) and built on
`gcr.io/distroless/static-nonroot` — no shell, no package manager, runs as an
unprivileged user. Because it runs as `nonroot`, bind to an unprivileged port
(the default `:8080`).

Put TLS and authentication in front of it (e.g. nginx/Caddy/Traefik basic auth,
or an ingress-controller auth annotation / forward-auth). All requests reaching
medav are treated as the single authorized user.

## Releasing

Releases are produced by [GoReleaser](https://goreleaser.com) on every `v*` tag
via GitHub Actions (`.github/workflows/release.yml`), publishing multi-arch
images to `ghcr.io/mganter/medav`.

Dry-run a build without publishing:

```sh
goreleaser release --snapshot --clean
```

## Development

```sh
go build ./...
go vet ./...
go test ./...          # fast unit tests, no external dependencies
```

### Integration tests

`internal/storage/integration_test.go` runs the full stack (HTTP handler →
storage backend → PostgreSQL) against a throwaway database started with
[testcontainers](https://golang.testcontainers.org/). It needs a running Docker
daemon and is gated behind the `integration` build tag, so the default
`go test ./...` does not require Docker:

```sh
go test -tags=integration ./internal/storage/
```
