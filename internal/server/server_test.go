package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-claude-monitor/internal/model"
	"codex-claude-monitor/internal/store"
)

type serverFixture struct {
	store        *store.Store
	handler      http.Handler
	now          time.Time
	agentToken   string
	displayToken string
}

func newServerFixture(t *testing.T, mutate func(*Config)) *serverFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	agentToken, err := db.CreateToken(ctx, store.CreateTokenRequest{
		Scope: store.ScopeAgentWrite, AgentID: "agent-a", Label: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	displayToken, err := db.CreateToken(ctx, store.CreateTokenRequest{
		Scope: store.ScopeDisplayRead, Label: "display",
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &serverFixture{
		store:        db,
		now:          time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
		agentToken:   agentToken.RawToken,
		displayToken: displayToken.RawToken,
	}
	cfg := Config{Store: db, Now: func() time.Time { return f.now }}
	if mutate != nil {
		mutate(&cfg)
	}
	server, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	f.handler = server.Handler()
	return f
}

func TestHealthz(t *testing.T) {
	f := newServerFixture(t, nil)
	response := serve(f.handler, http.MethodGet, "/healthz", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected CORS header = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHealthzCachesDatabaseProbeForOneSecond(t *testing.T) {
	f := newServerFixture(t, nil)
	if got := serve(f.handler, http.MethodGet, "/healthz", "", nil); got.Code != http.StatusOK {
		t.Fatalf("initial status = %d", got.Code)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if got := serve(f.handler, http.MethodGet, "/healthz", "", nil); got.Code != http.StatusOK {
		t.Fatalf("cached status = %d", got.Code)
	}
	f.now = f.now.Add(2 * time.Second)
	if got := serve(f.handler, http.MethodGet, "/healthz", "", nil); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("expired cache status = %d, body=%s", got.Code, got.Body.String())
	}
}

func TestAgentReportAndDisplaySnapshotEndToEnd(t *testing.T) {
	f := newServerFixture(t, nil)
	report := validReport(f.now)
	response := postReport(f, f.agentToken, report)
	if response.Code != http.StatusNoContent {
		t.Fatalf("report status = %d, body = %s", response.Code, response.Body.String())
	}

	response = serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body = %s", response.Code, response.Body.String())
	}
	var snapshot model.DisplaySnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.Agents.Online != 1 || snapshot.Agents.Total != 1 {
		t.Fatalf("agents = %+v", snapshot.Agents)
	}
	if snapshot.Tasks.Codex.Main != 1 || snapshot.Tasks.Total.Main != 1 {
		t.Fatalf("tasks = %+v", snapshot.Tasks)
	}
	codex := snapshot.Providers[model.ProviderCodex]
	if codex.Freshness != model.FreshnessFresh || codex.Windows.FiveHour == nil || codex.Windows.FiveHour.RemainingPercent != 80 {
		t.Fatalf("codex snapshot = %+v", codex)
	}
	if snapshot.Providers[model.ProviderClaude].Freshness != model.FreshnessUnavailable {
		t.Fatalf("claude snapshot = %+v", snapshot.Providers[model.ProviderClaude])
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected CORS header = %q", got)
	}

	// A report is a full snapshot. An empty newer report removes the task and
	// provider rows for this agent.
	f.now = f.now.Add(time.Second)
	empty := model.AgentReport{
		SchemaVersion: model.SchemaVersion,
		AgentID:       "agent-a",
		SentAt:        f.now,
		Providers:     map[model.ProviderName]model.ProviderReport{},
		ActiveTasks:   []model.ActiveTask{},
	}
	response = postReport(f, f.agentToken, empty)
	if response.Code != http.StatusNoContent {
		t.Fatalf("empty report status = %d, body = %s", response.Code, response.Body.String())
	}
	response = serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil)
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Tasks.Total.Main != 0 || snapshot.Providers[model.ProviderCodex].Freshness != model.FreshnessUnavailable {
		t.Fatalf("replacement snapshot = %+v", snapshot)
	}
}

func TestDisplaySnapshotExposesLoginRequired(t *testing.T) {
	f := newServerFixture(t, nil)
	report := validReport(f.now)
	report.Providers[model.ProviderCodex] = model.ProviderReport{
		ObservedAt: f.now,
		AuthState:  "unauthenticated",
		Source:     "codex-app-server",
		Windows:    model.ProviderWindows{},
		ErrorCode:  "not_authenticated",
	}
	response := postReport(f, f.agentToken, report)
	if response.Code != http.StatusNoContent {
		t.Fatalf("report status = %d, body = %s", response.Code, response.Body.String())
	}

	response = serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body = %s", response.Code, response.Body.String())
	}
	var snapshot model.DisplaySnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snapshot.Providers[model.ProviderCodex].LoginRequired {
		t.Fatalf("codex loginRequired = false: %+v", snapshot.Providers[model.ProviderCodex])
	}
	if len(snapshot.Warnings) != 2 || snapshot.Warnings[0] != "codex_login_required" || snapshot.Warnings[1] != "claude_unavailable" {
		t.Fatalf("warnings = %v", snapshot.Warnings)
	}
	if body := response.Body.String(); strings.Contains(body, "authState") || strings.Contains(body, "errorCode") {
		t.Fatalf("snapshot exposed internal authentication details: %s", body)
	}
}

func TestEmptyDisplaySnapshotOmitsUnavailableObservedAt(t *testing.T) {
	f := newServerFixture(t, nil)
	response := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "0001-01-01") {
		t.Fatalf("unavailable snapshot leaked zero time: %s", response.Body.String())
	}
	var snapshot model.DisplaySnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	for _, provider := range []model.ProviderName{model.ProviderCodex, model.ProviderClaude} {
		got := snapshot.Providers[provider]
		if got.Freshness != model.FreshnessUnavailable || got.ObservedAt != nil ||
			got.Windows.FiveHour != nil || got.Windows.SevenDay != nil {
			t.Fatalf("provider %s = %+v", provider, got)
		}
	}
}

func TestAuthenticationScopeAndAgentBinding(t *testing.T) {
	f := newServerFixture(t, nil)
	report := validReport(f.now)

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "bad", token: "not-a-real-token", want: http.StatusUnauthorized},
		{name: "wrong scope", token: f.displayToken, want: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := postReport(f, test.token, report)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}

	report.AgentID = "agent-b"
	response := postReport(f, f.agentToken, report)
	if response.Code != http.StatusForbidden {
		t.Fatalf("mismatch status = %d, body = %s", response.Code, response.Body.String())
	}
	response = serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.agentToken, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("display wrong-scope status = %d", response.Code)
	}
}

func TestOldAndDuplicateReportsRejected(t *testing.T) {
	f := newServerFixture(t, nil)
	report := validReport(f.now)
	if got := postReport(f, f.agentToken, report); got.Code != http.StatusNoContent {
		t.Fatalf("initial status = %d, body = %s", got.Code, got.Body.String())
	}
	if got := postReport(f, f.agentToken, report); got.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body = %s", got.Code, got.Body.String())
	}

	tooOld := validReport(f.now.Add(-MaxReportAge - time.Second))
	if got := postReport(f, f.agentToken, tooOld); got.Code != http.StatusBadRequest {
		t.Fatalf("old status = %d, body = %s", got.Code, got.Body.String())
	}
	future := validReport(f.now.Add(MaxFutureSkew + time.Second))
	if got := postReport(f, f.agentToken, future); got.Code != http.StatusBadRequest {
		t.Fatalf("future status = %d, body = %s", got.Code, got.Body.String())
	}
}

func TestReportBodyLimitUnknownFieldsAndSingleObject(t *testing.T) {
	f := newServerFixture(t, nil)
	large := `{"schemaVersion":1,"agentId":"agent-a","sentAt":"` + f.now.Format(time.RFC3339Nano) + `","providers":{},"activeTasks":[],"padding":"` + strings.Repeat("a", MaxReportBodySize) + `"}`
	response := serve(f.handler, http.MethodPost, "/api/v1/agent/report", f.agentToken, strings.NewReader(large))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d, body = %s", response.Code, response.Body.String())
	}

	unknown := `{"schemaVersion":1,"agentId":"agent-a","sentAt":"` + f.now.Format(time.RFC3339Nano) + `","providers":{},"activeTasks":[],"unexpected":true}`
	response = serve(f.handler, http.MethodPost, "/api/v1/agent/report", f.agentToken, strings.NewReader(unknown))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body = %s", response.Code, response.Body.String())
	}

	body, _ := json.Marshal(validReport(f.now))
	response = serve(f.handler, http.MethodPost, "/api/v1/agent/report", f.agentToken, strings.NewReader(string(body)+" {}"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("multiple object status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestPerTokenRateLimit(t *testing.T) {
	f := newServerFixture(t, func(cfg *Config) {
		cfg.DisplayRequestsPerMinute = 1
	})
	first := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	second := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second status = %d, Retry-After = %q", second.Code, second.Header().Get("Retry-After"))
	}
	f.now = f.now.Add(time.Minute)
	third := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil)
	if third.Code != http.StatusOK {
		t.Fatalf("third status after reset = %d", third.Code)
	}
}

func TestAuthenticationAttemptsAreLimitedBeforeTokenLookup(t *testing.T) {
	f := newServerFixture(t, func(cfg *Config) {
		cfg.AuthAttemptsPerMinute = 1
	})
	first := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", "invalid-one", nil)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first invalid attempt status = %d", first.Code)
	}
	second := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", "invalid-one", nil)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second invalid attempt status = %d, Retry-After = %q", second.Code, second.Header().Get("Retry-After"))
	}
}

func TestInvalidTokenCannotConsumeAnotherTokensPreAuthLimit(t *testing.T) {
	f := newServerFixture(t, func(cfg *Config) {
		cfg.AuthAttemptsPerMinute = 1
	})
	if got := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", "invalid", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("invalid status = %d", got.Code)
	}
	if got := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", f.displayToken, nil); got.Code != http.StatusOK {
		t.Fatalf("valid token was affected by a different fingerprint: %d, %s", got.Code, got.Body.String())
	}
}

func TestValidateAgentReport(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*model.AgentReport)
	}{
		{name: "schema", mutate: func(r *model.AgentReport) { r.SchemaVersion++ }},
		{name: "agent id", mutate: func(r *model.AgentReport) { r.AgentID = "bad id" }},
		{name: "missing providers", mutate: func(r *model.AgentReport) { r.Providers = nil }},
		{name: "missing tasks", mutate: func(r *model.AgentReport) { r.ActiveTasks = nil }},
		{name: "too many tasks", mutate: func(r *model.AgentReport) {
			task := r.ActiveTasks[0]
			r.ActiveTasks = make([]model.ActiveTask, 257)
			for i := range r.ActiveTasks {
				r.ActiveTasks[i] = task
				r.ActiveTasks[i].SessionID = fmt.Sprintf("session-%d", i)
			}
		}},
		{name: "provider", mutate: func(r *model.AgentReport) {
			r.Providers["other"] = model.ProviderReport{}
		}},
		{name: "percentage", mutate: func(r *model.AgentReport) {
			r.Providers[model.ProviderCodex] = providerWithPercent(now, 101, -1)
		}},
		{name: "sum", mutate: func(r *model.AgentReport) {
			r.Providers[model.ProviderCodex] = providerWithPercent(now, 10, 80)
		}},
		{name: "missing resets", mutate: func(r *model.AgentReport) {
			provider := providerWithPercent(now, 10, 90)
			provider.Windows.FiveHour.ResetsAt = time.Time{}
			r.Providers[model.ProviderCodex] = provider
		}},
		{name: "duplicate task", mutate: func(r *model.AgentReport) {
			r.ActiveTasks = append(r.ActiveTasks, r.ActiveTasks[0])
		}},
		{name: "task ordering", mutate: func(r *model.AgentReport) {
			r.ActiveTasks[0].LastSeenAt = r.ActiveTasks[0].StartedAt.Add(-time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := validReport(now)
			test.mutate(&report)
			if err := ValidateAgentReport(report, now); err == nil {
				t.Fatal("ValidateAgentReport() unexpectedly succeeded")
			}
		})
	}
	if err := ValidateAgentReport(validReport(now), now); err != nil {
		t.Fatalf("valid report error = %v", err)
	}
}

func TestLoggerDoesNotLeakBearerToken(t *testing.T) {
	var logs bytes.Buffer
	f := newServerFixture(t, func(cfg *Config) {
		cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	secret := "qmon_super-secret-value-that-must-not-be-logged"
	response := serve(f.handler, http.MethodGet, "/api/v1/display/snapshot", secret, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("bearer token leaked; logs=%q body=%q", logs.String(), response.Body.String())
	}
}

func serve(handler http.Handler, method, path, token string, body ioReader) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		data, _ := ioReadAll(body)
		reader = strings.NewReader(string(data))
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// These tiny local interfaces keep request construction generic without
// pulling implementation details into individual test cases.
type ioReader interface {
	Read([]byte) (int, error)
}

func ioReadAll(reader ioReader) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(reader)
	return b.Bytes(), err
}

func postReport(f *serverFixture, token string, report model.AgentReport) *httptest.ResponseRecorder {
	body, err := json.Marshal(report)
	if err != nil {
		panic(err)
	}
	return serve(f.handler, http.MethodPost, "/api/v1/agent/report", token, bytes.NewReader(body))
}

func validReport(now time.Time) model.AgentReport {
	return model.AgentReport{
		SchemaVersion: model.SchemaVersion,
		AgentID:       "agent-a",
		SentAt:        now,
		Providers: map[model.ProviderName]model.ProviderReport{
			model.ProviderCodex: providerWithPercent(now, 20, 80),
		},
		ActiveTasks: []model.ActiveTask{
			{
				Provider:   model.ProviderCodex,
				Kind:       model.TaskMain,
				SessionID:  "session-1",
				StartedAt:  now.Add(-time.Minute),
				LastSeenAt: now,
			},
		},
	}
}

func providerWithPercent(now time.Time, used, remaining float64) model.ProviderReport {
	return model.ProviderReport{
		ObservedAt: now,
		Plan:       "plus",
		Source:     "test",
		Windows: model.ProviderWindows{
			FiveHour: &model.LimitWindow{
				UsedPercent:      used,
				RemainingPercent: remaining,
				ResetsAt:         now.Add(5 * time.Hour),
			},
		},
	}
}
