package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"codex-claude-monitor/internal/model"
)

func TestNormalizeCodexMapsDurationsAndClamps(t *testing.T) {
	five, seven, reset := int64(300), int64(10080), int64(1786320000)
	plan := "pro"
	account := accountResponse{Account: &struct {
		Type     string `json:"type"`
		PlanType string `json:"planType"`
	}{Type: "chatgpt", PlanType: "plus"}}
	limits := rateLimitsResponse{RateLimitsByLimitID: map[string]rateLimitSnapshot{
		"codex": {PlanType: &plan, Primary: &rateLimitWindow{UsedPercent: -2, WindowDurationMin: &five, ResetsAt: &reset}, Secondary: &rateLimitWindow{UsedPercent: 120, WindowDurationMin: &seven, ResetsAt: &reset}},
	}}
	report := normalizeCodex(account, limits, time.Unix(10, 0))
	if report.Plan != "plus" || report.AuthState != "authenticated" {
		t.Fatalf("unexpected account normalization: %+v", report)
	}
	if report.Windows.FiveHour == nil || report.Windows.FiveHour.UsedPercent != 0 || report.Windows.FiveHour.RemainingPercent != 100 {
		t.Fatalf("unexpected 5h window: %+v", report.Windows.FiveHour)
	}
	if report.Windows.SevenDay == nil || report.Windows.SevenDay.UsedPercent != 100 || report.Windows.SevenDay.RemainingPercent != 0 {
		t.Fatalf("unexpected 7d window: %+v", report.Windows.SevenDay)
	}
}

func TestNormalizeCodexLeavesUnknownWindowNil(t *testing.T) {
	duration, reset := int64(1440), int64(1786320000)
	report := normalizeCodex(accountResponse{}, rateLimitsResponse{RateLimits: rateLimitSnapshot{
		Primary: &rateLimitWindow{UsedPercent: 50, WindowDurationMin: &duration, ResetsAt: &reset},
	}}, time.Now())
	if report.Windows.FiveHour != nil || report.Windows.SevenDay != nil {
		t.Fatalf("unknown duration invented a window: %+v", report.Windows)
	}
}

func TestNormalizeCodexNamesProTiers(t *testing.T) {
	account := func(plan string) accountResponse {
		return accountResponse{Account: &struct {
			Type     string `json:"type"`
			PlanType string `json:"planType"`
		}{Type: "chatgpt", PlanType: plan}}
	}
	plan := func(value string) *string { return &value }

	for name, testCase := range map[string]struct {
		account string
		limits  *string
		want    string
	}{
		"pro 20x":                    {account: "pro", limits: plan("pro"), want: "pro20"},
		"pro 5x":                     {account: "prolite", limits: plan("prolite"), want: "pro5"},
		"rate limits refine tier":    {account: "pro", limits: plan("prolite"), want: "pro5"},
		"unrelated conflict ignored": {account: "plus", limits: plan("pro"), want: "plus"},
	} {
		t.Run(name, func(t *testing.T) {
			report := normalizeCodex(account(testCase.account), rateLimitsResponse{
				RateLimits: rateLimitSnapshot{PlanType: testCase.limits},
			}, time.Unix(10, 0))
			if report.Plan != testCase.want {
				t.Fatalf("plan = %q, want %q", report.Plan, testCase.want)
			}
		})
	}
}

func TestNormalizeCodexResolvesDuplicateDurationsWithoutLimitIDs(t *testing.T) {
	five, resetSoon, resetLate := int64(300), int64(1786320000), int64(1786323600)
	misleadingFiveID, misleadingSevenID := "seven-day", "five-hour"
	lowUsage := rateLimitSnapshot{
		LimitID: &misleadingFiveID,
		Primary: &rateLimitWindow{UsedPercent: 10, WindowDurationMin: &five, ResetsAt: &resetSoon},
	}
	highUsage := rateLimitSnapshot{
		LimitID: &misleadingSevenID,
		Primary: &rateLimitWindow{UsedPercent: 70, WindowDurationMin: &five, ResetsAt: &resetLate},
	}

	first := normalizeCodex(accountResponse{}, rateLimitsResponse{RateLimitsByLimitID: map[string]rateLimitSnapshot{
		"codex": lowUsage,
		"other": highUsage,
	}}, time.Unix(10, 0))
	second := normalizeCodex(accountResponse{}, rateLimitsResponse{RateLimitsByLimitID: map[string]rateLimitSnapshot{
		"codex": highUsage,
		"other": lowUsage,
	}}, time.Unix(10, 0))

	for name, report := range map[string]model.ProviderReport{"first": first, "second": second} {
		if report.Windows.FiveHour == nil || report.Windows.FiveHour.UsedPercent != 70 {
			t.Fatalf("%s 5h window = %+v, want conservative duplicate with 70%% used", name, report.Windows.FiveHour)
		}
		if report.Windows.SevenDay != nil {
			t.Fatalf("%s classified an opaque ID as 7d: %+v", name, report.Windows.SevenDay)
		}
	}
}

func TestNormalizeCodexPrefersCanonicalRateLimitsOnConflict(t *testing.T) {
	five, canonicalReset, mappedReset := int64(300), int64(1786320000), int64(1786323600)
	report := normalizeCodex(accountResponse{}, rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 25, WindowDurationMin: &five, ResetsAt: &canonicalReset},
		},
		RateLimitsByLimitID: map[string]rateLimitSnapshot{
			"opaque": {
				Primary: &rateLimitWindow{UsedPercent: 90, WindowDurationMin: &five, ResetsAt: &mappedReset},
			},
		},
	}, time.Unix(10, 0))
	if report.Windows.FiveHour == nil || report.Windows.FiveHour.UsedPercent != 25 {
		t.Fatalf("5h window = %+v, want canonical top-level rateLimits window", report.Windows.FiveHour)
	}
}

func TestCodexCollectorHandshakeAndCollect(t *testing.T) {
	c := NewCodex(CodexConfig{
		Command: os.Args[0], Args: []string{"-test.run=TestCodexHelperProcess", "--"},
		Env: []string{"GO_WANT_CODEX_HELPER=normal"}, Timeout: time.Second,
	})
	defer c.Close()
	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Plan != "pro20" || report.Windows.FiveHour == nil || report.Windows.SevenDay == nil {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestCodexCollectorReleaseIdleResourcesAllowsFreshProbe(t *testing.T) {
	c := NewCodex(CodexConfig{
		Command: os.Args[0], Args: []string{"-test.run=TestCodexHelperProcess", "--"},
		Env: []string{"GO_WANT_CODEX_HELPER=normal"}, Timeout: time.Second,
	})
	defer c.Close()
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	first := c.proc
	c.mu.Unlock()
	if first == nil {
		t.Fatal("first probe did not retain app-server")
	}

	c.ReleaseIdleResources()
	c.mu.Lock()
	afterRelease := c.proc
	c.mu.Unlock()
	if afterRelease != nil {
		t.Fatal("idle app-server was not released")
	}
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("fresh probe after release failed: %v", err)
	}
	c.mu.Lock()
	second := c.proc
	c.mu.Unlock()
	if second == nil || second == first {
		t.Fatal("fresh probe did not start a new app-server")
	}
}

func TestCodexCollectorReturnsLoginRequiredWithoutRateLimitProbe(t *testing.T) {
	c := NewCodex(CodexConfig{
		Command: os.Args[0], Args: []string{"-test.run=TestCodexHelperProcess", "--"},
		Env: []string{"GO_WANT_CODEX_HELPER=unauthenticated"}, Timeout: time.Second,
	})
	defer c.Close()
	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.AuthState != "unauthenticated" || report.ErrorCode != "not_authenticated" ||
		report.Windows.FiveHour != nil || report.Windows.SevenDay != nil {
		t.Fatalf("unexpected unauthenticated report: %+v", report)
	}
}

func TestCodexCollectorLive(t *testing.T) {
	if os.Getenv("QUOTA_MONITOR_LIVE_CODEX_TEST") != "1" {
		t.Skip("set QUOTA_MONITOR_LIVE_CODEX_TEST=1 to query the logged-in local Codex CLI")
	}
	c := NewCodex(CodexConfig{Timeout: 20 * time.Second})
	defer c.Close()
	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.AuthState != "authenticated" {
		t.Fatalf("Codex is not authenticated: %+v", report)
	}
	if report.Windows.FiveHour == nil && report.Windows.SevenDay == nil {
		t.Fatalf("Codex returned no recognized windows: %+v", report)
	}
	var rawLimits rateLimitsResponse
	if err := c.call(context.Background(), "account/rateLimits/read", nil, &rawLimits); err != nil {
		t.Fatal(err)
	}
	t.Logf("Codex plan normalized=%q rateLimits=%q 5h=%+v 7d=%+v",
		report.Plan, stableCodexPlan(rawLimits), report.Windows.FiveHour, report.Windows.SevenDay)
}

func TestCodexCollectorTimesOut(t *testing.T) {
	c := NewCodex(CodexConfig{
		Command: os.Args[0], Args: []string{"-test.run=TestCodexHelperProcess", "--"},
		Env: []string{"GO_WANT_CODEX_HELPER=hang"}, Timeout: 50 * time.Millisecond,
	})
	defer c.Close()
	_, err := c.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected deadline error, got %v", err)
	}
}

func TestCodexCollectorReconnectsAfterDisconnect(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "starts")
	c := NewCodex(CodexConfig{
		Command: os.Args[0], Args: []string{"-test.run=TestCodexHelperProcess", "--"},
		Env: []string{"GO_WANT_CODEX_HELPER=disconnect-once", "CODEX_HELPER_COUNTER=" + counter}, Timeout: time.Second,
	})
	defer c.Close()
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatal("first collection should disconnect")
	}
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("collector did not reconnect: %v", err)
	}
}

func TestCodexHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_WANT_CODEX_HELPER")
	if mode == "" {
		return
	}
	if mode == "hang" {
		time.Sleep(time.Hour)
		return
	}
	disconnect := false
	if mode == "disconnect-once" {
		counter := os.Getenv("CODEX_HELPER_COUNTER")
		if _, err := os.Stat(counter); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(counter, []byte("1"), 0o600)
			disconnect = true
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		if request.ID == 0 {
			continue
		}
		if disconnect && request.Method == "account/read" {
			return
		}
		// A bidirectional server request may reuse a numeric id. It must not be
		// mistaken for the response to our request.
		if mode == "normal" && request.Method == "initialize" {
			_ = encoder.Encode(map[string]any{"id": request.ID, "method": "fixture/serverRequest", "params": map[string]any{}})
		}
		var result any = map[string]any{}
		switch request.Method {
		case "account/read":
			if mode == "unauthenticated" {
				result = map[string]any{"requiresOpenaiAuth": true, "account": nil}
			} else {
				result = map[string]any{"requiresOpenaiAuth": true, "account": map[string]any{"type": "chatgpt", "planType": "pro", "email": nil}}
			}
		case "account/rateLimits/read":
			if mode == "unauthenticated" {
				return
			}
			result = map[string]any{"rateLimits": map[string]any{
				"planType":  "pro",
				"primary":   map[string]any{"usedPercent": 10, "windowDurationMins": 300, "resetsAt": 1786320000},
				"secondary": map[string]any{"usedPercent": 20, "windowDurationMins": 10080, "resetsAt": 1786924800},
			}}
		}
		_ = encoder.Encode(map[string]any{"id": request.ID, "result": result})
	}
}

func TestRPCErrorString(t *testing.T) {
	got := (&rpcError{Code: -1, Message: "bad"}).Error()
	if got != "app-server error -1: bad" {
		t.Fatal(got)
	}
}

func TestCodexDefaultArguments(t *testing.T) {
	c := NewCodex(CodexConfig{})
	if c.cfg.Command != "codex" || !reflect.DeepEqual(c.cfg.Args, []string{"app-server", "--stdio"}) {
		t.Fatalf("unexpected defaults: %#v", c.cfg)
	}
}

func Example_normalizeWindow() {
	window := normalizeWindow(25, time.Unix(1, 0))
	fmt.Println(window.RemainingPercent)
	// Output: 75
}
