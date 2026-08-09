package hooks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-claude-monitor/internal/model"
)

func TestParseEventCopiesOnlyMinimalFields(t *testing.T) {
	t.Setenv("QUOTA_MONITOR_PROBE", "")
	payload := []byte(`{"hook_event_name":"subagent_start","session_id":"parent","agent_id":"child","prompt":"secret","cwd":"C:/private"}`)
	event, err := ParseEvent(model.ProviderCodex, "", payload, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if event.Name != "SubagentStart" || event.SessionID == "parent" || event.TaskID == "child" || len(event.SessionID) != 32 || len(event.TaskID) != 32 {
		t.Fatalf("bad event: %+v", event)
	}
}

func TestParseEventMarksProbeEnvironment(t *testing.T) {
	t.Setenv("QUOTA_MONITOR_PROBE", "1")
	event, err := ParseEvent(model.ProviderClaude, "Stop", []byte(`{"session_id":"s"}`), time.Now())
	if err != nil || !event.Probe {
		t.Fatalf("expected probe event: %+v %v", event, err)
	}
}

func TestForwardEventUsesSharedSecretFile(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath, gotAuth = request.URL.Path, request.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, request.Body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	secretPath := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretPath, []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ForwardEvent(context.Background(), ForwardConfig{BaseURL: server.URL, SecretFile: secretPath}, Event{Provider: model.ProviderCodex, Name: "Stop", SessionID: "s"})
	if err != nil || gotPath != "/v1/events" || gotAuth != "Bearer shared" {
		t.Fatalf("forward failed: %v path=%s auth=%s", err, gotPath, gotAuth)
	}
}

func TestForwardProbeIsSuppressed(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	err := ForwardEvent(context.Background(), ForwardConfig{BaseURL: server.URL, Secret: "s"}, Event{Probe: true})
	if err != nil || called {
		t.Fatalf("probe should not be forwarded: err=%v called=%v", err, called)
	}
}

func TestForwardRejectsNonLoopbackURL(t *testing.T) {
	err := ForwardEvent(context.Background(), ForwardConfig{BaseURL: "https://example.com", Secret: "s"}, Event{Provider: model.ProviderCodex, Name: "Stop", SessionID: "s"})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback rejection, got %v", err)
	}
}

func TestForwardEventDoesNotFollowRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirected = true
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	err := ForwardEvent(context.Background(), ForwardConfig{BaseURL: redirector.URL, Secret: "hook-secret"}, Event{Provider: model.ProviderCodex, Name: "Stop", SessionID: "s"})
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("expected redirect to fail, got %v", err)
	}
	if redirected {
		t.Fatal("hook client followed redirect")
	}
}

func TestRunStatusLineWrapperIgnoresForwardFailureWithoutPrevious(t *testing.T) {
	output, err := RunStatusLineWrapper(context.Background(), ForwardConfig{BaseURL: "http://127.0.0.1:1", Secret: "s"}, []byte(`{}`), nil, io.Discard)
	if err != nil || len(output) != 0 {
		t.Fatalf("status line was blocked: %q %v", output, err)
	}
}

func TestParseEventRejectsUnknownEvent(t *testing.T) {
	_, err := ParseEvent(model.ProviderCodex, "made-up", []byte(`{"session_id":"s"}`), time.Now())
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected name error, got %v", err)
	}
}
