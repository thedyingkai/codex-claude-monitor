package hooks

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallIsIdempotentAndUninstallRestoresUserConfig(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, ".codex", "hooks.json")
	claudePath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalCodex := `{"custom":"keep","hooks":{"Stop":[{"hooks":[{"type":"command","command":"pet-hook"}]}]}}`
	originalStatus := map[string]any{"type": "command", "command": "my-status", "padding": 2.0}
	originalClaudeObject := map[string]any{"env": map[string]any{"KEEP": "yes"}, "statusLine": originalStatus, "hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "my-claude-hook"}}}}}}
	originalClaude, _ := json.Marshal(originalClaudeObject)
	if err := os.WriteFile(codexPath, []byte(originalCodex), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, originalClaude, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := InstallConfig{HomeDir: home, Executable: filepath.Join(home, "quota monitor"), HookURL: "http://127.0.0.1:47632", Secret: "secret", TargetOS: "windows"}
	first, err := Install(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Backups) != 2 {
		t.Fatalf("expected two backups, got %#v", first.Backups)
	}
	if _, err := Install(cfg); err != nil {
		t.Fatalf("repeat install: %v", err)
	}

	codex := readJSONForTest(t, codexPath)
	codexHooks := codex["hooks"].(map[string]any)
	if _, exists := codexHooks["StopFailure"]; exists {
		t.Fatal("unsupported Codex StopFailure hook was installed")
	}
	if countManaged(codexHooks["Stop"]) != 1 || !containsCommand(codexHooks["Stop"], "pet-hook") {
		t.Fatalf("Codex hooks not merged idempotently: %#v", codexHooks["Stop"])
	}
	claude := readJSONForTest(t, claudePath)
	claudeHooks := claude["hooks"].(map[string]any)
	if countManaged(claudeHooks["StopFailure"]) != 1 || countManaged(claudeHooks["Stop"]) != 1 {
		t.Fatalf("Claude managed hooks wrong: %#v", claudeHooks)
	}
	previous, err := LoadPreviousStatusLine(first.StatePath)
	if err != nil || previous == nil || previous.Command != "my-status" {
		t.Fatalf("previous status line not captured: %+v %v", previous, err)
	}

	result, err := Uninstall(InstallConfig{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Backups) != 2 {
		t.Fatalf("uninstall backups missing: %#v", result.Backups)
	}
	codex = readJSONForTest(t, codexPath)
	codexHooks = codex["hooks"].(map[string]any)
	if countManaged(codexHooks["Stop"]) != 0 || !containsCommand(codexHooks["Stop"], "pet-hook") || codex["custom"] != "keep" {
		t.Fatalf("Codex user config damaged: %#v", codex)
	}
	claude = readJSONForTest(t, claudePath)
	status := claude["statusLine"].(map[string]any)
	if status["command"] != "my-status" || status["padding"] != 2.0 || claude["env"].(map[string]any)["KEEP"] != "yes" {
		t.Fatalf("Claude user config damaged: %#v", claude)
	}
	if _, err := os.Stat(first.StatePath); !os.IsNotExist(err) {
		t.Fatalf("managed state remains after uninstall: %v", err)
	}
	if _, err := Uninstall(InstallConfig{HomeDir: home}); err != nil {
		t.Fatalf("repeat uninstall must be idempotent: %v", err)
	}
}

func TestUninstallIgnoresMissingProviderConfigs(t *testing.T) {
	home := t.TempDir()
	if _, err := Uninstall(InstallConfig{HomeDir: home}); err != nil {
		t.Fatalf("missing configs should be a no-op: %v", err)
	}
}

func TestInstallRefusesCorruptExistingConfig(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Install(InstallConfig{HomeDir: home, Executable: "quota-monitor", Secret: "s"})
	if err == nil {
		t.Fatal("corrupt config was silently overwritten")
	}
	payload, _ := os.ReadFile(path)
	if string(payload) != `{broken` {
		t.Fatalf("corrupt user file changed: %q", payload)
	}
}

func TestInstallRollsBackWhenClaudeRenameIsDenied(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, ".codex", "hooks.json")
	claudePath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalCodex := []byte("{\n  \"custom\": \"codex-user-value\"\n}\n")
	originalClaude := []byte("{\n  \"env\": {\"CLAUDE_USER_VALUE\": \"keep\"}\n}\n")
	if err := os.WriteFile(codexPath, originalCodex, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, originalClaude, 0o600); err != nil {
		t.Fatal(err)
	}

	originalRename := atomicRename
	failed := false
	atomicRename = func(oldPath, newPath string) error {
		if !failed && filepath.Clean(newPath) == filepath.Clean(claudePath) {
			failed = true
			return fs.ErrPermission
		}
		return originalRename(oldPath, newPath)
	}
	t.Cleanup(func() { atomicRename = originalRename })

	cfg := InstallConfig{HomeDir: home, Executable: "quota-monitor", Secret: "do-not-persist", TargetOS: "windows"}
	_, err := Install(cfg)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected injected permission error, got %v", err)
	}
	if !failed {
		t.Fatal("Claude config rename failure was not injected")
	}
	assertFilePayload(t, codexPath, originalCodex)
	assertFilePayload(t, claudePath, originalClaude)
	assertMissing(t, filepath.Join(home, ".quota-monitor", "hooks-state.json"))
	assertMissing(t, filepath.Join(home, ".quota-monitor", "hook-secret"))
	assertNoNewBackups(t, codexPath, 0)
	assertNoNewBackups(t, claudePath, 0)
}

func TestRepeatInstallFailureRestoresPreviousManagedFiles(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, ".codex", "hooks.json")
	claudePath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte(`{"codex":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(`{"claude":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	firstConfig := InstallConfig{HomeDir: home, Executable: "quota-monitor-v1", Secret: "first-secret", TargetOS: "windows"}
	first, err := Install(firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	trackedPaths := []string{first.StatePath, first.SecretPath, codexPath, claudePath}
	before := make(map[string][]byte, len(trackedPaths))
	for _, path := range trackedPaths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	codexBackupCount := backupCount(t, codexPath)
	claudeBackupCount := backupCount(t, claudePath)

	originalRename := atomicRename
	failed := false
	atomicRename = func(oldPath, newPath string) error {
		if !failed && filepath.Clean(newPath) == filepath.Clean(claudePath) {
			failed = true
			return fs.ErrPermission
		}
		return originalRename(oldPath, newPath)
	}
	t.Cleanup(func() { atomicRename = originalRename })

	secondConfig := InstallConfig{HomeDir: home, Executable: "quota-monitor-v2", Secret: "second-secret", TargetOS: "windows"}
	if _, err := Install(secondConfig); err == nil || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected repeat-install permission error, got %v", err)
	}
	for _, path := range trackedPaths {
		assertFilePayload(t, path, before[path])
	}
	assertNoNewBackups(t, codexPath, codexBackupCount)
	assertNoNewBackups(t, claudePath, claudeBackupCount)

	// A failed repeat install must not make the prior installation impossible
	// to remove, nor alter the user's original provider settings.
	atomicRename = originalRename
	if _, err := Uninstall(InstallConfig{HomeDir: home}); err != nil {
		t.Fatalf("uninstall after rolled-back repeat install: %v", err)
	}
	if readJSONForTest(t, codexPath)["codex"] != "keep" {
		t.Fatal("Codex user value was not preserved")
	}
	if readJSONForTest(t, claudePath)["claude"] != "keep" {
		t.Fatal("Claude user value was not preserved")
	}
}

func readJSONForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func countManaged(value any) int {
	payload, _ := json.Marshal(value)
	return strings.Count(string(payload), ManagedMarker)
}

func containsCommand(value any, command string) bool {
	payload, _ := json.Marshal(value)
	return strings.Contains(string(payload), command)
}

func assertFilePayload(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s changed:\n got: %q\nwant: %q", path, got, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
	}
}

func assertNoNewBackups(t *testing.T, path string, want int) {
	t.Helper()
	if got := backupCount(t, path); got != want {
		t.Fatalf("backup count for %s = %d, want %d", path, got, want)
	}
}

func backupCount(t *testing.T, path string) int {
	t.Helper()
	matches, err := filepath.Glob(path + ".quota-monitor.*.bak")
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}
