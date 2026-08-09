package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var codexEvents = []string{
	"UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop", "PostToolUse",
}

var claudeEvents = []string{
	"UserPromptSubmit", "Stop", "StopFailure", "SessionEnd", "SubagentStart", "SubagentStop", "PostToolUse",
}

type InstallConfig struct {
	HomeDir    string
	Executable string
	HookURL    string
	Secret     string

	CodexConfigPath  string
	ClaudeConfigPath string
	StatePath        string
	SecretPath       string
	TargetOS         string
}

type InstallResult struct {
	CodexPath  string
	ClaudePath string
	StatePath  string
	SecretPath string
	Backups    []string
}

type installState struct {
	Version                  int             `json:"version"`
	CodexPath                string          `json:"codexPath"`
	ClaudePath               string          `json:"claudePath"`
	SecretPath               string          `json:"secretPath"`
	PreviousStatusLineExists bool            `json:"previousStatusLineExists"`
	PreviousStatusLine       json.RawMessage `json:"previousStatusLine,omitempty"`
}

func (c InstallConfig) withDefaults() (InstallConfig, error) {
	if c.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return c, err
		}
		c.HomeDir = home
	}
	if c.Executable == "" {
		return c, errors.New("quota-monitor executable path is required")
	}
	if c.HookURL == "" {
		c.HookURL = "http://127.0.0.1:47632"
	}
	if err := validateLoopbackURL(c.HookURL); err != nil {
		return c, err
	}
	if c.Secret == "" {
		return c, errors.New("hook shared secret is required")
	}
	if c.CodexConfigPath == "" {
		c.CodexConfigPath = filepath.Join(c.HomeDir, ".codex", "hooks.json")
	}
	if c.ClaudeConfigPath == "" {
		c.ClaudeConfigPath = filepath.Join(c.HomeDir, ".claude", "settings.json")
	}
	stateDir := filepath.Join(c.HomeDir, ".quota-monitor")
	if c.StatePath == "" {
		c.StatePath = filepath.Join(stateDir, "hooks-state.json")
	}
	if c.SecretPath == "" {
		c.SecretPath = filepath.Join(stateDir, "hook-secret")
	}
	if c.TargetOS == "" {
		c.TargetOS = runtime.GOOS
	}
	return c, nil
}

// Install merges isolated managed entries into both providers' user configs.
// Every changed existing file is backed up before replacement.
func Install(cfg InstallConfig) (InstallResult, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return InstallResult{}, err
	}
	result := InstallResult{CodexPath: cfg.CodexConfigPath, ClaudePath: cfg.ClaudeConfigPath, StatePath: cfg.StatePath, SecretPath: cfg.SecretPath}
	if err := os.MkdirAll(filepath.Dir(cfg.StatePath), 0o700); err != nil {
		return result, err
	}
	state, stateErr := readInstallState(cfg.StatePath)
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return result, fmt.Errorf("read hook install state: %w", stateErr)
	}
	if state.Version != 1 {
		state = installState{Version: 1}
	}
	state.CodexPath, state.ClaudePath, state.SecretPath = cfg.CodexConfigPath, cfg.ClaudeConfigPath, cfg.SecretPath

	codexRoot, codexMode, err := readJSONObject(cfg.CodexConfigPath)
	if err != nil {
		return result, err
	}
	claudeRoot, claudeMode, err := readJSONObject(cfg.ClaudeConfigPath)
	if err != nil {
		return result, err
	}

	command := managedCommand(cfg.TargetOS, cfg.Executable, "hook", "forward", "--provider", "codex", "--url", cfg.HookURL, "--secret-file", cfg.SecretPath, "--managed-by", ManagedMarker)
	mergeManagedHooks(codexRoot, codexEvents, command)
	claudeCommand := managedCommand(cfg.TargetOS, cfg.Executable, "hook", "forward", "--provider", "claude", "--url", cfg.HookURL, "--secret-file", cfg.SecretPath, "--managed-by", ManagedMarker)
	mergeManagedHooks(claudeRoot, claudeEvents, claudeCommand)

	currentStatus, hasStatus := claudeRoot["statusLine"]
	if !isManagedValue(currentStatus) {
		state.PreviousStatusLineExists = hasStatus
		if hasStatus {
			state.PreviousStatusLine, _ = json.Marshal(currentStatus)
		} else {
			state.PreviousStatusLine = nil
		}
	}
	statusCommand := managedCommand(cfg.TargetOS, cfg.Executable, "hook", "statusline", "--url", cfg.HookURL, "--secret-file", cfg.SecretPath, "--state-file", cfg.StatePath, "--managed-by", ManagedMarker)
	claudeRoot["statusLine"] = map[string]any{
		"type": "command", "command": statusCommand, "refreshInterval": 15,
	}
	// Persist restoration data before making the wrapper visible.
	statePayload, _ := json.MarshalIndent(state, "", "  ")
	statePayload = append(statePayload, '\n')

	mutations := []fileMutation{
		{path: cfg.StatePath, payload: statePayload, mode: 0o600},
		{path: cfg.SecretPath, payload: []byte(cfg.Secret + "\n"), mode: 0o600},
	}
	codexMutations, codexBackup, err := jsonFileMutations(cfg.CodexConfigPath, codexRoot, codexMode)
	if err != nil {
		return result, err
	}
	mutations = append(mutations, codexMutations...)
	claudeMutations, claudeBackup, err := jsonFileMutations(cfg.ClaudeConfigPath, claudeRoot, claudeMode)
	if err != nil {
		return result, err
	}
	mutations = append(mutations, claudeMutations...)

	// Treat state, secret, backups and both provider configs as one logical
	// installation. In particular, a Windows rename/permission failure while
	// replacing the later Claude config must not leave Codex partially installed.
	if err := applyFileTransaction(mutations); err != nil {
		return result, fmt.Errorf("install hooks transaction: %w", err)
	}
	if codexBackup != "" {
		result.Backups = append(result.Backups, codexBackup)
	}
	if claudeBackup != "" {
		result.Backups = append(result.Backups, claudeBackup)
	}
	return result, nil
}

// Uninstall removes only entries carrying ManagedMarker. If the current Claude
// statusLine is still the wrapper, the exact pre-install value is restored.
func Uninstall(cfg InstallConfig) (InstallResult, error) {
	if cfg.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return InstallResult{}, err
		}
		cfg.HomeDir = home
	}
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(cfg.HomeDir, ".quota-monitor", "hooks-state.json")
	}
	state, stateErr := readInstallState(cfg.StatePath)
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return InstallResult{}, fmt.Errorf("read hook install state: %w", stateErr)
	}
	if cfg.CodexConfigPath == "" {
		cfg.CodexConfigPath = state.CodexPath
		if cfg.CodexConfigPath == "" {
			cfg.CodexConfigPath = filepath.Join(cfg.HomeDir, ".codex", "hooks.json")
		}
	}
	if cfg.ClaudeConfigPath == "" {
		cfg.ClaudeConfigPath = state.ClaudePath
		if cfg.ClaudeConfigPath == "" {
			cfg.ClaudeConfigPath = filepath.Join(cfg.HomeDir, ".claude", "settings.json")
		}
	}
	if cfg.SecretPath == "" {
		cfg.SecretPath = state.SecretPath
		if cfg.SecretPath == "" {
			cfg.SecretPath = filepath.Join(cfg.HomeDir, ".quota-monitor", "hook-secret")
		}
	}
	result := InstallResult{CodexPath: cfg.CodexConfigPath, ClaudePath: cfg.ClaudeConfigPath, StatePath: cfg.StatePath, SecretPath: cfg.SecretPath}

	if root, mode, err := readJSONObjectIfExists(cfg.CodexConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	} else if err == nil && root != nil && removeManagedHooks(root) {
		backup, err := backupAndWriteJSON(cfg.CodexConfigPath, root, mode)
		if err != nil {
			return result, err
		}
		if backup != "" {
			result.Backups = append(result.Backups, backup)
		}
	}
	if root, mode, err := readJSONObjectIfExists(cfg.ClaudeConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	} else if err == nil && root != nil {
		changed := removeManagedHooks(root)
		if current, ok := root["statusLine"]; ok && isManagedValue(current) {
			changed = true
			if state.PreviousStatusLineExists && len(state.PreviousStatusLine) > 0 {
				var previous any
				if err := json.Unmarshal(state.PreviousStatusLine, &previous); err != nil {
					return result, fmt.Errorf("restore previous statusLine: %w", err)
				}
				root["statusLine"] = previous
			} else {
				delete(root, "statusLine")
			}
		}
		if changed {
			backup, err := backupAndWriteJSON(cfg.ClaudeConfigPath, root, mode)
			if err != nil {
				return result, err
			}
			if backup != "" {
				result.Backups = append(result.Backups, backup)
			}
		}
	}
	_ = os.Remove(cfg.SecretPath)
	_ = os.Remove(cfg.StatePath)
	return result, nil
}

func LoadPreviousStatusLine(statePath string) (*PreviousStatusLine, error) {
	state, err := readInstallState(statePath)
	if err != nil {
		return nil, err
	}
	if !state.PreviousStatusLineExists || len(state.PreviousStatusLine) == 0 {
		return nil, nil
	}
	var previous PreviousStatusLine
	if err := json.Unmarshal(state.PreviousStatusLine, &previous); err != nil {
		return nil, err
	}
	if previous.Command == "" {
		return nil, nil
	}
	return &previous, nil
}

func mergeManagedHooks(root map[string]any, events []string, command string) {
	hooksObject, ok := root["hooks"].(map[string]any)
	if !ok {
		hooksObject = make(map[string]any)
		root["hooks"] = hooksObject
	}
	for _, event := range events {
		groups, _ := hooksObject[event].([]any)
		groups, _ = filterManagedGroups(groups)
		groups = append(groups, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}},
		})
		hooksObject[event] = groups
	}
}

func removeManagedHooks(root map[string]any) bool {
	hooksObject, ok := root["hooks"].(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for event, raw := range hooksObject {
		groups, ok := raw.([]any)
		if !ok {
			continue
		}
		filtered, removed := filterManagedGroups(groups)
		if !removed {
			continue
		}
		changed = true
		if len(filtered) == 0 {
			delete(hooksObject, event)
		} else {
			hooksObject[event] = filtered
		}
	}
	return changed
}

func filterManagedGroups(groups []any) ([]any, bool) {
	result := make([]any, 0, len(groups))
	removed := false
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			result = append(result, rawGroup)
			continue
		}
		rawCommands, ok := group["hooks"].([]any)
		if !ok {
			result = append(result, rawGroup)
			continue
		}
		commands := make([]any, 0, len(rawCommands))
		for _, rawCommand := range rawCommands {
			if isManagedValue(rawCommand) {
				removed = true
				continue
			}
			commands = append(commands, rawCommand)
		}
		if len(commands) > 0 {
			copyGroup := make(map[string]any, len(group))
			for key, value := range group {
				copyGroup[key] = value
			}
			copyGroup["hooks"] = commands
			result = append(result, copyGroup)
		}
	}
	return result, removed
}

func isManagedValue(value any) bool {
	payload, _ := json.Marshal(value)
	return bytes.Contains(payload, []byte(ManagedMarker))
}

func managedCommand(targetOS, executable string, args ...string) string {
	values := append([]string{executable}, args...)
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, shellQuote(targetOS, value))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(targetOS, value string) string {
	if targetOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return `'` + strings.ReplaceAll(value, `'`, `'"'"'`) + `'`
}

func readInstallState(path string) (installState, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return installState{}, err
	}
	var state installState
	if err := json.Unmarshal(payload, &state); err != nil {
		return installState{}, err
	}
	return state, nil
}

func readJSONObject(path string) (map[string]any, os.FileMode, error) {
	root, mode, err := readJSONObjectIfExists(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]any), 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return root, mode, nil
}

func readJSONObjectIfExists(path string) (map[string]any, os.FileMode, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, 0, fmt.Errorf("parse %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	return root, info.Mode().Perm(), nil
}

func backupAndWriteJSON(path string, root map[string]any, mode os.FileMode) (string, error) {
	if mode == 0 {
		mode = 0o600
	}
	backup := ""
	if _, err := os.Stat(path); err == nil {
		original, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		backup = path + ".quota-monitor." + time.Now().UTC().Format("20060102T150405.000000000Z") + ".bak"
		if err := atomicWrite(backup, original, mode); err != nil {
			return "", fmt.Errorf("backup %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	payload, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	if err := atomicWrite(path, payload, mode); err != nil {
		return "", err
	}
	return backup, nil
}

type fileMutation struct {
	path    string
	payload []byte
	mode    os.FileMode
}

type fileSnapshot struct {
	path    string
	payload []byte
	mode    os.FileMode
	exists  bool
}

func jsonFileMutations(path string, root map[string]any, mode os.FileMode) ([]fileMutation, string, error) {
	if mode == 0 {
		mode = 0o600
	}
	payload, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, "", err
	}
	payload = append(payload, '\n')

	mutations := make([]fileMutation, 0, 2)
	backup := ""
	if info, err := os.Stat(path); err == nil {
		original, err := os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		backup = path + ".quota-monitor." + time.Now().UTC().Format("20060102T150405.000000000Z") + ".bak"
		mutations = append(mutations, fileMutation{path: backup, payload: original, mode: info.Mode().Perm()})
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	mutations = append(mutations, fileMutation{path: path, payload: payload, mode: mode})
	return mutations, backup, nil
}

// atomicRename is replaceable in tests so Windows-style rename/permission
// failures can be reproduced without depending on the host filesystem ACLs.
var atomicRename = os.Rename

func applyFileTransaction(mutations []fileMutation) error {
	snapshots := make([]fileSnapshot, len(mutations))
	seen := make(map[string]struct{}, len(mutations))
	for i, mutation := range mutations {
		cleanPath := filepath.Clean(mutation.path)
		if _, duplicate := seen[cleanPath]; duplicate {
			return fmt.Errorf("duplicate transaction path %s", mutation.path)
		}
		seen[cleanPath] = struct{}{}
		snapshot, err := snapshotFile(mutation.path)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", mutation.path, err)
		}
		snapshots[i] = snapshot
	}

	for i, mutation := range mutations {
		mode := mutation.mode
		if mode == 0 {
			mode = 0o600
		}
		if err := atomicWrite(mutation.path, mutation.payload, mode); err != nil {
			writeErr := fmt.Errorf("write %s: %w", mutation.path, err)
			if rollbackErr := rollbackFileTransaction(snapshots[:i+1]); rollbackErr != nil {
				return errors.Join(writeErr, fmt.Errorf("rollback: %w", rollbackErr))
			}
			return writeErr
		}
	}
	return nil
}

func snapshotFile(path string) (fileSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{path: path, payload: payload, mode: info.Mode().Perm(), exists: true}, nil
}

func rollbackFileTransaction(snapshots []fileSnapshot) error {
	var rollbackErrors []error
	for i := len(snapshots) - 1; i >= 0; i-- {
		snapshot := snapshots[i]
		if snapshot.exists {
			if err := atomicWrite(snapshot.path, snapshot.payload, snapshot.mode); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", snapshot.path, err))
			}
			continue
		}
		if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove %s: %w", snapshot.path, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func atomicWrite(path string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".quota-monitor-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return atomicRename(temporaryPath, path)
}
