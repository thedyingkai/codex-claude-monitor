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
	"strconv"
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

type codexHelperTraceEvent struct {
	Generation   int    `json:"generation"`
	Method       string `json:"method"`
	RefreshToken *bool  `json:"refreshToken,omitempty"`
}

func newTracedCodexCollector(t *testing.T, mode string) (*CodexCollector, string) {
	t.Helper()
	directory := t.TempDir()
	tracePath := filepath.Join(directory, "trace.jsonl")
	statePath := filepath.Join(directory, "generation")
	c := NewCodex(CodexConfig{
		Command: os.Args[0], Args: []string{"-test.run=TestCodexHelperProcess", "--"},
		Env: []string{
			"GO_WANT_CODEX_HELPER=" + mode,
			"CODEX_HELPER_STATE=" + statePath,
			"CODEX_HELPER_TRACE=" + tracePath,
		},
		Timeout: 3 * time.Second,
	})
	t.Cleanup(func() { _ = c.Close() })
	return c, tracePath
}

func readCodexHelperTrace(t *testing.T, path string) []codexHelperTraceEvent {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Codex helper trace: %v", err)
	}
	var events []codexHelperTraceEvent
	scanner := bufio.NewScanner(strings.NewReader(string(payload)))
	for scanner.Scan() {
		var event codexHelperTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode Codex helper trace line %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan Codex helper trace: %v", err)
	}
	return events
}

func codexTraceEventsByMethod(events []codexHelperTraceEvent, method string) []codexHelperTraceEvent {
	var selected []codexHelperTraceEvent
	for _, event := range events {
		if event.Method == method {
			selected = append(selected, event)
		}
	}
	return selected
}

func assertCodexRefreshSequence(t *testing.T, events []codexHelperTraceEvent, want ...bool) {
	t.Helper()
	reads := codexTraceEventsByMethod(events, "account/read")
	if len(reads) != len(want) {
		t.Fatalf("account/read count = %d, want %d; trace=%+v", len(reads), len(want), events)
	}
	for index, expected := range want {
		if reads[index].RefreshToken == nil {
			t.Fatalf("account/read %d omitted refreshToken; trace=%+v", index+1, events)
		}
		if *reads[index].RefreshToken != expected {
			t.Fatalf("account/read %d refreshToken = %t, want %t; trace=%+v",
				index+1, *reads[index].RefreshToken, expected, events)
		}
		if reads[index].Generation != index+1 {
			t.Fatalf("account/read %d generation = %d, want %d; trace=%+v",
				index+1, reads[index].Generation, index+1, events)
		}
	}
}

func TestCodexCollectorUsesManagedTokenOnFirstAttempt(t *testing.T) {
	c, tracePath := newTracedCodexCollector(t, "trace-normal")
	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.AuthState != "authenticated" || report.Windows.FiveHour == nil {
		t.Fatalf("unexpected report: %+v", report)
	}
	events := readCodexHelperTrace(t, tracePath)
	if starts := len(codexTraceEventsByMethod(events, "__start__")); starts != 1 {
		t.Fatalf("app-server starts = %d, want 1; trace=%+v", starts, events)
	}
	assertCodexRefreshSequence(t, events, false)
	if probes := len(codexTraceEventsByMethod(events, "account/rateLimits/read")); probes != 1 {
		t.Fatalf("rate-limit probes = %d, want 1; trace=%+v", probes, events)
	}
}

func TestCodexCollectorRefreshesAfterRateLimitAuthFailure(t *testing.T) {
	for _, testCase := range []struct {
		name string
		mode string
	}{
		{name: "explicit token_expired", mode: "rate-token-expired-once"},
		{name: "structured HTTP 401", mode: "rate-structured-401-once"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			c, tracePath := newTracedCodexCollector(t, testCase.mode)
			report, err := c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.AuthState != "authenticated" || report.Windows.FiveHour == nil || report.Windows.SevenDay == nil {
				t.Fatalf("unexpected recovered report: %+v", report)
			}
			events := readCodexHelperTrace(t, tracePath)
			if starts := len(codexTraceEventsByMethod(events, "__start__")); starts != 2 {
				t.Fatalf("app-server starts = %d, want 2; trace=%+v", starts, events)
			}
			assertCodexRefreshSequence(t, events, false, true)
			if probes := len(codexTraceEventsByMethod(events, "account/rateLimits/read")); probes != 2 {
				t.Fatalf("rate-limit probes = %d, want 2; trace=%+v", probes, events)
			}
		})
	}
}

func TestCodexCollectorReturnsLoginRequiredWhenRefreshStillFails(t *testing.T) {
	const sentinelSecret = "sentinel-refresh-secret-must-not-leak"
	c, tracePath := newTracedCodexCollector(t, "refresh-auth-fails")
	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned an error instead of a login-required report: %v", err)
	}
	if report.AuthState != "unauthenticated" || report.ErrorCode != "not_authenticated" ||
		report.Windows.FiveHour != nil || report.Windows.SevenDay != nil {
		t.Fatalf("unexpected login-required report: %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sentinelSecret) || strings.Contains(fmt.Sprintf("%+v", report), sentinelSecret) {
		t.Fatalf("provider report leaked the helper's sentinel secret: %s", encoded)
	}
	events := readCodexHelperTrace(t, tracePath)
	if starts := len(codexTraceEventsByMethod(events, "__start__")); starts != 2 {
		t.Fatalf("app-server starts = %d, want exactly 2; trace=%+v", starts, events)
	}
	assertCodexRefreshSequence(t, events, false, true)
	if probes := len(codexTraceEventsByMethod(events, "account/rateLimits/read")); probes != 1 {
		t.Fatalf("rate-limit probes = %d, want 1 because second account/read failed; trace=%+v", probes, events)
	}
}

func TestCodexCollectorDoesNotRetryOrdinaryForbiddenError(t *testing.T) {
	c, tracePath := newTracedCodexCollector(t, "rate-forbidden")
	report, err := c.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect unexpectedly recovered from an ordinary HTTP 403")
	}
	if report.ErrorCode != "rate_limits_read_failed" {
		t.Fatalf("unexpected unavailable report: %+v", report)
	}
	events := readCodexHelperTrace(t, tracePath)
	if starts := len(codexTraceEventsByMethod(events, "__start__")); starts != 1 {
		t.Fatalf("app-server starts = %d, want 1; trace=%+v", starts, events)
	}
	assertCodexRefreshSequence(t, events, false)
	if probes := len(codexTraceEventsByMethod(events, "account/rateLimits/read")); probes != 1 {
		t.Fatalf("rate-limit probes = %d, want 1; trace=%+v", probes, events)
	}
}

func TestCodexCollectorRefreshesAfterAccountReadTokenExpired(t *testing.T) {
	c, tracePath := newTracedCodexCollector(t, "account-token-expired-once")
	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.AuthState != "authenticated" || report.Windows.FiveHour == nil || report.Windows.SevenDay == nil {
		t.Fatalf("unexpected recovered report: %+v", report)
	}
	events := readCodexHelperTrace(t, tracePath)
	if starts := len(codexTraceEventsByMethod(events, "__start__")); starts != 2 {
		t.Fatalf("app-server starts = %d, want 2; trace=%+v", starts, events)
	}
	assertCodexRefreshSequence(t, events, false, true)
	if probes := len(codexTraceEventsByMethod(events, "account/rateLimits/read")); probes != 1 {
		t.Fatalf("rate-limit probes = %d, want 1; trace=%+v", probes, events)
	}
}

func TestIsCodexRefreshableAuthError(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "message token_expired",
			err:  &rpcError{Code: -32603, Message: "upstream token_expired"},
			want: true,
		},
		{
			name: "wrapped invalid_grant",
			err:  fmt.Errorf("read account: %w", &rpcError{Code: -32603, Message: "invalid_grant"}),
			want: true,
		},
		{
			name: "structured nested code",
			err:  &rpcError{Code: -32603, Message: "authentication failed", Data: json.RawMessage(`{"error":{"code":"refresh_token_expired"}}`)},
			want: true,
		},
		{
			name: "structured HTTP 401",
			err:  &rpcError{Code: -32603, Message: "authentication failed", Data: json.RawMessage(`{"httpStatus":401}`)},
			want: true,
		},
		{
			name: "plain 401 text is not enough",
			err:  &rpcError{Code: -32603, Message: "HTTP 401 Unauthorized"},
			want: false,
		},
		{
			name: "structured HTTP 403",
			err:  &rpcError{Code: -32603, Message: "forbidden", Data: json.RawMessage(`{"httpStatus":403,"code":"forbidden"}`)},
			want: false,
		},
		{
			name: "malformed data",
			err:  &rpcError{Code: -32603, Message: "authentication failed", Data: json.RawMessage(`{`)},
			want: false,
		},
		{
			name: "non RPC error",
			err:  context.DeadlineExceeded,
			want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isCodexRefreshableAuthError(testCase.err); got != testCase.want {
				t.Fatalf("isCodexRefreshableAuthError(%v) = %t, want %t", testCase.err, got, testCase.want)
			}
		})
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
	generation := 0
	if statePath := os.Getenv("CODEX_HELPER_STATE"); statePath != "" {
		var err error
		generation, err = advanceCodexHelperGeneration(statePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := appendCodexHelperTrace(codexHelperTraceEvent{
			Generation: generation,
			Method:     "__start__",
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
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
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		if request.ID == 0 {
			continue
		}
		event := codexHelperTraceEvent{Generation: generation, Method: request.Method}
		if request.Method == "account/read" {
			var params struct {
				RefreshToken *bool `json:"refreshToken"`
			}
			_ = json.Unmarshal(request.Params, &params)
			event.RefreshToken = params.RefreshToken
		}
		if generation != 0 {
			if err := appendCodexHelperTrace(event); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
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
			if mode == "account-token-expired-once" && generation == 1 {
				writeCodexHelperRPCError(encoder, request.ID, "account token_expired", map[string]any{
					"code": "token_expired",
				})
				continue
			}
			if mode == "refresh-auth-fails" && generation == 2 {
				const sentinelSecret = "sentinel-refresh-secret-must-not-leak"
				writeCodexHelperRPCError(encoder, request.ID, "refresh failed: "+sentinelSecret, map[string]any{
					"code":       "invalid_grant",
					"diagnostic": sentinelSecret,
				})
				continue
			}
			if mode == "unauthenticated" {
				result = map[string]any{"requiresOpenaiAuth": true, "account": nil}
			} else {
				result = map[string]any{"requiresOpenaiAuth": true, "account": map[string]any{"type": "chatgpt", "planType": "pro", "email": nil}}
			}
		case "account/rateLimits/read":
			if mode == "unauthenticated" {
				return
			}
			if (mode == "rate-token-expired-once" || mode == "refresh-auth-fails") && generation == 1 {
				writeCodexHelperRPCError(encoder, request.ID, "rate limit token_expired", map[string]any{
					"code": "token_expired",
				})
				continue
			}
			if mode == "rate-structured-401-once" && generation == 1 {
				writeCodexHelperRPCError(encoder, request.ID, "rate-limit authentication rejected", map[string]any{
					"httpStatus": 401,
				})
				continue
			}
			if mode == "rate-forbidden" {
				writeCodexHelperRPCError(encoder, request.ID, "rate-limit request forbidden", map[string]any{
					"httpStatus": 403,
					"code":       "forbidden",
				})
				continue
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

func advanceCodexHelperGeneration(path string) (int, error) {
	generation := 0
	payload, err := os.ReadFile(path)
	if err == nil {
		generation, err = strconv.Atoi(strings.TrimSpace(string(payload)))
		if err != nil {
			return 0, fmt.Errorf("decode Codex helper generation: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("read Codex helper generation: %w", err)
	}
	generation++
	if err := os.WriteFile(path, []byte(strconv.Itoa(generation)), 0o600); err != nil {
		return 0, fmt.Errorf("write Codex helper generation: %w", err)
	}
	return generation, nil
}

func appendCodexHelperTrace(event codexHelperTraceEvent) error {
	path := os.Getenv("CODEX_HELPER_TRACE")
	if path == "" {
		return nil
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode Codex helper trace: %w", err)
	}
	payload = append(payload, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open Codex helper trace: %w", err)
	}
	_, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write Codex helper trace: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Codex helper trace: %w", closeErr)
	}
	return nil
}

func writeCodexHelperRPCError(encoder *json.Encoder, id int64, message string, data any) {
	_ = encoder.Encode(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    -32603,
			"message": message,
			"data":    data,
		},
	})
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
