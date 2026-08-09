package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"codex-claude-monitor/internal/agent"
	"codex-claude-monitor/internal/collector"
	"codex-claude-monitor/internal/hooks"
	"codex-claude-monitor/internal/model"
	"codex-claude-monitor/internal/server"
	"codex-claude-monitor/internal/store"
)

var version = "dev"

// A one-shot collection can legitimately spend up to 45 seconds in the Codex
// app-server initialization/account/rate-limit calls and up to 30 seconds in
// Claude. Standalone can run the providers serially, so the timeout covers the
// sum of their probe limits. Agent mode also needs a separate allowance for the
// HTTPS report instead of reusing collectInterval, which is a scheduling knob
// and may intentionally be much shorter than either CLI timeout.
const agentOnceTimeout = 75 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	var err error
	switch args[0] {
	case "standalone":
		err = runStandalone(args[1:], stdout, stderr)
	case "server":
		err = runServer(args[1:], stdout, stderr)
	case "agent":
		err = runAgent(args[1:], stdout, stderr)
	case "hooks":
		err = runHooks(args[1:], stdout, stderr)
	case "hook":
		err = runHookHelper(args[1:], stdout, stderr)
	case "service":
		err = runService(args[1:], stdout, stderr)
	case "token":
		err = runToken(args[1:], stdout, stderr)
	case "doctor":
		err = runDoctor(args[1:], stdout, stderr)
	case "login":
		err = runLogin(args[1:], stdout, stderr)
	case "version", "--version", "-version":
		_, err = fmt.Fprintln(stdout, version)
	case "help", "--help", "-h":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func runHooks(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("hooks requires install or uninstall")
	}
	switch args[0] {
	case "install":
		return runHooksInstall(args[1:], stdout, stderr)
	case "uninstall":
		return runHooksUninstall(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown hooks command %q", args[0])
	}
}

func runHooksInstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hooks install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	executableDefault, _ := os.Executable()
	executable := fs.String("executable", executableDefault, "absolute quota-monitor executable path")
	home := fs.String("home", "", "target user home (defaults to current user)")
	hookURL := fs.String("url", "http://127.0.0.1:47632", "local agent hook URL")
	secretPath := fs.String("secret-file", "", "shared secret path")
	statePath := fs.String("state-file", "", "installation state path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("hooks install accepts no positional arguments")
	}
	if *executable == "" {
		return errors.New("cannot determine executable path; pass --executable")
	}
	absExecutable, err := filepath.Abs(*executable)
	if err != nil {
		return err
	}
	resolvedHome := *home
	if resolvedHome == "" {
		resolvedHome, err = os.UserHomeDir()
		if err != nil {
			return err
		}
	}
	if *secretPath == "" {
		*secretPath = filepath.Join(resolvedHome, ".quota-monitor", "hook-secret")
	}
	secret, err := loadOrCreateSecret(*secretPath)
	if err != nil {
		return err
	}
	result, err := hooks.Install(hooks.InstallConfig{
		HomeDir: resolvedHome, Executable: absExecutable, HookURL: *hookURL,
		Secret: secret, SecretPath: *secretPath, StatePath: *statePath,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runHooksUninstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hooks uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", "", "target user home (defaults to current user)")
	secretPath := fs.String("secret-file", "", "override shared secret path")
	statePath := fs.String("state-file", "", "override installation state path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("hooks uninstall accepts no positional arguments")
	}
	result, err := hooks.Uninstall(hooks.InstallConfig{HomeDir: *home, SecretPath: *secretPath, StatePath: *statePath})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runHookHelper(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("hook requires forward or statusline")
	}
	switch args[0] {
	case "forward":
		return runHookForward(args[1:], stderr)
	case "statusline":
		return runHookStatusLine(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown hook helper %q", args[0])
	}
}

func runHookForward(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook forward", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "", "codex or claude")
	hookURL := fs.String("url", "http://127.0.0.1:47632", "local hook receiver URL")
	secretFile := fs.String("secret-file", "", "local hook secret file")
	_ = fs.String("managed-by", "", "installer marker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *providerName == "" || *secretFile == "" {
		return errors.New("hook forward requires --provider and --secret-file")
	}
	payload, err := readLimited(os.Stdin, agent.MaxHookPayload)
	if err != nil {
		fmt.Fprintf(stderr, "quota-monitor hook ignored: %v\n", err)
		return nil
	}
	event, err := hooks.ParseEvent(model.ProviderName(*providerName), "", payload, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "quota-monitor hook ignored: %v\n", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := hooks.ForwardEvent(ctx, hooks.ForwardConfig{BaseURL: *hookURL, SecretFile: *secretFile}, event); err != nil {
		fmt.Fprintf(stderr, "quota-monitor hook delivery skipped: %v\n", err)
	}
	return nil
}

func runHookStatusLine(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook statusline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hookURL := fs.String("url", "http://127.0.0.1:47632", "local hook receiver URL")
	secretFile := fs.String("secret-file", "", "local hook secret file")
	statePath := fs.String("state-file", "", "hook installation state file")
	_ = fs.String("managed-by", "", "installer marker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *secretFile == "" || *statePath == "" {
		return errors.New("hook statusline requires --state-file")
	}
	payload, err := readLimited(os.Stdin, agent.MaxHookPayload)
	if err != nil {
		return nil // Status-line telemetry must never block Claude.
	}
	previous, previousErr := hooks.LoadPreviousStatusLine(*statePath)
	if previousErr != nil {
		fmt.Fprintf(stderr, "quota-monitor statusline state skipped: %v\n", previousErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, wrapperErr := hooks.RunStatusLineWrapper(ctx, hooks.ForwardConfig{BaseURL: *hookURL, SecretFile: *secretFile}, payload, previous, stderr)
	if len(output) > 0 {
		_, _ = stdout.Write(output)
	}
	if wrapperErr != nil {
		fmt.Fprintf(stderr, "quota-monitor previous statusline failed: %v\n", wrapperErr)
	}
	return nil
}

func readLimited(reader io.Reader, maximum int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maximum {
		return nil, fmt.Errorf("input exceeds %d bytes", maximum)
	}
	return payload, nil
}

func loadOrCreateSecret(path string) (string, error) {
	if payload, err := os.ReadFile(path); err == nil {
		secret := strings.TrimSpace(string(payload))
		if secret != "" {
			return secret, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(buffer)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}

type agentFileConfig struct {
	AgentID              string                        `json:"agentId"`
	ServerURL            string                        `json:"serverUrl"`
	TokenFile            string                        `json:"tokenFile"`
	HookAddr             string                        `json:"hookAddr"`
	HookSecretFile       string                        `json:"hookSecretFile"`
	CodexCommand         string                        `json:"codexCommand"`
	ClaudeCommand        string                        `json:"claudeCommand"`
	PlanOverrides        map[model.ProviderName]string `json:"planOverrides,omitempty"`
	ReportInterval       string                        `json:"reportInterval"`
	CollectInterval      string                        `json:"collectInterval"`
	SequentialCollection bool                          `json:"sequentialCollection,omitempty"`
	MaxBackoff           string                        `json:"maxBackoff"`
	AllowInsecureHTTP    bool                          `json:"allowInsecureHttp"`
}

func runAgent(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", envOr("QUOTA_MONITOR_AGENT_CONFIG", "agent.json"), "agent JSON config path")
	once := fs.Bool("once", false, "collect and report once, then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("agent accepts no positional arguments")
	}

	fileConfig, err := loadAgentConfig(*configPath)
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(*configPath)
	token, err := readSecretFile(baseDir, fileConfig.TokenFile)
	if err != nil {
		return fmt.Errorf("agent token: %w", err)
	}
	hookSecret, err := readSecretFile(baseDir, fileConfig.HookSecretFile)
	if err != nil {
		return fmt.Errorf("hook secret: %w", err)
	}
	reportInterval, err := durationOrDefault(fileConfig.ReportInterval, agent.DefaultReportInterval)
	if err != nil {
		return fmt.Errorf("reportInterval: %w", err)
	}
	collectInterval, err := durationOrDefault(fileConfig.CollectInterval, agent.DefaultCollectInterval)
	if err != nil {
		return fmt.Errorf("collectInterval: %w", err)
	}
	maxBackoff, err := durationOrDefault(fileConfig.MaxBackoff, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("maxBackoff: %w", err)
	}

	// app-server stderr can include provider diagnostics with account or local
	// path details, so normal Agent logs intentionally discard it.
	codex := collector.NewCodex(collector.CodexConfig{Command: fileConfig.CodexCommand, Stderr: io.Discard})
	claude := collector.NewClaude(collector.ClaudeConfig{Command: fileConfig.ClaudeCommand})
	cfg := agent.Config{
		AgentID:              fileConfig.AgentID,
		ServerURL:            fileConfig.ServerURL,
		Token:                token,
		HookAddr:             fileConfig.HookAddr,
		HookSecret:           hookSecret,
		Codex:                codex,
		Claude:               claude,
		PlanOverrides:        fileConfig.PlanOverrides,
		ReportInterval:       reportInterval,
		CollectInterval:      collectInterval,
		SequentialCollection: fileConfig.SequentialCollection,
		MaxBackoff:           maxBackoff,
		AllowInsecureHTTP:    fileConfig.AllowInsecureHTTP,
		Logger:               log.New(stderr, "agent: ", log.LstdFlags|log.Lmsgprefix),
	}

	if *once {
		instance, err := agent.New(cfg)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), agentOnceTimeout)
		defer cancel()
		instance.CollectOnce(ctx)
		if err := instance.ReportOnce(ctx); err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, "agent report accepted")
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.Run(ctx, cfg)
}

func loadAgentConfig(path string) (agentFileConfig, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return agentFileConfig{}, fmt.Errorf("read agent config: %w", err)
	}
	var cfg agentFileConfig
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return agentFileConfig{}, fmt.Errorf("decode agent config: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return agentFileConfig{}, errors.New("decode agent config: trailing JSON value")
		}
		return agentFileConfig{}, fmt.Errorf("decode agent config: trailing data: %w", err)
	}
	if cfg.AgentID == "" || cfg.ServerURL == "" || cfg.TokenFile == "" || cfg.HookSecretFile == "" {
		return agentFileConfig{}, errors.New("agent config requires agentId, serverUrl, tokenFile, and hookSecretFile")
	}
	return cfg, nil
}

func readSecretFile(baseDir, path string) (string, error) {
	if path == "" {
		return "", errors.New("secret file path is empty")
	}
	path = os.ExpandEnv(path)
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimLeft(path[1:], `/\`))
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(payload))
	if secret == "" {
		return "", errors.New("secret file is empty")
	}
	return secret, nil
}

func durationOrDefault(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errors.New("must be a positive Go duration such as 15s or 2m")
	}
	return duration, nil
}

func runStandalone(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("standalone", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listenAddress := fs.String("listen", envOr("QUOTA_MONITOR_LISTEN", "127.0.0.1:8787"), "HTTP listen address (keep loopback behind HTTPS proxy)")
	dbPath := fs.String("db", envOr("QUOTA_MONITOR_DB", "data/monitor.db"), "SQLite database path")
	displayTokenPath := fs.String("display-token-file", envOr("QUOTA_MONITOR_DISPLAY_TOKEN_FILE", "data/display.token"), "display token file; created on first run")
	hookAddress := fs.String("hook-addr", envOr("QUOTA_MONITOR_HOOK_ADDR", "127.0.0.1:47632"), "local Hooks listener; empty disables Hooks")
	hookSecretPath := fs.String("hook-secret-file", envOr("QUOTA_MONITOR_HOOK_SECRET_FILE", "data/hook-secret"), "local Hook secret file; created on first run")
	agentID := fs.String("agent-id", envOr("QUOTA_MONITOR_AGENT_ID", "cloud-standalone"), "stable local collector ID")
	codexCommand := fs.String("codex-command", envOr("QUOTA_MONITOR_CODEX_COMMAND", "codex"), "Codex CLI executable")
	claudeCommand := fs.String("claude-command", envOr("QUOTA_MONITOR_CLAUDE_COMMAND", "claude"), "Claude CLI executable")
	enableCodex := fs.Bool("codex", true, "collect Codex quota")
	enableClaude := fs.Bool("claude", true, "collect Claude quota (Claude Code officially requires 4 GB RAM)")
	codexPlan := fs.String("codex-plan", "", "optional verified Codex plan override, such as pro5 or pro20")
	claudePlan := fs.String("claude-plan", "", "optional verified Claude plan override, such as max5, max20, or none")
	collectInterval := fs.Duration("collect-interval", agent.DefaultCollectInterval, "provider quota polling interval")
	sequentialCollection := fs.Bool("sequential-collection", true, "probe Codex then Claude serially and release idle provider helpers")
	persistInterval := fs.Duration("persist-interval", agent.DefaultReportInterval, "SQLite task heartbeat interval")
	logFormat := fs.String("log-format", envOr("QUOTA_MONITOR_LOG_FORMAT", "text"), "json or text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("standalone accepts no positional arguments")
	}
	if !*enableCodex && !*enableClaude {
		return errors.New("standalone requires at least one of --codex or --claude")
	}
	if *collectInterval <= 0 || *persistInterval <= 0 {
		return errors.New("collect and persist intervals must be positive")
	}

	logger, err := newQuotaLogger(*logFormat, stderr)
	if err != nil {
		return err
	}
	if *dbPath != ":memory:" && !strings.HasPrefix(*dbPath, "file:") {
		if err := os.MkdirAll(filepath.Dir(*dbPath), 0o700); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
	}
	database, err := store.Open(context.Background(), *dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	resolvedDisplayToken, err := resolveUserPath(*displayTokenPath)
	if err != nil {
		return fmt.Errorf("display token path: %w", err)
	}
	createdDisplayToken, err := ensureDisplayToken(context.Background(), database, resolvedDisplayToken)
	if err != nil {
		return err
	}
	if createdDisplayToken {
		fmt.Fprintf(stdout, "created display token file %s; copy its qmon_ value to the ESP32\n", resolvedDisplayToken)
	}

	hookSecret := ""
	if strings.TrimSpace(*hookAddress) != "" {
		resolvedHookSecret, err := resolveUserPath(*hookSecretPath)
		if err != nil {
			return fmt.Errorf("hook secret path: %w", err)
		}
		hookSecret, err = loadOrCreateSecret(resolvedHookSecret)
		if err != nil {
			return fmt.Errorf("hook secret: %w", err)
		}
	}

	api, err := server.New(server.Config{Store: database, Logger: logger})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *listenAddress, err)
	}
	defer listener.Close()
	httpServer := &http.Server{
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	var codexCollector, claudeCollector agent.ProviderCollector
	if *enableCodex {
		codexCollector = collector.NewCodex(collector.CodexConfig{Command: *codexCommand, Stderr: io.Discard})
	} else {
		codexCollector = disabledProviderCollector{provider: model.ProviderCodex}
	}
	if *enableClaude {
		claudeCollector = collector.NewClaude(collector.ClaudeConfig{Command: *claudeCommand})
	} else {
		claudeCollector = disabledProviderCollector{provider: model.ProviderClaude}
	}
	overrides := make(map[model.ProviderName]string)
	if strings.TrimSpace(*codexPlan) != "" {
		overrides[model.ProviderCodex] = *codexPlan
	}
	if strings.TrimSpace(*claudePlan) != "" {
		overrides[model.ProviderClaude] = *claudePlan
	}
	plainLogger := log.New(stderr, "standalone: ", log.LstdFlags|log.Lmsgprefix)
	notifier := newProviderStatusNotifier(plainLogger)
	instance, err := agent.New(agent.Config{
		AgentID:              *agentID,
		Codex:                codexCollector,
		Claude:               claudeCollector,
		PlanOverrides:        overrides,
		HookAddr:             *hookAddress,
		HookSecret:           hookSecret,
		ReportInterval:       *persistInterval,
		CollectInterval:      *collectInterval,
		SequentialCollection: *sequentialCollection,
		Logger:               plainLogger,
		ProviderStatus:       notifier.Observe,
		ReportSink: agent.ReportSinkFunc(func(ctx context.Context, report model.AgentReport) error {
			now := time.Now().UTC()
			if err := server.ValidateAgentReport(report, now); err != nil {
				return fmt.Errorf("validate local report: %w", err)
			}
			return database.ReplaceAgentReport(ctx, report, now)
		}),
	})
	if err != nil {
		return err
	}
	defer instance.Close()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	runCtx, cancelRun := context.WithCancel(signalCtx)
	defer cancelRun()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("standalone API listening", "address", *listenAddress)
		serverErrors <- httpServer.Serve(listener)
	}()

	// Persist one complete state before starting the regular schedulers. This
	// avoids replacing a previous good snapshot with an empty startup report.
	initialCtx, cancelInitial := context.WithTimeout(runCtx, agentOnceTimeout)
	instance.CollectOnce(initialCtx)
	cancelInitial()
	persistCtx, cancelPersist := context.WithTimeout(runCtx, 10*time.Second)
	err = instance.ReportOnce(persistCtx)
	cancelPersist()
	if err != nil {
		cancelRun()
		_ = listener.Close()
		return fmt.Errorf("persist initial standalone snapshot: %w", err)
	}

	agentErrors := make(chan error, 1)
	go func() { agentErrors <- instance.RunAfterInitialCollection(runCtx) }()

	var runErr error
	agentFinished := false
	select {
	case <-signalCtx.Done():
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			runErr = fmt.Errorf("standalone API: %w", err)
		}
	case err := <-agentErrors:
		agentFinished = true
		if err != nil {
			runErr = fmt.Errorf("standalone collector: %w", err)
		}
	}

	cancelRun()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	cancelShutdown()
	if !agentFinished {
		select {
		case err := <-agentErrors:
			if runErr == nil && err != nil {
				runErr = fmt.Errorf("standalone collector: %w", err)
			}
		case <-time.After(10 * time.Second):
			if runErr == nil {
				runErr = errors.New("standalone collector did not stop within 10 seconds")
			}
		}
	}
	if runErr == nil && shutdownErr != nil && !errors.Is(shutdownErr, net.ErrClosed) {
		runErr = fmt.Errorf("shutdown standalone API: %w", shutdownErr)
	}
	if runErr == nil {
		_, _ = fmt.Fprintln(stdout, "standalone stopped")
	}
	return runErr
}

func newQuotaLogger(format string, writer io.Writer) (*slog.Logger, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return slog.New(slog.NewJSONHandler(writer, nil)), nil
	case "text":
		return slog.New(slog.NewTextHandler(writer, nil)), nil
	default:
		return nil, fmt.Errorf("unsupported log format %q", format)
	}
}

func resolveUserPath(value string) (string, error) {
	value = os.ExpandEnv(strings.TrimSpace(value))
	if value == "" {
		return "", errors.New("path is empty")
	}
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimLeft(value[1:], `/\`))
	}
	return filepath.Clean(value), nil
}

func ensureDisplayToken(ctx context.Context, database *store.Store, path string) (bool, error) {
	payload, err := os.ReadFile(path)
	if err == nil {
		raw := strings.TrimSpace(string(payload))
		if raw == "" {
			return false, errors.New("display token file is empty; restore it or choose a new file and database")
		}
		if _, err := database.AuthenticateToken(ctx, raw, store.ScopeDisplayRead); err != nil {
			return false, errors.New("display token file does not match the database; restore the matching pair")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return false, fmt.Errorf("secure display token file: %w", err)
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read display token file: %w", err)
	}
	created, err := database.CreateToken(ctx, store.CreateTokenRequest{Scope: store.ScopeDisplayRead, Label: "standalone display"})
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		_ = database.RevokeToken(context.Background(), created.ID)
		return false, fmt.Errorf("create display token directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = database.RevokeToken(context.Background(), created.ID)
		return false, fmt.Errorf("create display token file: %w", err)
	}
	_, writeErr := io.WriteString(file, created.RawToken+"\n")
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = database.RevokeToken(context.Background(), created.ID)
		return false, fmt.Errorf("write display token file: %w", writeErr)
	}
	return true, nil
}

type providerStatusNotifier struct {
	logger *log.Logger
	mu     sync.Mutex
	last   map[model.ProviderName]string
}

type disabledProviderCollector struct {
	provider model.ProviderName
}

func (c disabledProviderCollector) Collect(context.Context) (model.ProviderReport, error) {
	return model.ProviderReport{
		ObservedAt: time.Now().UTC(),
		AuthState:  "disabled",
		Plan:       "off",
		Source:     "standalone-disabled",
		Windows:    model.ProviderWindows{},
	}, nil
}

func (disabledProviderCollector) Close() error { return nil }

func newProviderStatusNotifier(logger *log.Logger) *providerStatusNotifier {
	return &providerStatusNotifier{logger: logger, last: make(map[model.ProviderName]string)}
}

func (n *providerStatusNotifier) Observe(provider model.ProviderName, report model.ProviderReport) {
	state := report.AuthState + "|" + report.ErrorCode
	n.mu.Lock()
	if n.last[provider] == state {
		n.mu.Unlock()
		return
	}
	previous := n.last[provider]
	n.last[provider] = state
	n.mu.Unlock()

	name := "Codex"
	loginCommand := "quota-monitor login codex"
	if provider == model.ProviderClaude {
		name = "Claude"
		loginCommand = "quota-monitor login claude"
	}
	loginRequired := report.AuthState == "unauthenticated" || report.ErrorCode == "not_authenticated"
	if loginRequired {
		n.logger.Printf("%s 未登录或登录已失效；请在同一服务器用户下运行: %s（登录后自动恢复，无需重启）", name, loginCommand)
		return
	}
	if report.AuthState == "authenticated" {
		if strings.HasPrefix(previous, "unauthenticated|") || strings.HasSuffix(previous, "|not_authenticated") {
			n.logger.Printf("%s 登录已恢复，额度采集已自动继续", name)
		} else {
			n.logger.Printf("%s 登录状态正常", name)
		}
		return
	}
	if report.ErrorCode != "" {
		n.logger.Printf("%s 额度采集暂时不可用；程序会自动重试", name)
	}
}

func runLogin(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return errors.New("login requires exactly one provider: codex or claude")
	}
	provider := strings.ToLower(strings.TrimSpace(args[0]))
	var executable string
	var commandArgs []string
	switch provider {
	case "codex":
		executable = "codex"
		commandArgs = []string{"login", "--device-auth"}
	case "claude":
		executable = "claude"
		commandArgs = []string{"auth", "login", "--claudeai"}
	default:
		return fmt.Errorf("unsupported login provider %q; use codex or claude", provider)
	}
	resolvedExecutable, err := resolveLoginExecutable(executable)
	if err != nil {
		return fmt.Errorf("find %s CLI: %w", provider, err)
	}
	command := exec.Command(resolvedExecutable, commandArgs...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = os.Environ()
	if err = command.Run(); err != nil {
		return fmt.Errorf("%s login failed: %w", provider, err)
	}
	_, err = fmt.Fprintf(stdout, "%s login completed; standalone will refresh automatically\n", provider)
	return err
}

// resolveLoginExecutable preserves the normal PATH lookup, but also handles
// the default per-user install location used by the official Codex and Claude
// installers on Unix. The fallback is an absolute path assembled without a
// shell, so executable names and HOME contents are never interpreted as shell
// input.
func resolveLoginExecutable(name string) (string, error) {
	return resolveLoginExecutableForOS(name, runtime.GOOS, exec.LookPath, os.UserHomeDir)
}

func resolveLoginExecutableForOS(
	name string,
	goos string,
	lookPath func(string) (string, error),
	userHomeDir func() (string, error),
) (string, error) {
	path, pathErr := lookPath(name)
	if pathErr == nil {
		return path, nil
	}
	if goos == "windows" {
		return "", pathErr
	}

	home, homeErr := userHomeDir()
	if homeErr != nil {
		return "", fmt.Errorf("not found in PATH and determine current user home: %w", homeErr)
	}
	candidate := filepath.Join(home, ".local", "bin", name)
	info, statErr := os.Stat(candidate)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("not found in PATH or %s: %w", candidate, pathErr)
		}
		return "", fmt.Errorf("inspect fallback executable %s: %w", candidate, statErr)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("fallback executable %s is not a regular file", candidate)
	}
	return candidate, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `quota-monitor - Codex and Claude quota monitor

Usage:
  quota-monitor standalone [options]
  quota-monitor server [options]
  quota-monitor agent [options]
  quota-monitor hooks install|uninstall [options]
  quota-monitor service generate [options]
  quota-monitor token create|list|revoke [options]
  quota-monitor doctor [options]
  quota-monitor login codex|claude
  quota-monitor version

Run a command with --help for its options.`)
}

func runService(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "generate" {
		return errors.New("service requires generate")
	}
	fs := flag.NewFlagSet("service generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	targetDefault := "systemd"
	if runtime.GOOS == "windows" {
		targetDefault = "windows"
	}
	target := fs.String("target", targetDefault, "windows or systemd")
	mode := fs.String("mode", "agent", "agent or standalone")
	executableDefault, _ := os.Executable()
	executable := fs.String("executable", executableDefault, "absolute quota-monitor executable path")
	configPath := fs.String("config", "", "absolute agent config path (agent mode only)")
	workingDirectory := fs.String("working-directory", "", "service working directory")
	windowsUserID := fs.String("windows-user-id", "", "optional Windows user SID")
	outputPath := fs.String("output", "", "write file instead of stdout")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *executable == "" {
		return errors.New("service generate accepts no positional arguments and requires --executable")
	}
	if strings.EqualFold(*mode, "agent") && *configPath == "" {
		return errors.New("service generate --mode agent requires --config")
	}
	resolvedExecutable, err := resolveServicePath(*executable)
	if err != nil {
		return err
	}
	resolvedConfig := ""
	if *configPath != "" {
		resolvedConfig, err = resolveServicePath(*configPath)
		if err != nil {
			return err
		}
	}
	cfg := agent.ServiceFileConfig{
		Executable: resolvedExecutable, ConfigPath: resolvedConfig,
		WorkingDirectory: *workingDirectory, WindowsUserID: *windowsUserID, Mode: *mode,
	}
	var content string
	switch strings.ToLower(*target) {
	case "windows":
		content, err = agent.GenerateWindowsTaskXML(cfg)
	case "systemd", "linux":
		content, err = agent.GenerateSystemdUnit(cfg)
	default:
		return fmt.Errorf("unsupported service target %q", *target)
	}
	if err != nil {
		return err
	}
	if *outputPath == "" {
		_, err = io.WriteString(stdout, content)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*outputPath, []byte(content), 0o600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %s\n", *outputPath)
	return err
}

// resolveServicePath preserves already-absolute paths for either target OS.
// filepath.Abs alone would turn /home/... into \home\... on Windows and
// C:\... into a host-relative path on Linux when generating cross-platform
// service files.
func resolveServicePath(value string) (string, error) {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) ||
		(len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')) {
		return value, nil
	}
	return filepath.Abs(value)
}

func runServer(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", envOr("QUOTA_MONITOR_LISTEN", "127.0.0.1:8787"), "HTTP listen address")
	dbPath := fs.String("db", envOr("QUOTA_MONITOR_DB", "data/monitor.db"), "SQLite database path")
	logFormat := fs.String("log-format", envOr("QUOTA_MONITOR_LOG_FORMAT", "json"), "json or text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("server does not accept positional arguments")
	}

	if *dbPath != ":memory:" && !strings.HasPrefix(*dbPath, "file:") {
		if err := os.MkdirAll(filepath.Dir(*dbPath), 0o700); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
	}
	ctx := context.Background()
	database, err := store.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	var handler slog.Handler
	switch strings.ToLower(*logFormat) {
	case "json":
		handler = slog.NewJSONHandler(stderr, nil)
	case "text":
		handler = slog.NewTextHandler(stderr, nil)
	default:
		return fmt.Errorf("unsupported log format %q", *logFormat)
	}
	logger := slog.New(handler)
	api, err := server.New(server.Config{Store: database, Logger: logger})
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		// Do not log filesystem paths; database location is operational config,
		// not useful request telemetry and may reveal a user/home layout.
		logger.Info("server listening", "address", *listen)
		errCh <- httpServer.ListenAndServe()
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		_, _ = fmt.Fprintln(stdout, "server stopped")
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func runToken(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("token requires create, list, or revoke")
	}
	switch args[0] {
	case "create":
		return runTokenCreate(args[1:], stdout, stderr)
	case "list":
		return runTokenList(args[1:], stdout, stderr)
	case "revoke":
		return runTokenRevoke(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown token command %q", args[0])
	}
}

func tokenDBFlag(fs *flag.FlagSet) *string {
	return fs.String("db", envOr("QUOTA_MONITOR_DB", "data/monitor.db"), "SQLite database path")
}

func openTokenStore(ctx context.Context, path string) (*store.Store, error) {
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	return store.Open(ctx, path)
}

func runTokenCreate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := tokenDBFlag(fs)
	scope := fs.String("scope", "", "agent:write or display:read")
	agentID := fs.String("agent-id", "", "required for agent:write")
	label := fs.String("label", "", "operator-facing label")
	jsonOutput := fs.Bool("json", false, "print one-time token as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *scope == "" {
		return errors.New("token create requires --scope and no positional arguments")
	}
	database, err := openTokenStore(context.Background(), *dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	created, err := database.CreateToken(context.Background(), store.CreateTokenRequest{
		Scope: store.TokenScope(*scope), AgentID: *agentID, Label: *label,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(stdout).Encode(created)
	}
	fmt.Fprintf(stdout, "Token ID: %s\nScope: %s\n", created.ID, created.Scope)
	if created.AgentID != "" {
		fmt.Fprintf(stdout, "Agent ID: %s\n", created.AgentID)
	}
	fmt.Fprintf(stdout, "Bearer token (shown once): %s\n", created.RawToken)
	return nil
}

func runTokenList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("token list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := tokenDBFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("token list accepts no positional arguments")
	}
	database, err := openTokenStore(context.Background(), *dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	records, err := database.ListTokens(context.Background())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(records)
}

func runTokenRevoke(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := tokenDBFlag(fs)
	id := fs.String("id", "", "token ID returned by create/list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *id == "" {
		return errors.New("token revoke requires --id")
	}
	database, err := openTokenStore(context.Background(), *dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.RevokeToken(context.Background(), *id); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "revoked %s\n", *id)
	return err
}

func runDoctor(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	healthURL := fs.String("health-url", "", "server health URL, for example https://monitor.example.com/healthz")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("doctor accepts no positional arguments")
	}
	if *healthURL == "" {
		return errors.New("doctor currently requires --health-url")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequest(http.MethodGet, *healthURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health request returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	_, err = fmt.Fprintf(stdout, "server healthy: %s\n", strings.TrimSpace(string(body)))
	return err
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
