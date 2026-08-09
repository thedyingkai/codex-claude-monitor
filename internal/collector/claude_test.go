package collector

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"codex-claude-monitor/internal/model"
)

func TestParseClaudeStatusLineFixture(t *testing.T) {
	payload, err := os.ReadFile("testdata/claude-statusline.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := ParseClaudeStatusLine(payload, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if report.Windows.FiveHour == nil || report.Windows.FiveHour.UsedPercent != 12.5 || report.Windows.FiveHour.RemainingPercent != 87.5 {
		t.Fatalf("bad 5h: %+v", report.Windows.FiveHour)
	}
	if report.Windows.SevenDay == nil || report.Windows.SevenDay.UsedPercent != 91 {
		t.Fatalf("bad 7d: %+v", report.Windows.SevenDay)
	}
}

func TestParseClaudeUsageNDJSONFixture(t *testing.T) {
	payload, _ := os.ReadFile("testdata/claude-usage.ndjson")
	report, err := ParseClaudeUsage(payload, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if report.Source != "claude-usage" || report.Windows.FiveHour.UsedPercent != 25 || report.Windows.SevenDay.UsedPercent != 40 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestParseClaudeUsageRenderedCommandText(t *testing.T) {
	payload := []byte("{\"type\":\"result\",\"result\":\"Current session\\n25% used\\nResets 2026-08-03T05:00:00Z\\nCurrent week (all models)\\n40% used\\nResets 2026-08-09T00:00:00Z\\nCurrent week (Sonnet only)\\n99% used\\nResets 2026-08-10T00:00:00Z\"}\n")
	report, err := ParseClaudeUsage(payload, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if report.Windows.FiveHour == nil || report.Windows.FiveHour.UsedPercent != 25 || report.Windows.SevenDay == nil || report.Windows.SevenDay.UsedPercent != 40 {
		t.Fatalf("unexpected rendered /usage parse: %+v", report.Windows)
	}
}

func TestParseClaudeStatusLineAllowsMissingWindow(t *testing.T) {
	report, err := ParseClaudeStatusLine([]byte(`{"rate_limits":{"five_hour":{"used_percentage":10,"resets_at":1786320000000}}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.Windows.FiveHour == nil || report.Windows.SevenDay != nil {
		t.Fatalf("unexpected windows: %+v", report.Windows)
	}
}

type runnerCall struct {
	args []string
	env  []string
}

type fakeRunner struct {
	outputs [][]byte
	errors  []error
	calls   []runnerCall
}

func (r *fakeRunner) Run(_ context.Context, _ string, args []string, env []string) ([]byte, error) {
	r.calls = append(r.calls, runnerCall{append([]string(nil), args...), append([]string(nil), env...)})
	index := len(r.calls) - 1
	return r.outputs[index], r.errors[index]
}

func TestClaudeCollectorParsesUnauthenticatedStdoutDespiteExitOne(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte(`{"loggedIn":false,"authMethod":"none"}`)}, errors: []error{errors.New("exit status 1")}}
	report, err := NewClaude(ClaudeConfig{Runner: runner}).Collect(context.Background())
	if err != nil {
		t.Fatalf("valid auth stdout must win over exit status: %v", err)
	}
	if report.AuthState != "unauthenticated" || report.ErrorCode != "not_authenticated" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("usage should not run while logged out: %d calls", len(runner.calls))
	}
}

func TestClaudeCollectorUsesOnlyUsageSlashCommand(t *testing.T) {
	runner := &fakeRunner{
		outputs: [][]byte{[]byte(`{"loggedIn":true,"subscriptionType":"max"}`), []byte(`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":"2026-08-03T00:00:00Z"}}}`)},
		errors:  []error{nil, nil},
	}
	report, err := NewClaude(ClaudeConfig{Command: "claude-test", Runner: runner}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "/usage", "--output-format", "stream-json", "--verbose", "--no-session-persistence"}
	if !reflect.DeepEqual(runner.calls[1].args, want) {
		t.Fatalf("usage arguments changed: %#v", runner.calls[1].args)
	}
	if !contains(runner.calls[1].env, ProbeEnvironment) || !contains(runner.calls[1].env, "NO_COLOR=1") {
		t.Fatalf("probe environment missing: %#v", runner.calls[1].env)
	}
	if report.Plan != "max" || report.Windows.FiveHour == nil {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestMergeProviderReportsFillsMissingWindows(t *testing.T) {
	five, seven := normalizeWindow(1, time.Unix(100, 0)), normalizeWindow(2, time.Unix(200, 0))
	a := modelReport(time.Unix(10, 0), &five, nil)
	b := modelReport(time.Unix(20, 0), nil, &seven)
	merged := MergeProviderReports(a, b)
	if merged.Windows.FiveHour == nil || merged.Windows.SevenDay == nil || !merged.ObservedAt.Equal(b.ObservedAt) {
		t.Fatalf("unexpected merge: %+v", merged)
	}
}

func TestMergeProviderReportsDoesNotCarryWindowPastReset(t *testing.T) {
	expired := normalizeWindow(1, time.Unix(15, 0))
	old := modelReport(time.Unix(10, 0), &expired, nil)
	newer := modelReport(time.Unix(20, 0), nil, nil)
	merged := MergeProviderReports(old, newer)
	if merged.Windows.FiveHour != nil {
		t.Fatalf("expired window was carried forward: %+v", merged.Windows.FiveHour)
	}
}

func TestMergeProviderReportsDoesNotCarryWindowAcrossLogout(t *testing.T) {
	five := normalizeWindow(1, time.Now().Add(time.Hour))
	loggedIn := model.ProviderReport{
		ObservedAt: time.Now(), AuthState: "authenticated", Windows: model.ProviderWindows{FiveHour: &five},
	}
	loggedOut := model.ProviderReport{
		ObservedAt: time.Now().Add(time.Second), AuthState: "unauthenticated", ErrorCode: "not_authenticated",
		Windows: model.ProviderWindows{FiveHour: &five},
	}
	merged := MergeProviderReports(loggedIn, loggedOut)
	if merged.AuthState != "unauthenticated" || merged.Windows.FiveHour != nil || merged.Windows.SevenDay != nil {
		t.Fatalf("logout retained quota: %+v", merged)
	}
}

func modelReport(at time.Time, five, seven *model.LimitWindow) model.ProviderReport {
	return model.ProviderReport{ObservedAt: at, Windows: model.ProviderWindows{FiveHour: five, SevenDay: seven}}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
