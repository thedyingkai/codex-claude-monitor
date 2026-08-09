package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-claude-monitor/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return s
}

func TestOpenMigratesAndEnablesWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "monitor.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	var migrationVersion int
	if err := s.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&migrationVersion); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if migrationVersion != len(migrations) {
		t.Fatalf("migration version = %d, want %d", migrationVersion, len(migrations))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Reopening proves migrations are idempotent and the on-disk schema is
	// usable by a later server process.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer s.Close()
}

func TestTokenLifecycleAndNoRawValueAtRest(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	created, err := s.CreateToken(ctx, CreateTokenRequest{
		Scope: ScopeAgentWrite, AgentID: "desktop-1", Label: "home workstation",
	})
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	if len(created.RawToken) < 40 {
		t.Fatalf("raw token is unexpectedly short: %d", len(created.RawToken))
	}
	if created.AgentID != "desktop-1" || created.Scope != ScopeAgentWrite {
		t.Fatalf("created record = %+v", created.TokenRecord)
	}

	wantDigest := sha256.Sum256([]byte(created.RawToken))
	var gotDigest []byte
	if err := s.db.QueryRowContext(ctx, "SELECT digest FROM tokens WHERE id = ?", created.ID).Scan(&gotDigest); err != nil {
		t.Fatalf("read digest: %v", err)
	}
	if string(gotDigest) != string(wantDigest[:]) {
		t.Fatal("stored digest does not match SHA-256(raw token)")
	}

	records, err := s.ListTokens(ctx)
	if err != nil {
		t.Fatalf("ListTokens() error = %v", err)
	}
	if len(records) != 1 || records[0].ID != created.ID {
		t.Fatalf("ListTokens() = %+v", records)
	}
	encoded, _ := json.Marshal(records)
	if string(encoded) == "" || contains(string(encoded), created.RawToken) {
		t.Fatalf("listed token leaked raw bearer value: %s", encoded)
	}

	authenticated, err := s.AuthenticateToken(ctx, created.RawToken, ScopeAgentWrite)
	if err != nil {
		t.Fatalf("AuthenticateToken() error = %v", err)
	}
	if authenticated.ID != created.ID || authenticated.LastUsedAt == nil {
		t.Fatalf("authenticated record = %+v", authenticated)
	}
	if _, err := s.AuthenticateToken(ctx, created.RawToken, ScopeDisplayRead); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong-scope AuthenticateToken() error = %v, want ErrInvalidToken", err)
	}
	if _, err := s.AuthenticateToken(ctx, created.RawToken+"x", ScopeAgentWrite); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("bad AuthenticateToken() error = %v, want ErrInvalidToken", err)
	}

	if err := s.RevokeToken(ctx, created.ID); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
	if _, err := s.AuthenticateToken(ctx, created.RawToken, ScopeAgentWrite); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked AuthenticateToken() error = %v, want ErrInvalidToken", err)
	}
	records, err = s.ListTokens(ctx)
	if err != nil || records[0].RevokedAt == nil {
		t.Fatalf("revoked ListTokens() = %+v, %v", records, err)
	}
}

func TestCreateTokenScopeBinding(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tests := []CreateTokenRequest{
		{Scope: ScopeAgentWrite},
		{Scope: ScopeAgentWrite, AgentID: "bad agent id"},
		{Scope: ScopeDisplayRead, AgentID: "not-allowed"},
		{Scope: "admin"},
	}
	for _, test := range tests {
		if _, err := s.CreateToken(ctx, test); err == nil {
			t.Fatalf("CreateToken(%+v) unexpectedly succeeded", test)
		}
	}
}

func TestReplaceReportRejectsOldAndReplacesCompleteState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	first := testReport("desktop-1", now.Add(-10*time.Second))
	first.Providers[model.ProviderCodex] = providerReport(now.Add(-30*time.Second), 15)
	first.ActiveTasks = []model.ActiveTask{
		task(model.ProviderCodex, model.TaskMain, "session-main", now.Add(-time.Minute)),
		task(model.ProviderClaude, model.TaskSub, "session-sub", now.Add(-time.Minute)),
	}
	if err := s.ReplaceAgentReport(ctx, first, now.Add(-9*time.Second)); err != nil {
		t.Fatalf("first ReplaceAgentReport() error = %v", err)
	}

	if err := s.ReplaceAgentReport(ctx, first, now); !errors.Is(err, ErrStaleReport) {
		t.Fatalf("duplicate ReplaceAgentReport() error = %v, want ErrStaleReport", err)
	}
	older := first
	older.SentAt = first.SentAt.Add(-time.Second)
	if err := s.ReplaceAgentReport(ctx, older, now); !errors.Is(err, ErrStaleReport) {
		t.Fatalf("older ReplaceAgentReport() error = %v, want ErrStaleReport", err)
	}

	second := testReport("desktop-1", now)
	second.Providers[model.ProviderClaude] = providerReport(now, 25)
	second.ActiveTasks = []model.ActiveTask{
		task(model.ProviderClaude, model.TaskMain, "replacement", now),
	}
	if err := s.ReplaceAgentReport(ctx, second, now); err != nil {
		t.Fatalf("replacement ReplaceAgentReport() error = %v", err)
	}

	var codexProviders int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_reports WHERE agent_id = ? AND provider = ?", "desktop-1", model.ProviderCodex).Scan(&codexProviders); err != nil {
		t.Fatal(err)
	}
	if codexProviders != 0 {
		t.Fatalf("old provider rows remain: %d", codexProviders)
	}
	var tasks int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM active_tasks WHERE agent_id = ?", "desktop-1").Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 {
		t.Fatalf("task rows = %d, want 1", tasks)
	}
}

func TestBuildDisplaySnapshotAggregatesLatestAndLiveTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	a := testReport("agent-a", now.Add(-20*time.Second))
	a.Providers[model.ProviderCodex] = providerReport(now.Add(-4*time.Minute), 40)
	a.Providers[model.ProviderClaude] = providerReport(now.Add(-6*time.Minute), 50)
	a.ActiveTasks = []model.ActiveTask{
		task(model.ProviderCodex, model.TaskMain, "shared-session", now.Add(-time.Minute)),
		task(model.ProviderCodex, model.TaskSub, "codex-sub", now.Add(-TaskTTL-time.Second)), // expired
	}
	if err := s.ReplaceAgentReport(ctx, a, now.Add(-20*time.Second)); err != nil {
		t.Fatal(err)
	}

	b := testReport("agent-b", now.Add(-10*time.Second))
	b.Providers[model.ProviderCodex] = providerReport(now.Add(-time.Minute), 10)
	b.ActiveTasks = []model.ActiveTask{
		task(model.ProviderCodex, model.TaskMain, "shared-session", now.Add(-time.Minute)),
		task(model.ProviderClaude, model.TaskSub, "claude-sub", now.Add(-time.Minute)),
	}
	if err := s.ReplaceAgentReport(ctx, b, now.Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}

	offline := testReport("agent-offline", now.Add(-time.Minute))
	offline.ActiveTasks = []model.ActiveTask{
		task(model.ProviderClaude, model.TaskMain, "offline-task", now.Add(-time.Minute)),
	}
	if err := s.ReplaceAgentReport(ctx, offline, now.Add(-AgentOnlineWindow-time.Second)); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.BuildDisplaySnapshot(ctx, now)
	if err != nil {
		t.Fatalf("BuildDisplaySnapshot() error = %v", err)
	}
	if snapshot.SchemaVersion != model.SchemaVersion || !snapshot.GeneratedAt.Equal(now) {
		t.Fatalf("snapshot metadata = %+v", snapshot)
	}
	if snapshot.Agents.Total != 3 || snapshot.Agents.Online != 2 {
		t.Fatalf("agent summary = %+v", snapshot.Agents)
	}
	codex := snapshot.Providers[model.ProviderCodex]
	if codex.Freshness != model.FreshnessFresh || codex.Windows.FiveHour == nil || codex.Windows.FiveHour.UsedPercent != 10 {
		t.Fatalf("latest codex = %+v", codex)
	}
	claude := snapshot.Providers[model.ProviderClaude]
	if claude.Freshness != model.FreshnessStale || claude.Windows.FiveHour == nil {
		t.Fatalf("latest claude = %+v", claude)
	}
	if snapshot.Tasks.Codex.Main != 2 || snapshot.Tasks.Codex.Sub != 0 || snapshot.Tasks.Claude.Sub != 1 || snapshot.Tasks.Claude.Main != 0 {
		t.Fatalf("task summary = %+v", snapshot.Tasks)
	}
	if snapshot.Tasks.Total.Main != 2 || snapshot.Tasks.Total.Sub != 1 {
		t.Fatalf("total task summary = %+v", snapshot.Tasks.Total)
	}
	wantWarnings := []string{"claude_stale"}
	if !equalStrings(snapshot.Warnings, wantWarnings) {
		t.Fatalf("warnings = %v, want %v", snapshot.Warnings, wantWarnings)
	}
}

func TestBuildDisplaySnapshotUnavailable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	report := testReport("logged-out-agent", now)
	report.Providers[model.ProviderCodex] = model.ProviderReport{
		ObservedAt: now,
		AuthState:  "unauthenticated",
		Source:     "codex-app-server",
		ErrorCode:  "not_authenticated",
		Windows:    model.ProviderWindows{},
	}
	if err := s.ReplaceAgentReport(ctx, report, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.BuildDisplaySnapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Providers[model.ProviderCodex].Freshness != model.FreshnessUnavailable ||
		snapshot.Providers[model.ProviderClaude].Freshness != model.FreshnessUnavailable {
		t.Fatalf("providers = %+v", snapshot.Providers)
	}
	wantWarnings := []string{"codex_login_required", "claude_unavailable"}
	if !equalStrings(snapshot.Warnings, wantWarnings) {
		t.Fatalf("warnings = %v, want %v", snapshot.Warnings, wantWarnings)
	}
}

func TestBuildDisplaySnapshotDoesNotFallBackPastNewestUnavailableReport(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	usable := testReport("authenticated", now.Add(-time.Minute))
	usable.Providers[model.ProviderCodex] = providerReport(now.Add(-time.Minute), 30)
	if err := s.ReplaceAgentReport(ctx, usable, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	failed := testReport("logged-out", now)
	failed.Providers[model.ProviderCodex] = model.ProviderReport{
		ObservedAt: now,
		AuthState:  "unauthenticated",
		ErrorCode:  "not_authenticated",
		Windows:    model.ProviderWindows{},
	}
	if err := s.ReplaceAgentReport(ctx, failed, now); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.BuildDisplaySnapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	codex := snapshot.Providers[model.ProviderCodex]
	if codex.Freshness != model.FreshnessUnavailable || codex.Windows.FiveHour != nil || codex.Windows.SevenDay != nil {
		t.Fatalf("codex = %+v", codex)
	}
	if !codex.LoginRequired {
		t.Fatalf("codex loginRequired = false: %+v", codex)
	}
	if codex.ObservedAt == nil || !codex.ObservedAt.Equal(now) {
		t.Fatalf("codex observedAt = %v, want %v", codex.ObservedAt, now)
	}
}

func TestRawTokenIsNotPresentInDatabaseFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "monitor.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateToken(ctx, CreateTokenRequest{Scope: ScopeDisplayRead, Label: "display"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(b), created.RawToken) {
		t.Fatal("raw token was found in SQLite database")
	}
}

func testReport(agentID string, sentAt time.Time) model.AgentReport {
	return model.AgentReport{
		SchemaVersion: model.SchemaVersion,
		AgentID:       agentID,
		SentAt:        sentAt,
		Providers:     make(map[model.ProviderName]model.ProviderReport),
		ActiveTasks:   make([]model.ActiveTask, 0),
	}
}

func providerReport(observedAt time.Time, used float64) model.ProviderReport {
	return model.ProviderReport{
		ObservedAt: observedAt,
		Plan:       "pro",
		Source:     "test",
		Windows: model.ProviderWindows{
			FiveHour: &model.LimitWindow{
				UsedPercent:      used,
				RemainingPercent: 100 - used,
				ResetsAt:         observedAt.Add(5 * time.Hour),
			},
		},
	}
}

func task(provider model.ProviderName, kind model.TaskKind, sessionID string, lastSeen time.Time) model.ActiveTask {
	return model.ActiveTask{
		Provider:   provider,
		Kind:       kind,
		SessionID:  sessionID,
		StartedAt:  lastSeen.Add(-time.Minute),
		LastSeenAt: lastSeen,
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
