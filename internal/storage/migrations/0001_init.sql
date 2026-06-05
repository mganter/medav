-- A single sequence shared by calendar_objects and tombstones so every change
-- (write or delete) gets a globally monotonic change_seq.
CREATE SEQUENCE IF NOT EXISTS calendar_objects_change_seq;

CREATE TABLE IF NOT EXISTS calendars (
    path                    text PRIMARY KEY,
    name                    text        NOT NULL DEFAULT '',
    description             text        NOT NULL DEFAULT '',
    max_resource_size       bigint      NOT NULL DEFAULT 0,           -- 0 = unlimited
    supported_component_set text[]      NOT NULL DEFAULT '{VEVENT,VTODO}',
    ctag                    bigint      NOT NULL DEFAULT 0,
    created_at              timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS calendar_objects (
    path           text PRIMARY KEY,
    calendar_path  text        NOT NULL REFERENCES calendars(path) ON DELETE CASCADE,
    uid            text        NOT NULL,
    component_type text        NOT NULL,                              -- 'VEVENT' | 'VTODO' | ...
    etag           text        NOT NULL,                              -- unquoted sha256 hex
    data           bytea       NOT NULL,                              -- canonical raw .ics bytes
    content_length bigint      NOT NULL,
    dtstart        timestamptz,                                       -- UTC; NULL = no usable start
    dtend          timestamptz,                                       -- UTC; NULL = open-ended
    has_rrule      boolean     NOT NULL DEFAULT false,
    change_seq     bigint      NOT NULL DEFAULT nextval('calendar_objects_change_seq'),
    modified_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (calendar_path, uid)
);

CREATE INDEX IF NOT EXISTS idx_co_calendar      ON calendar_objects (calendar_path);
CREATE INDEX IF NOT EXISTS idx_co_calendar_comp ON calendar_objects (calendar_path, component_type);
CREATE INDEX IF NOT EXISTS idx_co_range         ON calendar_objects (calendar_path, dtstart, dtend);
CREATE INDEX IF NOT EXISTS idx_co_change_seq    ON calendar_objects (calendar_path, change_seq);

-- Tombstones give a future RFC 6578 sync-token middleware a place to record
-- deletions. Unused by the current handler but cheap to carry now.
CREATE TABLE IF NOT EXISTS tombstones (
    calendar_path text        NOT NULL,
    path          text        NOT NULL,
    change_seq    bigint      NOT NULL DEFAULT nextval('calendar_objects_change_seq'),
    deleted_at    timestamptz NOT NULL DEFAULT now()
);
