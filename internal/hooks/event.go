// Package hooks safely integrates provider hook payloads with the local agent.
package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"codex-claude-monitor/internal/model"
)

const ManagedMarker = "quota-monitor-managed-v1"

// Event is the minimal normalized hook payload retained by the task ledger.
// Prompt text, working directory, tool arguments, and transcripts are never
// copied from the provider payload.
type Event struct {
	Provider  model.ProviderName `json:"provider"`
	Name      string             `json:"name"`
	SessionID string             `json:"sessionId"`
	TaskID    string             `json:"taskId,omitempty"`
	At        time.Time          `json:"at"`
	Probe     bool               `json:"probe,omitempty"`
}

func ParseEvent(provider model.ProviderName, overrideName string, payload []byte, now time.Time) (Event, error) {
	if provider != model.ProviderCodex && provider != model.ProviderClaude {
		return Event{}, errors.New("provider must be codex or claude")
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Event{}, fmt.Errorf("decode hook payload: %w", err)
	}
	name := overrideName
	if name == "" {
		name = firstString(raw, "hook_event_name", "hookEventName", "event_name", "eventName", "event")
	}
	name = canonicalEventName(name)
	if name == "" {
		return Event{}, errors.New("hook event name is missing")
	}
	rawSessionID := firstString(raw, "session_id", "sessionId", "conversation_id", "conversationId", "thread_id", "threadId")
	if rawSessionID == "" {
		return Event{}, errors.New("hook session id is missing")
	}
	rawTaskID := firstString(raw, "agent_id", "agentId", "subagent_id", "subagentId", "task_id", "taskId")
	if input, ok := raw["agent"].(map[string]any); ok && rawTaskID == "" {
		rawTaskID = firstString(input, "id", "agent_id", "agentId")
	}
	sessionID := opaqueID(provider, rawSessionID)
	taskID := ""
	if rawTaskID != "" {
		taskID = opaqueID(provider, rawTaskID)
	}
	at := now.UTC()
	for _, key := range []string{"timestamp", "at", "created_at", "createdAt"} {
		if text, ok := raw[key].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
				at = parsed.UTC()
				break
			}
		}
	}
	probe, _ := raw["quota_monitor_probe"].(bool)
	probe = probe || IsProbeEnvironment()
	return Event{Provider: provider, Name: name, SessionID: sessionID, TaskID: taskID, At: at, Probe: probe}, nil
}

func opaqueID(provider model.ProviderName, raw string) string {
	digest := sha256.Sum256([]byte(string(provider) + "\x00" + raw))
	return fmt.Sprintf("%x", digest[:16])
}

func canonicalEventName(value string) string {
	compact := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	switch compact {
	case "userpromptsubmit":
		return "UserPromptSubmit"
	case "stop":
		return "Stop"
	case "stopfailure":
		return "StopFailure"
	case "sessionend":
		return "SessionEnd"
	case "subagentstart":
		return "SubagentStart"
	case "subagentstop":
		return "SubagentStop"
	case "posttooluse":
		return "PostToolUse"
	default:
		return ""
	}
}

func IsProbeEnvironment() bool {
	return os.Getenv("QUOTA_MONITOR_PROBE") == "1" || os.Getenv("CLAUDE_CODE_QUOTA_MONITOR_PROBE") == "1"
}

type ForwardConfig struct {
	BaseURL    string
	Secret     string
	SecretFile string
	Client     *http.Client
}

func (c ForwardConfig) resolvedSecret() (string, error) {
	if c.Secret != "" {
		return c.Secret, nil
	}
	if c.SecretFile == "" {
		return "", errors.New("hook shared secret is missing")
	}
	payload, err := os.ReadFile(c.SecretFile)
	if err != nil {
		return "", fmt.Errorf("read hook secret: %w", err)
	}
	secret := strings.TrimSpace(string(payload))
	if secret == "" {
		return "", errors.New("hook shared secret file is empty")
	}
	return secret, nil
}

func (c ForwardConfig) client() *http.Client {
	client := http.Client{Timeout: time.Second}
	if c.Client != nil {
		client = *c.Client
	}
	// The receiver is a fixed loopback endpoint. Never forward the local hook
	// secret to a redirect target, even if another local process issues 30x.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

func ForwardEvent(ctx context.Context, cfg ForwardConfig, event Event) error {
	if event.Probe {
		return nil
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return forward(ctx, cfg, "/v1/events", payload)
}

func ForwardStatusLine(ctx context.Context, cfg ForwardConfig, payload []byte) error {
	return forward(ctx, cfg, "/v1/statusline", payload)
}

func forward(ctx context.Context, cfg ForwardConfig, path string, payload []byte) error {
	secret, err := cfg.resolvedSecret()
	if err != nil {
		return err
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:47632"
	}
	if err := validateLoopbackURL(base); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := cfg.client().Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("local agent returned %s", response.Status)
	}
	return nil
}

func validateLoopbackURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("hook URL must be an HTTP(S) loopback URL")
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("hook URL must resolve directly to loopback")
	}
	return nil
}

// PreviousStatusLine is the provider config captured before installing the
// quota-monitor wrapper.
type PreviousStatusLine struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
}

// RunStatusLineWrapper forwards quota JSON without blocking Claude's display,
// then passes the exact same stdin to a pre-existing status-line command and
// returns that command's stdout.
func RunStatusLineWrapper(ctx context.Context, cfg ForwardConfig, input []byte, previous *PreviousStatusLine, stderr io.Writer) ([]byte, error) {
	forwardCtx, cancel := context.WithTimeout(ctx, time.Second)
	_ = ForwardStatusLine(forwardCtx, cfg, input) // Hook telemetry must never block Claude.
	cancel()
	if previous == nil || strings.TrimSpace(previous.Command) == "" {
		return nil, nil
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", previous.Command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", previous.Command)
	}
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = stderr
	return cmd.Output()
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
