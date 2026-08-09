// Package store persists server state in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"codex-claude-monitor/internal/model"

	_ "modernc.org/sqlite"
)

const (
	// AgentOnlineWindow is the maximum time since the server received a report
	// for an agent to be counted as online.
	AgentOnlineWindow = 45 * time.Second
	// TaskTTL removes orphaned tasks whose last heartbeat is too old.
	TaskTTL = 15 * time.Minute
	// ProviderFreshWindow is the age after which a provider sample is stale.
	ProviderFreshWindow = 5 * time.Minute
)

var (
	ErrInvalidToken  = errors.New("invalid token")
	ErrTokenNotFound = errors.New("token not found")
	ErrStaleReport   = errors.New("report is not newer than the stored report")
)

type TokenScope string

const (
	ScopeAgentWrite  TokenScope = "agent:write"
	ScopeDisplayRead TokenScope = "display:read"
)

func (s TokenScope) Valid() bool {
	return s == ScopeAgentWrite || s == ScopeDisplayRead
}

type CreateTokenRequest struct {
	Scope   TokenScope
	AgentID string
	Label   string
}

// TokenRecord intentionally contains no bearer token or digest, so it is safe
// to return from list operations.
type TokenRecord struct {
	ID         string     `json:"id"`
	Scope      TokenScope `json:"scope"`
	AgentID    string     `json:"agentId,omitempty"`
	Label      string     `json:"label,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// CreatedToken is returned only by CreateToken. RawToken cannot be recovered
// from the database and must be shown to the operator exactly once.
type CreatedToken struct {
	TokenRecord
	RawToken string `json:"token"`
}

type Store struct {
	db *sql.DB
}

// Open opens a SQLite database, enables WAL and foreign keys, and applies all
// schema migrations. The special path ":memory:" is supported for tests.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}

	dsn := sqliteDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection gives deterministic in-memory behavior and still
	// permits concurrent HTTP callers. WAL serializes writes at the database.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.configure(ctx, path); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func sqliteDSN(path string) string {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	// URL form avoids interpreting Windows drive-letter colons as parameters.
	normalized := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	u := &url.URL{Scheme: "file", Path: normalized}
	return u.String()
}

func (s *Store) configure(ctx context.Context, path string) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	}
	if path != ":memory:" {
		pragmas = append(pragmas, "PRAGMA journal_mode = WAL")
	}
	for _, statement := range pragmas {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite (%s): %w", statement, err)
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at_ns INTEGER NOT NULL
)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	var version int
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err = tx.ExecContext(ctx, migrations[i]); err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at_ns) VALUES (?, ?)", i+1, time.Now().UTC().UnixNano())
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

var migrations = []string{`
CREATE TABLE tokens (
    id TEXT PRIMARY KEY,
    digest BLOB NOT NULL UNIQUE CHECK(length(digest) = 32),
    scope TEXT NOT NULL CHECK(scope IN ('agent:write', 'display:read')),
    agent_id TEXT,
    label TEXT NOT NULL DEFAULT '',
    created_at_ns INTEGER NOT NULL,
    revoked_at_ns INTEGER,
    last_used_at_ns INTEGER,
    CHECK((scope = 'agent:write' AND agent_id IS NOT NULL AND length(agent_id) > 0)
       OR (scope = 'display:read' AND agent_id IS NULL))
);
CREATE INDEX tokens_scope_active_idx ON tokens(scope, revoked_at_ns);

CREATE TABLE agents (
    agent_id TEXT PRIMARY KEY,
    report_sent_at_ns INTEGER NOT NULL,
    last_report_at_ns INTEGER NOT NULL
);

CREATE TABLE provider_reports (
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK(provider IN ('codex', 'claude')),
    observed_at_ns INTEGER NOT NULL,
    auth_state TEXT NOT NULL DEFAULT '',
    plan TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    windows_json BLOB NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(agent_id, provider)
);
CREATE INDEX provider_latest_idx ON provider_reports(provider, observed_at_ns DESC);

CREATE TABLE active_tasks (
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK(provider IN ('codex', 'claude')),
    kind TEXT NOT NULL CHECK(kind IN ('main', 'sub')),
    session_id TEXT NOT NULL,
    started_at_ns INTEGER NOT NULL,
    last_seen_at_ns INTEGER NOT NULL,
    PRIMARY KEY(agent_id, provider, session_id)
);
CREATE INDEX active_tasks_live_idx ON active_tasks(last_seen_at_ns);
`}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) CreateToken(ctx context.Context, req CreateTokenRequest) (CreatedToken, error) {
	if !req.Scope.Valid() {
		return CreatedToken{}, fmt.Errorf("invalid token scope %q", req.Scope)
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.Scope == ScopeAgentWrite && req.AgentID == "" {
		return CreatedToken{}, errors.New("agent:write token requires an agent ID")
	}
	if req.Scope == ScopeDisplayRead && req.AgentID != "" {
		return CreatedToken{}, errors.New("display:read token cannot be bound to an agent")
	}
	if req.AgentID != "" && !validAgentID(req.AgentID) {
		return CreatedToken{}, errors.New("agent ID must be 1-64 characters using letters, digits, dot, underscore, or hyphen")
	}
	if len(req.Label) > 200 {
		return CreatedToken{}, errors.New("token label is too long")
	}

	rawSecret, err := randomString(32)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate bearer token: %w", err)
	}
	rawToken := "qmon_" + rawSecret
	idPart, err := randomString(12)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate token ID: %w", err)
	}
	id := "tok_" + idPart
	digest := sha256.Sum256([]byte(rawToken))
	createdAt := time.Now().UTC()

	var agentID any
	if req.AgentID != "" {
		agentID = req.AgentID
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO tokens(id, digest, scope, agent_id, label, created_at_ns)
VALUES (?, ?, ?, ?, ?, ?)`, id, digest[:], req.Scope, agentID, req.Label, createdAt.UnixNano())
	if err != nil {
		return CreatedToken{}, fmt.Errorf("insert token: %w", err)
	}
	record := TokenRecord{
		ID:        id,
		Scope:     req.Scope,
		AgentID:   req.AgentID,
		Label:     req.Label,
		CreatedAt: createdAt,
	}
	return CreatedToken{TokenRecord: record, RawToken: rawToken}, nil
}

func validAgentID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func randomString(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Store) RevokeToken(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE tokens SET revoked_at_ns = ? WHERE id = ? AND revoked_at_ns IS NULL",
		time.Now().UTC().UnixNano(), id)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revoke result: %w", err)
	}
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

func (s *Store) ListTokens(ctx context.Context) ([]TokenRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, scope, COALESCE(agent_id, ''), label, created_at_ns, revoked_at_ns, last_used_at_ns
FROM tokens ORDER BY created_at_ns, id`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	records := make([]TokenRecord, 0)
	for rows.Next() {
		var rec TokenRecord
		var created int64
		var revoked, lastUsed sql.NullInt64
		if err := rows.Scan(&rec.ID, &rec.Scope, &rec.AgentID, &rec.Label, &created, &revoked, &lastUsed); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		rec.CreatedAt = time.Unix(0, created).UTC()
		if revoked.Valid {
			t := time.Unix(0, revoked.Int64).UTC()
			rec.RevokedAt = &t
		}
		if lastUsed.Valid {
			t := time.Unix(0, lastUsed.Int64).UTC()
			rec.LastUsedAt = &t
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tokens: %w", err)
	}
	return records, nil
}

// AuthenticateToken compares SHA-256 digests in constant time. It traverses
// every active token in the requested scope to avoid a position-dependent
// early return. The raw bearer value is never persisted or logged.
func (s *Store) AuthenticateToken(ctx context.Context, raw string, scope TokenScope) (TokenRecord, error) {
	if !scope.Valid() || len(raw) < 16 || len(raw) > 256 {
		return TokenRecord{}, ErrInvalidToken
	}
	want := sha256.Sum256([]byte(raw))
	rows, err := s.db.QueryContext(ctx, `
SELECT id, digest, scope, COALESCE(agent_id, ''), label, created_at_ns, last_used_at_ns
FROM tokens WHERE scope = ? AND revoked_at_ns IS NULL`, scope)
	if err != nil {
		return TokenRecord{}, fmt.Errorf("query active tokens: %w", err)
	}

	var matched TokenRecord
	matchedCount := 0
	compared := false
	for rows.Next() {
		var rec TokenRecord
		var digest []byte
		var created int64
		var lastUsed sql.NullInt64
		if err := rows.Scan(&rec.ID, &digest, &rec.Scope, &rec.AgentID, &rec.Label, &created, &lastUsed); err != nil {
			_ = rows.Close()
			return TokenRecord{}, fmt.Errorf("scan active token: %w", err)
		}
		compared = true
		match := subtle.ConstantTimeCompare(want[:], digest)
		matchedCount += match
		if match == 1 {
			rec.CreatedAt = time.Unix(0, created).UTC()
			if lastUsed.Valid {
				t := time.Unix(0, lastUsed.Int64).UTC()
				rec.LastUsedAt = &t
			}
			matched = rec
		}
	}
	if err := rows.Close(); err != nil {
		return TokenRecord{}, fmt.Errorf("close active token rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return TokenRecord{}, fmt.Errorf("iterate active tokens: %w", err)
	}
	if !compared {
		var dummy [sha256.Size]byte
		_ = subtle.ConstantTimeCompare(want[:], dummy[:])
	}
	if matchedCount != 1 {
		return TokenRecord{}, ErrInvalidToken
	}

	usedAt := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, "UPDATE tokens SET last_used_at_ns = ? WHERE id = ?", usedAt.UnixNano(), matched.ID); err != nil {
		return TokenRecord{}, fmt.Errorf("update token use: %w", err)
	}
	matched.LastUsedAt = &usedAt
	return matched, nil
}

// ReplaceAgentReport atomically replaces the complete provider and task state
// for one agent. Reports are ordered by their agent-generated SentAt value.
func (s *Store) ReplaceAgentReport(ctx context.Context, report model.AgentReport, receivedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin report transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
INSERT INTO agents(agent_id, report_sent_at_ns, last_report_at_ns)
VALUES (?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET
    report_sent_at_ns = excluded.report_sent_at_ns,
    last_report_at_ns = excluded.last_report_at_ns
WHERE excluded.report_sent_at_ns > agents.report_sent_at_ns`,
		report.AgentID, report.SentAt.UTC().UnixNano(), receivedAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("upsert agent report: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read report upsert result: %w", err)
	}
	if n == 0 {
		return ErrStaleReport
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM provider_reports WHERE agent_id = ?", report.AgentID); err != nil {
		return fmt.Errorf("clear provider reports: %w", err)
	}
	for provider, providerReport := range report.Providers {
		windows, err := json.Marshal(providerReport.Windows)
		if err != nil {
			return fmt.Errorf("encode %s windows: %w", provider, err)
		}
		observedAt := int64(0)
		if !providerReport.ObservedAt.IsZero() {
			observedAt = providerReport.ObservedAt.UTC().UnixNano()
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO provider_reports(
    agent_id, provider, observed_at_ns, auth_state, plan, source, windows_json, error_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			report.AgentID, provider, observedAt, providerReport.AuthState,
			providerReport.Plan, providerReport.Source, windows, providerReport.ErrorCode)
		if err != nil {
			return fmt.Errorf("insert %s report: %w", provider, err)
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM active_tasks WHERE agent_id = ?", report.AgentID); err != nil {
		return fmt.Errorf("clear active tasks: %w", err)
	}
	for _, task := range report.ActiveTasks {
		_, err := tx.ExecContext(ctx, `
INSERT INTO active_tasks(agent_id, provider, kind, session_id, started_at_ns, last_seen_at_ns)
VALUES (?, ?, ?, ?, ?, ?)`, report.AgentID, task.Provider, task.Kind, task.SessionID,
			task.StartedAt.UTC().UnixNano(), task.LastSeenAt.UTC().UnixNano())
		if err != nil {
			return fmt.Errorf("insert active task: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit agent report: %w", err)
	}
	return nil
}

func (s *Store) BuildDisplaySnapshot(ctx context.Context, now time.Time) (model.DisplaySnapshot, error) {
	now = now.UTC()
	snapshot := model.DisplaySnapshot{
		SchemaVersion: model.SchemaVersion,
		GeneratedAt:   now,
		Providers: map[model.ProviderName]model.ProviderSnapshot{
			model.ProviderCodex:  unavailableProvider(),
			model.ProviderClaude: unavailableProvider(),
		},
		Warnings: make([]string, 0),
	}

	if err := s.loadAgentSummary(ctx, now, &snapshot); err != nil {
		return model.DisplaySnapshot{}, err
	}
	for _, provider := range []model.ProviderName{model.ProviderCodex, model.ProviderClaude} {
		providerSnapshot, found, err := s.loadLatestProvider(ctx, provider, now)
		if err != nil {
			return model.DisplaySnapshot{}, err
		}
		if found {
			snapshot.Providers[provider] = providerSnapshot
		}
		providerState := snapshot.Providers[provider]
		if providerState.LoginRequired {
			snapshot.Warnings = append(snapshot.Warnings, string(provider)+"_login_required")
		} else if providerState.Freshness != model.FreshnessFresh {
			snapshot.Warnings = append(snapshot.Warnings, string(provider)+"_"+string(providerState.Freshness))
		}
	}
	if err := s.loadTaskSummary(ctx, now, &snapshot); err != nil {
		return model.DisplaySnapshot{}, err
	}
	return snapshot, nil
}

func unavailableProvider() model.ProviderSnapshot {
	return model.ProviderSnapshot{
		Freshness: model.FreshnessUnavailable,
		Windows:   model.ProviderWindows{},
	}
}

func (s *Store) loadAgentSummary(ctx context.Context, now time.Time, snapshot *model.DisplaySnapshot) error {
	threshold := now.Add(-AgentOnlineWindow).UnixNano()
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(CASE WHEN last_report_at_ns >= ? THEN 1 ELSE 0 END), 0)
FROM agents`, threshold).Scan(&snapshot.Agents.Total, &snapshot.Agents.Online); err != nil {
		return fmt.Errorf("load agent summary: %w", err)
	}
	return nil
}

func (s *Store) loadLatestProvider(ctx context.Context, provider model.ProviderName, now time.Time) (model.ProviderSnapshot, bool, error) {
	var observed int64
	var authState, plan, source, errorCode string
	var windowsJSON []byte
	err := s.db.QueryRowContext(ctx, `
SELECT observed_at_ns, auth_state, plan, source, windows_json, error_code
FROM provider_reports
WHERE provider = ? AND observed_at_ns > 0
	ORDER BY observed_at_ns DESC, agent_id ASC
	LIMIT 1`, provider).Scan(&observed, &authState, &plan, &source, &windowsJSON, &errorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return unavailableProvider(), false, nil
	}
	if err != nil {
		return model.ProviderSnapshot{}, false, fmt.Errorf("load latest %s report: %w", provider, err)
	}

	var windows model.ProviderWindows
	if err := json.Unmarshal(windowsJSON, &windows); err != nil {
		return model.ProviderSnapshot{}, false, fmt.Errorf("decode latest %s windows: %w", provider, err)
	}
	observedAt := time.Unix(0, observed).UTC()
	candidate := model.ProviderSnapshot{
		ObservedAt:    &observedAt,
		LoginRequired: authState == "unauthenticated" || errorCode == "not_authenticated",
		Plan:          plan,
		Source:        source,
		Windows:       windows,
	}
	if windows.FiveHour == nil && windows.SevenDay == nil {
		candidate.Freshness = model.FreshnessUnavailable
		return candidate, true, nil
	}
	candidate.Freshness = model.FreshnessFresh
	if now.Sub(observedAt) > ProviderFreshWindow {
		candidate.Freshness = model.FreshnessStale
	}
	return candidate, true, nil
}

func (s *Store) loadTaskSummary(ctx context.Context, now time.Time, snapshot *model.DisplaySnapshot) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.provider, t.kind
FROM active_tasks t
JOIN agents a ON a.agent_id = t.agent_id
WHERE a.last_report_at_ns >= ? AND t.last_seen_at_ns >= ?`,
		now.Add(-AgentOnlineWindow).UnixNano(), now.Add(-TaskTTL).UnixNano())
	if err != nil {
		return fmt.Errorf("load active tasks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var provider model.ProviderName
		var kind model.TaskKind
		if err := rows.Scan(&provider, &kind); err != nil {
			return fmt.Errorf("scan active task: %w", err)
		}
		var count *model.TaskCount
		switch provider {
		case model.ProviderCodex:
			count = &snapshot.Tasks.Codex
		case model.ProviderClaude:
			count = &snapshot.Tasks.Claude
		default:
			continue
		}
		switch kind {
		case model.TaskMain:
			count.Main++
			snapshot.Tasks.Total.Main++
		case model.TaskSub:
			count.Sub++
			snapshot.Tasks.Total.Sub++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate active tasks: %w", err)
	}
	return nil
}
