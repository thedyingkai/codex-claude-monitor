package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-claude-monitor/internal/firmware"
	"codex-claude-monitor/internal/model"
	"codex-claude-monitor/internal/store"
)

const agentOnceHelperEnvironment = "GO_WANT_QUOTA_MONITOR_AGENT_ONCE_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(agentOnceHelperEnvironment) == "1" {
		runAgentOnceHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runAgentOnceHelper() {
	if len(os.Args) > 1 && os.Args[1] == "app-server" {
		scanner := bufio.NewScanner(os.Stdin)
		encoder := json.NewEncoder(os.Stdout)
		for scanner.Scan() {
			var request struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == 0 {
				continue
			}
			var result any = map[string]any{}
			switch request.Method {
			case "account/read":
				result = map[string]any{
					"requiresOpenaiAuth": true,
					"account":            map[string]any{"type": "chatgpt", "planType": "pro"},
				}
			case "account/rateLimits/read":
				result = map[string]any{"rateLimits": map[string]any{
					"primary": map[string]any{
						"usedPercent": 10, "windowDurationMins": 300, "resetsAt": 1786320000,
					},
				}}
			}
			_ = encoder.Encode(map[string]any{"id": request.ID, "result": result})
			if request.Method == "account/rateLimits/read" {
				return
			}
		}
		return
	}

	// The Claude collector checks auth before /usage. A logged-out fixture is
	// enough to exercise the process boundary without invoking a second probe.
	_, _ = os.Stdout.WriteString("{\"loggedIn\":false}\n")
}

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run returned %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("version output is empty")
	}
}

func TestFirmwarePublishCommand(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := filepath.Join(t.TempDir(), "firmware.bin")
	payload := []byte("e32r28t command fixture")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"firmware", "publish",
		"--board", "e32r28t",
		"--version", "0.3.0",
		"--file", source,
		"--firmware-dir", directory,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("firmware publish returned %d: %s", code, stderr.String())
	}
	var manifest firmware.Manifest
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("decode command output: %v (%s)", err, stdout.String())
	}
	if manifest.Board != firmware.BoardE32R28T || manifest.Version != "0.3.0" || manifest.SizeBytes != int64(len(payload)) {
		t.Fatalf("manifest = %+v", manifest)
	}
	loaded, err := firmware.LoadManifest(directory)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != manifest {
		t.Fatalf("stored manifest = %+v; want %+v", loaded, manifest)
	}
}

func TestFirmwarePublishCommandValidatesRequiredFlags(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"firmware"},
		{"firmware", "unknown"},
		{"firmware", "publish"},
		{"firmware", "publish", "--board", "e32r28t", "--version", "0.3.0"},
		{"firmware", "publish", "unexpected"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Errorf("run(%q) unexpectedly succeeded: %s", args, stdout.String())
		}
	}
}

func TestResolveLoginExecutablePrefersPATH(t *testing.T) {
	want := filepath.Join("usr", "bin", "codex")
	got, err := resolveLoginExecutableForOS(
		"codex",
		"linux",
		func(name string) (string, error) {
			if name != "codex" {
				t.Fatalf("lookPath name = %q; want codex", name)
			}
			return want, nil
		},
		func() (string, error) {
			t.Fatal("userHomeDir called even though PATH lookup succeeded")
			return "", nil
		},
	)
	if err != nil {
		t.Fatalf("resolveLoginExecutableForOS() error = %v", err)
	}
	if got != want {
		t.Fatalf("resolveLoginExecutableForOS() = %q; want %q", got, want)
	}
}

func TestResolveLoginExecutableFallsBackToUnixUserLocalBin(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := resolveLoginExecutableForOS(
		"claude",
		"linux",
		func(string) (string, error) { return "", exec.ErrNotFound },
		func() (string, error) { return home, nil },
	)
	if err != nil {
		t.Fatalf("resolveLoginExecutableForOS() error = %v", err)
	}
	if got != want {
		t.Fatalf("resolveLoginExecutableForOS() = %q; want %q", got, want)
	}
}

func TestResolveLoginExecutableDoesNotUseUnixFallbackOnWindows(t *testing.T) {
	homeCalled := false
	_, err := resolveLoginExecutableForOS(
		"codex",
		"windows",
		func(string) (string, error) { return "", exec.ErrNotFound },
		func() (string, error) {
			homeCalled = true
			return t.TempDir(), nil
		},
	)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("resolveLoginExecutableForOS() error = %v; want exec.ErrNotFound", err)
	}
	if homeCalled {
		t.Fatal("userHomeDir called for Windows fallback")
	}
}

func TestResolveLoginExecutableRejectsFallbackDirectory(t *testing.T) {
	home := t.TempDir()
	candidate := filepath.Join(home, ".local", "bin", "codex")
	if err := os.MkdirAll(candidate, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := resolveLoginExecutableForOS(
		"codex",
		"linux",
		func(string) (string, error) { return "", exec.ErrNotFound },
		func() (string, error) { return home, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("resolveLoginExecutableForOS() error = %v; want non-regular-file error", err)
	}
}

func TestDoctorDoesNotFollowRedirects(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirected = true
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/healthz", http.StatusFound)
	}))
	defer redirector.Close()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"doctor", "--health-url", redirector.URL}, &stdout, &stderr); code != 1 {
		t.Fatalf("doctor returned %d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if redirected {
		t.Fatal("doctor followed a health redirect")
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"nope"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestLoadAgentConfigRejectsTrailingContent(t *testing.T) {
	t.Parallel()
	valid := `{"agentId":"desktop-1","serverUrl":"https://example.test","tokenFile":"agent.token","hookSecretFile":"hook.secret"}`
	for _, test := range []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "single object", content: valid + "\n\t", wantErr: false},
		{name: "second object", content: valid + "\n{}", wantErr: true},
		{name: "trailing garbage", content: valid + "\nnot-json", wantErr: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "agent.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadAgentConfig(path)
			if (err != nil) != test.wantErr {
				t.Fatalf("loadAgentConfig() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestAgentOnceDoesNotUseCollectIntervalAsDeadline(t *testing.T) {
	t.Setenv(agentOnceHelperEnvironment, "1")

	reported := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agent/report" || request.Header.Get("Authorization") != "Bearer test-agent-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case reported <- struct{}{}:
		default:
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "agent.token")
	hookSecretPath := filepath.Join(directory, "hook.secret")
	for path, value := range map[string]string{
		tokenPath: "test-agent-token\n", hookSecretPath: "test-hook-secret\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := agentFileConfig{
		AgentID: "desktop-1", ServerURL: server.URL,
		TokenFile: tokenPath, HookSecretFile: hookSecretPath,
		CodexCommand: os.Args[0], ClaudeCommand: os.Args[0],
		CollectInterval: "1ns", AllowInsecureHTTP: true,
	}
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "agent.json")
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	started := time.Now()
	if err := runAgent([]string{"--config", configPath, "--once"}, &stdout, &stderr); err != nil {
		t.Fatalf("runAgent --once: %v (stderr=%s)", err, stderr.String())
	}
	if time.Since(started) >= agentOnceTimeout {
		t.Fatalf("one-shot Agent exhausted its independent timeout")
	}
	select {
	case <-reported:
	default:
		t.Fatal("server did not receive the one-shot report")
	}
}

func TestEnsureDisplayTokenCreatesAndReusesMatchingPair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(directory, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tokenPath := filepath.Join(directory, "display.token")
	created, err := ensureDisplayToken(ctx, database, tokenPath)
	if err != nil || !created {
		t.Fatalf("first ensureDisplayToken() = %v, %v; want created", created, err)
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(token, "qmon_") {
		t.Fatalf("unexpected token format: %q", token)
	}
	if _, err := database.AuthenticateToken(ctx, token, store.ScopeDisplayRead); err != nil {
		t.Fatalf("created token does not authenticate: %v", err)
	}
	created, err = ensureDisplayToken(ctx, database, tokenPath)
	if err != nil || created {
		t.Fatalf("second ensureDisplayToken() = %v, %v; want reuse", created, err)
	}
}

func TestEnsureDisplayTokenRejectsDifferentDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	first, err := store.Open(ctx, filepath.Join(directory, "first.db"))
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(directory, "display.token")
	if _, err := ensureDisplayToken(ctx, first, tokenPath); err != nil {
		first.Close()
		t.Fatal(err)
	}
	first.Close()

	second, err := store.Open(ctx, filepath.Join(directory, "second.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := ensureDisplayToken(ctx, second, tokenPath); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ensureDisplayToken() error = %v; want database mismatch", err)
	}
}

func TestProviderStatusNotifierLogsTransitionsOnly(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	notifier := newProviderStatusNotifier(log.New(&output, "", 0))
	loggedOut := model.ProviderReport{AuthState: "unauthenticated", ErrorCode: "not_authenticated"}
	notifier.Observe(model.ProviderClaude, loggedOut)
	notifier.Observe(model.ProviderClaude, loggedOut)
	notifier.Observe(model.ProviderClaude, model.ProviderReport{AuthState: "authenticated"})
	text := output.String()
	if strings.Count(text, "quota-monitor login claude") != 1 {
		t.Fatalf("login prompt count is not one: %q", text)
	}
	if !strings.Contains(text, "登录已恢复") {
		t.Fatalf("recovery transition missing: %q", text)
	}
}
