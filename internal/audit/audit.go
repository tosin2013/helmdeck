// Package audit defines the helmdeck audit-log contract and ships a
// SQLite-backed Writer (ADR 010, ADR 013, PRD §8.8). Every API call,
// pack invocation, session lifecycle event, credential read, and policy
// change is recorded with actor attribution so the Audit Logs panel and
// downstream OpenTelemetry exporters (T510) have a single source of truth.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Severity is a closed set so the UI filter (§8.8) doesn't have to guess.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// EventType is a closed vocabulary covering every audit-emitting subsystem.
// New event types are added here and nowhere else so the UI filter list
// stays in sync with reality.
type EventType string

const (
	EventAPIRequest      EventType = "api_request"
	EventSessionCreate   EventType = "session_create"
	EventSessionTerminate EventType = "session_terminate"
	EventPackCall        EventType = "pack_call"
	EventLLMCall         EventType = "llm_call"
	EventMCPCall         EventType = "mcp_call"
	EventVaultRead       EventType = "vault_read"
	EventKeyRotated      EventType = "key_rotated"
	EventPolicyChanged   EventType = "policy_changed"
	EventLogin           EventType = "login"
)

// Entry is the row written to audit_log. Payload is JSON-encoded by the
// Writer; callers pass a Go map or struct.
type Entry struct {
	ID           int64     // populated by Writer.Write on success
	Timestamp    time.Time
	Severity     Severity
	EventType    EventType
	ActorSubject string // JWT sub claim
	ActorClient  string // JWT client claim (claude-code, claude-desktop, ...)
	SessionID    string
	Method       string
	Path         string
	StatusCode   int
	Payload      map[string]any
}

// Filter narrows Query results. All fields are optional.
type Filter struct {
	From         time.Time
	To           time.Time
	EventType    EventType
	SessionID    string
	ActorSubject string
	Severity     Severity
	Limit        int
}

// Writer is the persistence contract. The SQLite implementation lives in
// this package; a no-op implementation is supplied for tests that don't
// care about audit side effects.
type Writer interface {
	Write(ctx context.Context, e Entry) error
	Query(ctx context.Context, f Filter) ([]Entry, error)
}

// Discard is a no-op Writer used by tests and dev mode.
type Discard struct{}

// Write implements Writer.
func (Discard) Write(context.Context, Entry) error { return nil }

// Query implements Writer.
func (Discard) Query(context.Context, Filter) ([]Entry, error) { return nil, nil }

// SQLiteWriter persists entries into the audit_log table managed by
// internal/store.
type SQLiteWriter struct {
	db *sql.DB
}

// NewSQLiteWriter wraps an open *sql.DB. The caller owns the DB.
func NewSQLiteWriter(db *sql.DB) *SQLiteWriter {
	return &SQLiteWriter{db: db}
}

// Write implements Writer. Payload is JSON-encoded; nil payloads write NULL.
func (w *SQLiteWriter) Write(ctx context.Context, e Entry) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	var payloadJSON sql.NullString
	if e.Payload != nil {
		buf, err := json.Marshal(redact(e.Payload))
		if err != nil {
			return fmt.Errorf("audit: marshal payload: %w", err)
		}
		payloadJSON = sql.NullString{String: string(buf), Valid: true}
	}
	res, err := w.db.ExecContext(ctx, `
        INSERT INTO audit_log (ts, severity, event_type, actor_subject, actor_client, session_id, method, path, status_code, payload_json)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp.UTC().Format(time.RFC3339Nano),
		string(e.Severity),
		string(e.EventType),
		nullable(e.ActorSubject),
		nullable(e.ActorClient),
		nullable(e.SessionID),
		nullable(e.Method),
		nullable(e.Path),
		e.StatusCode,
		payloadJSON,
	)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	id, _ := res.LastInsertId()
	e.ID = id
	return nil
}

// Query implements Writer.
func (w *SQLiteWriter) Query(ctx context.Context, f Filter) ([]Entry, error) {
	var (
		conds []string
		args  []any
	)
	if !f.From.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, f.From.UTC().Format(time.RFC3339Nano))
	}
	if !f.To.IsZero() {
		conds = append(conds, "ts <= ?")
		args = append(args, f.To.UTC().Format(time.RFC3339Nano))
	}
	if f.EventType != "" {
		conds = append(conds, "event_type = ?")
		args = append(args, string(f.EventType))
	}
	if f.SessionID != "" {
		conds = append(conds, "session_id = ?")
		args = append(args, f.SessionID)
	}
	if f.ActorSubject != "" {
		conds = append(conds, "actor_subject = ?")
		args = append(args, f.ActorSubject)
	}
	if f.Severity != "" {
		conds = append(conds, "severity = ?")
		args = append(args, string(f.Severity))
	}

	q := `SELECT id, ts, severity, event_type, actor_subject, actor_client, session_id, method, path, status_code, payload_json FROM audit_log`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY id DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}

	rows, err := w.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			e            Entry
			ts           string
			actorSubject sql.NullString
			actorClient  sql.NullString
			sessionID    sql.NullString
			method       sql.NullString
			path         sql.NullString
			payloadJSON  sql.NullString
		)
		if err := rows.Scan(&e.ID, &ts, &e.Severity, &e.EventType, &actorSubject, &actorClient, &sessionID, &method, &path, &e.StatusCode, &payloadJSON); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		e.ActorSubject = actorSubject.String
		e.ActorClient = actorClient.String
		e.SessionID = sessionID.String
		e.Method = method.String
		e.Path = path.String
		if payloadJSON.Valid {
			_ = json.Unmarshal([]byte(payloadJSON.String), &e.Payload)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// redact replaces sensitive header / field values with the literal
// "[redacted]" before the payload is marshaled. Operators see that a
// credential was used without seeing its value (ADR 010).
func redact(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if isSensitiveKey(k) {
			out[k] = "[redacted]"
			continue
		}
		switch vv := v.(type) {
		case map[string]any:
			out[k] = redact(vv)
		default:
			out[k] = vv
		}
	}
	return out
}

// SensitiveKeys is the closed set of payload field names that audit
// redaction always strips. Add new keys here as new subsystems land.
var SensitiveKeys = map[string]struct{}{
	"authorization": {},
	"cookie":        {},
	"set-cookie":    {},
	"x-api-key":     {},
	"api_key":       {},
	"apikey":        {},
	"password":      {},
	"secret":        {},
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"private_key":   {},
}

func isSensitiveKey(k string) bool {
	_, ok := SensitiveKeys[strings.ToLower(k)]
	return ok
}

func nullable(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
