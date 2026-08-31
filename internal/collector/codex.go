// Package collector reads quota state from the provider-owned local CLIs.
// It deliberately returns only normalized quota data; account identifiers and
// credentials never leave the provider process.
package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codex-claude-monitor/internal/model"
)

const (
	FiveHourMinutes = int64(300)
	SevenDayMinutes = int64(10080)
)

// CodexConfig controls the local app-server child process.
type CodexConfig struct {
	Command string
	Args    []string
	Env     []string
	Timeout time.Duration
	Stderr  io.Writer
}

// CodexCollector owns one long-lived codex app-server JSONL connection. A
// failed or timed-out request tears the process down; the next Collect starts a
// fresh connection and performs the initialization handshake again.
type CodexCollector struct {
	cfg CodexConfig

	collectMu sync.Mutex
	startMu   sync.Mutex
	writeMu   sync.Mutex
	mu        sync.Mutex
	proc      *codexProcess
	pending   map[int64]chan rpcResponse
	nextID    atomic.Int64
}

type codexProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	exited chan struct{}
	once   sync.Once
}

type rpcRequest struct {
	ID     int64  `json:"id,omitempty"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     int64           `json:"id"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("app-server error %d: %s", e.Code, e.Message)
}

func NewCodex(cfg CodexConfig) *CodexCollector {
	if cfg.Command == "" {
		cfg.Command = "codex"
	}
	if len(cfg.Args) == 0 {
		cfg.Args = []string{"app-server", "--stdio"}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	return &CodexCollector{cfg: cfg, pending: make(map[int64]chan rpcResponse)}
}

// Collect queries both account/read and account/rateLimits/read. Collections
// are serialized so an expired access token can be refreshed without two
// callers racing to rotate the same provider-owned refresh token.
func (c *CodexCollector) Collect(ctx context.Context) (model.ProviderReport, error) {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	report, err := c.collectAttempt(ctx, false)
	if err == nil {
		return report, nil
	}

	refreshable := isCodexRefreshableAuthError(err)
	if resetErr := c.resetCurrentProcessAndWait(ctx); resetErr != nil {
		return report, resetErr
	}
	if !refreshable {
		return report, err
	}

	// account/read normally lets the Codex CLI manage its own token lifecycle.
	// Force a refresh only after app-server has explicitly rejected the access
	// token, then retry the complete account + rate-limit transaction once.
	report, err = c.collectAttempt(ctx, true)
	if err == nil {
		return report, nil
	}
	retryWasAuthFailure := isCodexRefreshableAuthError(err)
	if resetErr := c.resetCurrentProcessAndWait(ctx); resetErr != nil {
		return report, resetErr
	}
	if retryWasAuthFailure {
		// Do not retain old quota values indefinitely when the refresh token can
		// no longer recover the account. A nil error lets the agent publish the
		// login-required state instead of preserving the stale snapshot.
		return codexLoginRequiredReport(), nil
	}
	return report, err
}

func (c *CodexCollector) collectAttempt(ctx context.Context, refreshToken bool) (model.ProviderReport, error) {
	if err := c.ensureStarted(ctx); err != nil {
		return unavailableReport(model.ProviderCodex, "codex-app-server", "start_failed"), err
	}

	var account accountResponse
	if err := c.call(ctx, "account/read", map[string]any{"refreshToken": refreshToken}, &account); err != nil {
		return unavailableReport(model.ProviderCodex, "codex-app-server", "account_read_failed"), err
	}
	if account.Account == nil && account.RequiresOpenAIAuth {
		// A separately invoked `codex login --device-auth` updates the provider's
		// credential store. Recreate app-server on the next poll so it cannot keep
		// an unauthenticated in-memory session after that external login succeeds.
		if err := c.resetCurrentProcessAndWait(ctx); err != nil {
			return unavailableReport(model.ProviderCodex, "codex-app-server", "account_read_failed"), err
		}
		return codexLoginRequiredReport(), nil
	}
	var limits rateLimitsResponse
	if err := c.call(ctx, "account/rateLimits/read", nil, &limits); err != nil {
		return unavailableReport(model.ProviderCodex, "codex-app-server", "rate_limits_read_failed"), err
	}
	return normalizeCodex(account, limits, time.Now()), nil
}

func codexLoginRequiredReport() model.ProviderReport {
	return model.ProviderReport{
		ObservedAt: time.Now().UTC(),
		AuthState:  "unauthenticated",
		Source:     "codex-app-server",
		Windows:    model.ProviderWindows{},
		ErrorCode:  "not_authenticated",
	}
}

func isCodexRefreshableAuthError(err error) bool {
	var appErr *rpcError
	if !errors.As(err, &appErr) {
		return false
	}

	message := strings.ToLower(appErr.Message)
	if strings.Contains(message, "token_expired") ||
		strings.Contains(message, "invalid_grant") ||
		strings.Contains(message, "refresh token is expired") ||
		(strings.Contains(message, "401 unauthorized") && strings.Contains(message, "expired")) {
		return true
	}

	var data any
	if len(appErr.Data) == 0 || json.Unmarshal(appErr.Data, &data) != nil {
		return false
	}
	return codexAuthErrorDataIsRefreshable(data)
}

func codexAuthErrorDataIsRefreshable(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			compactKey := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			switch compactKey {
			case "code", "errorcode", "type":
				if text, ok := child.(string); ok {
					switch strings.ToLower(strings.TrimSpace(text)) {
					case "token_expired", "refresh_token_expired", "invalid_token", "invalid_grant":
						return true
					}
				}
			case "httpstatus", "status", "statuscode":
				if number, ok := child.(float64); ok && number == 401 {
					return true
				}
			}
			if codexAuthErrorDataIsRefreshable(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if codexAuthErrorDataIsRefreshable(child) {
				return true
			}
		}
	case string:
		text := strings.ToLower(typed)
		return strings.Contains(text, "token_expired") || strings.Contains(text, "invalid_grant")
	}
	return false
}

func (c *CodexCollector) ensureStarted(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	current := c.proc
	c.mu.Unlock()
	if current != nil {
		select {
		case <-current.done:
		default:
			return nil
		}
	}

	cmd := exec.Command(c.cfg.Command, c.cfg.Args...)
	cmd.Env = append(os.Environ(), c.cfg.Env...)
	cmd.Stderr = c.cfg.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open codex stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open codex stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start %s: %w", c.cfg.Command, err)
	}
	p := &codexProcess{cmd: cmd, stdin: stdin, done: make(chan struct{}), exited: make(chan struct{})}
	c.mu.Lock()
	c.proc = p
	c.mu.Unlock()
	go c.readLoop(p, stdout)

	var ignored json.RawMessage
	if err := c.callOnProcess(ctx, p, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "quota-monitor", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": false},
	}, &ignored); err != nil {
		c.stopProcess(p, err)
		return fmt.Errorf("initialize codex app-server: %w", err)
	}
	if err := c.notify(p, "initialized", map[string]any{}); err != nil {
		c.stopProcess(p, err)
		return fmt.Errorf("notify codex initialized: %w", err)
	}
	return nil
}

func (c *CodexCollector) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		return errors.New("codex app-server is not running")
	}
	return c.callOnProcess(ctx, p, method, params, out)
}

func (c *CodexCollector) callOnProcess(ctx context.Context, p *codexProcess, method string, params any, out any) error {
	requestCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	id := c.nextID.Add(1)
	responseCh := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = responseCh
	c.mu.Unlock()

	if err := c.writeJSON(p, rpcRequest{ID: id, Method: method, Params: params}); err != nil {
		c.removePending(id)
		c.stopProcess(p, err)
		return err
	}

	select {
	case response := <-responseCh:
		if response.Error != nil {
			return response.Error
		}
		if out == nil || len(response.Result) == 0 || string(response.Result) == "null" {
			return nil
		}
		if raw, ok := out.(*json.RawMessage); ok {
			*raw = append((*raw)[:0], response.Result...)
			return nil
		}
		if err := json.Unmarshal(response.Result, out); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	case <-requestCtx.Done():
		c.removePending(id)
		c.stopProcess(p, requestCtx.Err())
		return fmt.Errorf("%s: %w", method, requestCtx.Err())
	case <-p.done:
		c.removePending(id)
		if err := requestCtx.Err(); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		return fmt.Errorf("%s: app-server disconnected", method)
	}
}

func (c *CodexCollector) notify(p *codexProcess, method string, params any) error {
	return c.writeJSON(p, struct {
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{Method: method, Params: params})
}

func (c *CodexCollector) writeJSON(p *codexProcess, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	current := c.proc
	c.mu.Unlock()
	if current != p {
		return errors.New("codex app-server process changed")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if _, err := p.stdin.Write(payload); err != nil {
		return fmt.Errorf("write app-server request: %w", err)
	}
	return nil
}

func (c *CodexCollector) readLoop(p *codexProcess, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var response rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.ID == 0 || response.Method != "" {
			continue // notifications and forward-compatible messages are ignored.
		}
		c.mu.Lock()
		ch := c.pending[response.ID]
		delete(c.pending, response.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- response
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.stopProcess(p, err)
}

func (c *CodexCollector) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *CodexCollector) stopProcess(p *codexProcess, _ error) {
	p.once.Do(func() {
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		close(p.done)
		go func() {
			_ = p.cmd.Wait()
			close(p.exited)
		}()
		c.mu.Lock()
		if c.proc == p {
			c.proc = nil
		}
		for id := range c.pending {
			delete(c.pending, id)
		}
		c.mu.Unlock()
	})
}

func (c *CodexCollector) Close() error {
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p != nil {
		c.stopProcess(p, errors.New("collector closed"))
	}
	return nil
}

// ReleaseIdleResources stops the reusable app-server child without disabling
// the collector. A later Collect call starts a fresh child and repeats the
// handshake. Sequential standalone collection uses this to avoid retaining
// Codex CLI memory while Claude's one-shot probe runs.
func (c *CodexCollector) ReleaseIdleResources() {
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		return
	}
	c.stopProcess(p, errors.New("release idle collector resources"))
	// Process.Kill is asynchronous on Unix. Wait for reaping so the Claude
	// probe cannot overlap the app-server's resident memory.
	<-p.exited
}

func (c *CodexCollector) resetCurrentProcessAndWait(ctx context.Context) error {
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		return nil
	}
	c.stopProcess(p, errors.New("recreate codex app-server"))
	select {
	case <-p.exited:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for codex app-server exit: %w", ctx.Err())
	}
}

type accountResponse struct {
	Account *struct {
		Type     string `json:"type"`
		PlanType string `json:"planType"`
	} `json:"account"`
	RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
}

type rateLimitWindow struct {
	UsedPercent       float64 `json:"usedPercent"`
	WindowDurationMin *int64  `json:"windowDurationMins"`
	ResetsAt          *int64  `json:"resetsAt"`
}

type rateLimitSnapshot struct {
	LimitID   *string          `json:"limitId"`
	PlanType  *string          `json:"planType"`
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
}

type rateLimitsResponse struct {
	RateLimits          rateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
}

func normalizeCodex(account accountResponse, limits rateLimitsResponse, observedAt time.Time) model.ProviderReport {
	report := model.ProviderReport{
		ObservedAt: observedAt.UTC(),
		AuthState:  "unauthenticated",
		Source:     "codex-app-server",
		Windows:    model.ProviderWindows{},
	}
	if account.Account != nil {
		report.AuthState = "authenticated"
		report.Plan = account.Account.PlanType
	} else if !account.RequiresOpenAIAuth {
		report.AuthState = "not-required"
	}

	report.Plan = selectCodexPlan(report.Plan, stableCodexPlan(limits))

	var fiveHour, sevenDay codexWindowChoice
	considerCodexSnapshot(&fiveHour, &sevenDay, limits.RateLimits, true)
	for _, snapshot := range limits.RateLimitsByLimitID {
		considerCodexSnapshot(&fiveHour, &sevenDay, snapshot, false)
	}
	if fiveHour.set {
		window := fiveHour.window
		report.Windows.FiveHour = &window
	}
	if sevenDay.set {
		window := sevenDay.window
		report.Windows.SevenDay = &window
	}
	return report
}

// codexWindowChoice makes duplicate-duration resolution independent of map
// iteration order and opaque limit IDs. The top-level rateLimits snapshot is
// the canonical aggregate when present. Within the same source class, the
// most restrictive window wins; reset time is a deterministic tie-breaker.
type codexWindowChoice struct {
	window    model.LimitWindow
	canonical bool
	set       bool
}

func considerCodexSnapshot(fiveHour, sevenDay *codexWindowChoice, snapshot rateLimitSnapshot, canonical bool) {
	for _, window := range []*rateLimitWindow{snapshot.Primary, snapshot.Secondary} {
		if window == nil || window.WindowDurationMin == nil || window.ResetsAt == nil {
			continue
		}
		var destination *codexWindowChoice
		switch *window.WindowDurationMin {
		case FiveHourMinutes:
			destination = fiveHour
		case SevenDayMinutes:
			destination = sevenDay
		default:
			continue
		}
		candidate := codexWindowChoice{
			window:    normalizeWindow(window.UsedPercent, time.Unix(*window.ResetsAt, 0)),
			canonical: canonical,
			set:       true,
		}
		if preferCodexWindow(candidate, *destination) {
			*destination = candidate
		}
	}
}

func preferCodexWindow(candidate, current codexWindowChoice) bool {
	if !current.set {
		return true
	}
	if candidate.canonical != current.canonical {
		return candidate.canonical
	}
	if candidate.window.UsedPercent != current.window.UsedPercent {
		return candidate.window.UsedPercent > current.window.UsedPercent
	}
	return candidate.window.ResetsAt.Before(*current.window.ResetsAt)
}

func stableCodexPlan(limits rateLimitsResponse) string {
	if limits.RateLimits.PlanType != nil && *limits.RateLimits.PlanType != "" {
		return *limits.RateLimits.PlanType
	}
	var plan string
	for _, snapshot := range limits.RateLimitsByLimitID {
		if snapshot.PlanType == nil || *snapshot.PlanType == "" {
			continue
		}
		if plan == "" || *snapshot.PlanType < plan {
			plan = *snapshot.PlanType
		}
	}
	return plan
}

// selectCodexPlan translates app-server's internal Pro tier names to the
// product labels used by the display. In the current protocol, "prolite" is
// Pro 5x and "pro" is Pro 20x. Rate-limit metadata wins only within that Pro
// family because it describes the entitlement backing the returned windows;
// an unrelated conflicting account plan remains authoritative.
func selectCodexPlan(accountPlan, rateLimitsPlan string) string {
	accountPlan = strings.TrimSpace(accountPlan)
	rateLimitsPlan = strings.TrimSpace(rateLimitsPlan)
	if accountPlan == "" {
		return canonicalCodexPlan(rateLimitsPlan)
	}
	if rateLimitsPlan != "" && isCodexProPlan(accountPlan) && isCodexProPlan(rateLimitsPlan) {
		return canonicalCodexPlan(rateLimitsPlan)
	}
	return canonicalCodexPlan(accountPlan)
}

func isCodexProPlan(plan string) bool {
	switch compactPlan(plan) {
	case "pro", "chatgptpro", "prolite", "pro5", "pro5x", "pro20", "pro20x":
		return true
	default:
		return false
	}
}

func canonicalCodexPlan(plan string) string {
	switch compactPlan(plan) {
	case "prolite", "pro5", "pro5x":
		return "pro5"
	case "pro", "chatgptpro", "pro20", "pro20x":
		return "pro20"
	default:
		return strings.TrimSpace(plan)
	}
}

func compactPlan(plan string) string {
	var compact strings.Builder
	compact.Grow(len(plan))
	for _, char := range strings.ToLower(strings.TrimSpace(plan)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			compact.WriteRune(char)
		}
	}
	return compact.String()
}

func normalizeWindow(used float64, resetsAt time.Time) model.LimitWindow {
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	resetUTC := resetsAt.UTC()
	return model.LimitWindow{
		UsedPercent:      used,
		RemainingPercent: 100 - used,
		ResetsAt:         &resetUTC,
	}
}

func unavailableReport(provider model.ProviderName, source, code string) model.ProviderReport {
	return model.ProviderReport{
		ObservedAt: time.Now().UTC(),
		AuthState:  "unknown",
		Source:     source,
		ErrorCode:  code,
		Windows:    model.ProviderWindows{},
	}
}
