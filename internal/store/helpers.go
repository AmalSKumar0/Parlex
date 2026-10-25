package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Text converts a string into a valid pgtype.Text.
func Text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// NullText converts an optional string into pgtype.Text.
func NullText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// PgUUID converts a uuid.UUID into a valid pgtype.UUID.
func PgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// NullPgUUID converts an optional uuid.UUID into pgtype.UUID.
func NullPgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{Valid: false}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

// Timestamptz converts a time.Time into a valid pgtype.Timestamptz.
func Timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// NullTimestamptz converts an optional time.Time into pgtype.Timestamptz.
func NullTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
