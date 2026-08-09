package agent

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-claude-monitor/internal/model"
)

type staticCollector struct {
	report model.ProviderReport
	err    error
	closed bool
}

func (c *staticCollector) Collect(context.Context) (model.ProviderReport, error) {
	return c.report, c.err
}
func (c *staticCollector) Close() error { c.closed = true; return nil }

type blockingCollector struct {
	calls      atomic.Int32
	concurrent atomic.Int32
	max        atomic.Int32
}

func (c *blockingCollector) Collect(ctx context.Context) (model.ProviderReport, error) {
	c.calls.Add(1)
	concurrent := c.concurrent.Add(1)
	defer c.concurrent.Add(-1)
	for {
		maximum := c.max.Load()
		if concurrent <= maximum || c.max.CompareAndSwap(maximum, concurrent) {
			break
		}
	}
	<-ctx.Done()
	return model.ProviderReport{ErrorCode: "cancelled"}, ctx.Err()
}

func (*blockingCollector) Close() error { return nil }

type collectionRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *collectionRecorder) add(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *collectionRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type recordedCollector struct {
	name     string
	recorder *collectionRecorder
	started  chan<- string
	gate     <-chan struct{}
}

func (c *recordedCollector) Collect(ctx context.Context) (model.ProviderReport, error) {
	c.recorder.add(c.name + ":start")
	if c.started != nil {
		c.started <- c.name
	}
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return model.ProviderReport{ErrorCode: "cancelled"}, ctx.Err()
		}
	}
	c.recorder.add(c.name + ":finish")
	return model.ProviderReport{ObservedAt: time.Now(), Source: "fixture"}, nil
}

func (*recordedCollector) Close() error { return nil }

type releasingRecordedCollector struct {
	*recordedCollector
}

func (c *releasingRecordedCollector) ReleaseIdleResources() {
	c.recorder.add(c.name + ":release")
}

func TestAgentReportsFullStateWithBearerToken(t *testing.T) {
	var received model.AgentReport
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agent/report" || request.Header.Get("Authorization") != "Bearer token" {
			http.Error(writer, "bad request", http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	five := model.LimitWindow{UsedPercent: 20, RemainingPercent: 80, ResetsAt: time.Now().Add(time.Hour)}
	codex := &staticCollector{report: model.ProviderReport{ObservedAt: time.Now(), Source: "fixture", Windows: model.ProviderWindows{FiveHour: &five}}}
	agent, err := New(Config{AgentID: "host-1", ServerURL: server.URL, Token: "token", AllowInsecureHTTP: true, Codex: codex})
	if err != nil {
		t.Fatal(err)
	}
	agent.CollectOnce(context.Background())
	if err := agent.ReportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if received.SchemaVersion != 1 || received.AgentID != "host-1" || received.Providers[model.ProviderCodex].Windows.FiveHour == nil {
		t.Fatalf("bad report: %+v", received)
	}
}

func TestAgentReportsDirectlyToStandaloneSink(t *testing.T) {
	var received model.AgentReport
	var statusProvider model.ProviderName
	five := model.LimitWindow{UsedPercent: 25, RemainingPercent: 75, ResetsAt: time.Now().Add(time.Hour)}
	instance, err := New(Config{
		AgentID: "cloud-standalone",
		Codex: &staticCollector{report: model.ProviderReport{
			ObservedAt: time.Now(), AuthState: "authenticated", Plan: "pro20",
			Windows: model.ProviderWindows{FiveHour: &five},
		}},
		ReportSink: ReportSinkFunc(func(_ context.Context, report model.AgentReport) error {
			received = report
			return nil
		}),
		ProviderStatus: func(provider model.ProviderName, _ model.ProviderReport) {
			statusProvider = provider
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance.CollectOnce(context.Background())
	if err := instance.ReportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if received.AgentID != "cloud-standalone" || received.Providers[model.ProviderCodex].Plan != "pro20" {
		t.Fatalf("direct report = %+v", received)
	}
	if statusProvider != model.ProviderCodex {
		t.Fatalf("status provider = %q", statusProvider)
	}
}

func TestAgentAppliesValidatedPlanOverride(t *testing.T) {
	claude := &staticCollector{report: model.ProviderReport{
		ObservedAt: time.Now(), AuthState: "authenticated", Plan: "max",
	}}
	instance, err := New(Config{
		AgentID: "host-1", ServerURL: "http://127.0.0.1:8787", Token: "token",
		AllowInsecureHTTP: true, Claude: claude,
		PlanOverrides: map[model.ProviderName]string{model.ProviderClaude: " max20 "},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance.CollectOnce(context.Background())
	if got := instance.SnapshotReport().Providers[model.ProviderClaude].Plan; got != "max20" {
		t.Fatalf("plan = %q, want max20", got)
	}

	for name, overrides := range map[string]map[model.ProviderName]string{
		"provider": {model.ProviderName("other"): "pro"},
		"value":    {model.ProviderClaude: "max20<script>"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(Config{
				AgentID: "host-1", ServerURL: "http://127.0.0.1:8787", Token: "token",
				AllowInsecureHTTP: true, PlanOverrides: overrides,
			})
			if err == nil {
				t.Fatal("invalid plan override was accepted")
			}
		})
	}
}

func TestSlowCollectionDoesNotBlockReportsOrOverlap(t *testing.T) {
	reported := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case reported <- struct{}{}:
		default:
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	collector := &blockingCollector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			AgentID: "slow-collector", ServerURL: server.URL, Token: "token",
			AllowInsecureHTTP: true, Claude: collector,
			ReportInterval: 20 * time.Millisecond, CollectInterval: 10 * time.Millisecond,
		})
	}()

	select {
	case <-reported:
	case <-time.After(250 * time.Millisecond):
		cancel()
		t.Fatal("report scheduler was blocked by provider collection")
	}
	// Let several collection ticks elapse while the first call is blocked. A
	// second provider query must not overlap it.
	time.Sleep(60 * time.Millisecond)
	if got := collector.max.Load(); got != 1 {
		cancel()
		t.Fatalf("concurrent collections = %d, want 1", got)
	}
	if got := collector.calls.Load(); got != 1 {
		cancel()
		t.Fatalf("collection calls while first was blocked = %d, want 1", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func TestDefaultCollectionStillStartsProvidersConcurrently(t *testing.T) {
	recorder := &collectionRecorder{}
	started := make(chan string, 2)
	gate := make(chan struct{})
	instance, err := New(Config{
		AgentID: "parallel-default",
		ReportSink: ReportSinkFunc(func(context.Context, model.AgentReport) error {
			return nil
		}),
		Codex:  &recordedCollector{name: "codex", recorder: recorder, started: started, gate: gate},
		Claude: &recordedCollector{name: "claude", recorder: recorder, started: started, gate: gate},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		instance.CollectOnce(context.Background())
		close(done)
	}()
	seen := make(map[string]bool)
	for len(seen) < 2 {
		select {
		case provider := <-started:
			seen[provider] = true
		case <-time.After(time.Second):
			close(gate)
			t.Fatalf("providers did not overlap; started = %v", seen)
		}
	}
	close(gate)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parallel collection did not finish")
	}
}

func TestSequentialCollectionOrdersAndReleasesProviders(t *testing.T) {
	recorder := &collectionRecorder{}
	codex := &releasingRecordedCollector{&recordedCollector{name: "codex", recorder: recorder}}
	claude := &recordedCollector{name: "claude", recorder: recorder}
	instance, err := New(Config{
		AgentID:              "sequential-standalone",
		SequentialCollection: true,
		ReportSink: ReportSinkFunc(func(context.Context, model.AgentReport) error {
			return nil
		}),
		Codex: codex, Claude: claude,
	})
	if err != nil {
		t.Fatal(err)
	}

	instance.CollectOnce(context.Background())
	want := []string{"codex:start", "codex:finish", "codex:release", "claude:start", "claude:finish"}
	got := recorder.snapshot()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
	report := instance.SnapshotReport()
	if len(report.Providers) != 2 || report.Providers[model.ProviderCodex].Source != "fixture" || report.Providers[model.ProviderClaude].Source != "fixture" {
		t.Fatalf("sequential state did not use normal result storage: %+v", report.Providers)
	}
}

func TestSuccessfulReportDelayKeepsStartToStartInterval(t *testing.T) {
	if got := successfulReportDelay(15*time.Second, 10*time.Second); got != 5*time.Second {
		t.Fatalf("delay = %s, want 5s", got)
	}
	if got := successfulReportDelay(15*time.Second, 20*time.Second); got != 0 {
		t.Fatalf("overrun delay = %s, want immediate", got)
	}
}

func TestAgentRetainsLastGoodQuotaOnCollectorFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	five := model.LimitWindow{UsedPercent: 10, RemainingPercent: 90}
	adapter := &staticCollector{report: model.ProviderReport{ObservedAt: time.Now(), Windows: model.ProviderWindows{FiveHour: &five}}}
	agent, _ := New(Config{AgentID: "a", ServerURL: server.URL, Token: "t", AllowInsecureHTTP: true, Codex: adapter})
	agent.CollectOnce(context.Background())
	adapter.report = model.ProviderReport{ErrorCode: "rate_limits_read_failed"}
	adapter.err = errors.New("offline")
	agent.CollectOnce(context.Background())
	report := agent.SnapshotReport().Providers[model.ProviderCodex]
	if report.Windows.FiveHour == nil || report.ErrorCode != "rate_limits_read_failed" {
		t.Fatalf("last good state not retained: %+v", report)
	}
}

func TestAgentRequiresHTTPSByDefault(t *testing.T) {
	_, err := New(Config{AgentID: "a", ServerURL: "http://example.com", Token: "t"})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS validation, got %v", err)
	}
}

func TestAgentAllowsInsecureHTTPOnlyOnLoopback(t *testing.T) {
	for _, serverURL := range []string{"http://localhost:8787", "http://127.0.0.1:8787", "http://[::1]:8787"} {
		if _, err := New(Config{AgentID: "a", ServerURL: serverURL, Token: "t", AllowInsecureHTTP: true}); err != nil {
			t.Fatalf("loopback URL %q was rejected: %v", serverURL, err)
		}
	}
	for _, serverURL := range []string{"http://example.com", "http://192.168.1.20:8787", "http://localhost.example.com"} {
		if _, err := New(Config{AgentID: "a", ServerURL: serverURL, Token: "t", AllowInsecureHTTP: true}); err == nil {
			t.Fatalf("non-loopback URL %q was accepted", serverURL)
		}
	}
}

func TestAgentRejectsCredentialsInServerURL(t *testing.T) {
	_, err := New(Config{AgentID: "a", ServerURL: "https://user:secret@example.com", Token: "t"})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected embedded credentials to be rejected, got %v", err)
	}
}

func TestAgentNeverFollowsReportRedirect(t *testing.T) {
	var redirectedAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedAuthorization = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	agent, err := New(Config{AgentID: "a", ServerURL: redirector.URL, Token: "secret", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.ReportOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("expected redirect response to fail, got %v", err)
	}
	if redirectedAuthorization != "" {
		t.Fatalf("authorization reached redirect target: %q", redirectedAuthorization)
	}
}

func TestAgentClearsClaudeWindowsWhenLogoutIsObserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	five := model.LimitWindow{UsedPercent: 10, RemainingPercent: 90, ResetsAt: time.Now().Add(time.Hour)}
	adapter := &staticCollector{report: model.ProviderReport{
		ObservedAt: time.Now(), AuthState: "authenticated", Windows: model.ProviderWindows{FiveHour: &five},
	}}
	agent, err := New(Config{AgentID: "a", ServerURL: server.URL, Token: "t", AllowInsecureHTTP: true, Claude: adapter})
	if err != nil {
		t.Fatal(err)
	}
	agent.CollectOnce(context.Background())
	adapter.report = model.ProviderReport{
		ObservedAt: time.Now().Add(time.Second), AuthState: "unauthenticated", ErrorCode: "not_authenticated", Source: "claude-auth-status",
	}
	agent.CollectOnce(context.Background())
	report := agent.SnapshotReport().Providers[model.ProviderClaude]
	if report.AuthState != "unauthenticated" || report.Windows.FiveHour != nil || report.Windows.SevenDay != nil {
		t.Fatalf("logout retained stale Claude quota: %+v", report)
	}
}

func TestServiceGenerators(t *testing.T) {
	windows, err := GenerateWindowsTaskXML(ServiceFileConfig{Executable: `C:\Program Files\quota-monitor.exe`, ConfigPath: `C:\Users\me\agent.json`, WindowsUserID: `PC\me`})
	if err != nil || !strings.Contains(windows, "LeastPrivilege") || !strings.Contains(windows, "quota-monitor.exe") {
		t.Fatalf("bad Windows task: %v %s", err, windows)
	}
	if err := xml.Unmarshal([]byte(windows), &struct{}{}); err != nil {
		t.Fatalf("Windows task is not XML: %v", err)
	}
	unit, err := GenerateSystemdUnit(ServiceFileConfig{Executable: "/usr/local/bin/quota-monitor", ConfigPath: "/home/me/.quota-monitor/agent.json"})
	if err != nil || !strings.Contains(unit, "ProtectHome=false") || !strings.Contains(unit, "UMask=0077") || !strings.Contains(unit, "Environment=\"PATH=%h/.local/bin:") || !strings.Contains(unit, "WorkingDirectory=\"/home/me/.quota-monitor\"") || !strings.Contains(unit, " agent --config ") {
		t.Fatalf("bad unit: %v %s", err, unit)
	}
	standalone, err := GenerateSystemdUnit(ServiceFileConfig{
		Executable: "/home/me/bin/quota-monitor", WorkingDirectory: "/home/me/.quota-monitor", Mode: "standalone",
	})
	if err != nil || !strings.Contains(standalone, ` standalone --firmware-dir="%h/.local/share/quota-monitor/firmware"`) || strings.Contains(standalone, "--config") ||
		!strings.Contains(standalone, "standalone quota monitor") {
		t.Fatalf("bad standalone unit: %v %s", err, standalone)
	}
	customFirmware, err := GenerateSystemdUnit(ServiceFileConfig{
		Executable: "/home/me/bin/quota-monitor", Mode: "standalone",
		FirmwareDirectory: "/home/me/qmon firmware",
	})
	if err != nil || !strings.Contains(customFirmware, `--firmware-dir "/home/me/qmon firmware"`) {
		t.Fatalf("bad standalone firmware directory: %v %s", err, customFirmware)
	}
}
