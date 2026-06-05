package storage

import (
	"crypto/sha256"
	"encoding/hex"
)

// computeETag returns an unquoted strong validator derived from an object's raw
// iCalendar bytes. go-webdav adds the surrounding quotes when it writes the
// HTTP ETag header, and ConditionalMatch.ETag() returns the unquoted value, so
// the value stored and compared here is always unquoted.
func computeETag(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
