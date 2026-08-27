package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"codex-claude-monitor/internal/model"
)

const ProbeEnvironment = "QUOTA_MONITOR_PROBE=1"

// CommandRunner makes provider command execution fixture-testable without
// putting shell parsing in the collector.
type CommandRunner interface {
	Run(ctx context.Context, command string, args []string, env []string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		// Provider stderr may contain account identifiers, local paths or login
		// URLs. Keep stdout for the documented JSON-on-nonzero auth case, but
		// expose only the process status to logs and callers.
		return stdout.Bytes(), fmt.Errorf("provider CLI command failed: %w", err)
	}
	return stdout.Bytes(), nil
}

type ClaudeConfig struct {
	Command string
	Runner  CommandRunner
	Timeout time.Duration
}

type ClaudeCollector struct {
	cfg ClaudeConfig
}

func NewClaude(cfg ClaudeConfig) *ClaudeCollector {
	if cfg.Command == "" {
		cfg.Command = "claude"
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecRunner{}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &ClaudeCollector{cfg: cfg}
}

// Collect first checks auth and then invokes the built-in /usage slash command.
// The slash command is the sole prompt argument; no model prompt or fallback
// prompt is ever constructed by this package.
func (c *ClaudeCollector) Collect(ctx context.Context) (model.ProviderReport, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	authOutput, err := c.cfg.Runner.Run(requestCtx, c.cfg.Command, []string{"auth", "status", "--json"}, []string{"NO_COLOR=1", ProbeEnvironment})
	auth, parseErr := ParseClaudeAuthStatus(authOutput)
	if parseErr != nil {
		if err != nil {
			return unavailableReport(model.ProviderClaude, "claude-auth-status", "auth_status_failed"), err
		}
		return unavailableReport(model.ProviderClaude, "claude-auth-status", "auth_status_invalid"), parseErr
	}
	if auth.AuthState != "authenticated" {
		auth.ErrorCode = "not_authenticated"
		return auth, nil
	}
	if err != nil {
		return unavailableReport(model.ProviderClaude, "claude-auth-status", "auth_status_failed"), err
	}

	usageOutput, err := c.cfg.Runner.Run(requestCtx, c.cfg.Command, []string{
		"-p", "/usage",
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
	}, []string{"NO_COLOR=1", ProbeEnvironment, "CLAUDE_CODE_QUOTA_MONITOR_PROBE=1"})
	if err != nil {
		auth.ErrorCode = "usage_command_failed"
		auth.Source = "claude-auth-status"
		return auth, err
	}
	usage, err := ParseClaudeUsage(usageOutput, time.Now())
	if err != nil {
		auth.ErrorCode = "usage_parse_failed"
		return auth, err
	}
	usage.AuthState = auth.AuthState
	if usage.Plan == "" {
		usage.Plan = auth.Plan
	}
	return usage, nil
}

func (c *ClaudeCollector) Close() error { return nil }

func CollectClaudeAuth(ctx context.Context, runner CommandRunner, command string, timeout time.Duration) (model.ProviderReport, error) {
	collector := NewClaude(ClaudeConfig{Command: command, Runner: runner, Timeout: timeout})
	requestCtx, cancel := context.WithTimeout(ctx, collector.cfg.Timeout)
	defer cancel()
	payload, commandErr := collector.cfg.Runner.Run(requestCtx, collector.cfg.Command, []string{"auth", "status", "--json"}, []string{"NO_COLOR=1", ProbeEnvironment})
	report, parseErr := ParseClaudeAuthStatus(payload)
	if parseErr == nil {
		if report.AuthState != "authenticated" {
			report.ErrorCode = "not_authenticated"
			return report, nil
		}
		if commandErr == nil {
			return report, nil
		}
	}
	if commandErr != nil {
		return model.ProviderReport{}, commandErr
	}
	return model.ProviderReport{}, parseErr
}

func CollectClaudeUsage(ctx context.Context, runner CommandRunner, command string, timeout time.Duration) (model.ProviderReport, error) {
	collector := NewClaude(ClaudeConfig{Command: command, Runner: runner, Timeout: timeout})
	requestCtx, cancel := context.WithTimeout(ctx, collector.cfg.Timeout)
	defer cancel()
	payload, err := collector.cfg.Runner.Run(requestCtx, collector.cfg.Command, []string{
		"-p", "/usage", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
	}, []string{"NO_COLOR=1", ProbeEnvironment, "CLAUDE_CODE_QUOTA_MONITOR_PROBE=1"})
	if err != nil {
		return model.ProviderReport{}, err
	}
	return ParseClaudeUsage(payload, time.Now())
}

// ParseClaudeAuthStatus accepts both current Claude Code JSON and older field
// spellings seen in fixtures. It intentionally discards account identifiers.
func ParseClaudeAuthStatus(payload []byte) (model.ProviderReport, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return model.ProviderReport{}, fmt.Errorf("decode claude auth status: %w", err)
	}
	loggedIn, _ := boolValue(raw, "loggedIn", "logged_in", "authenticated")
	plan, _ := stringValue(raw, "subscriptionType", "subscription_type", "plan", "planType")
	if plan == "" {
		plan, _ = stringValueNested(raw, []string{"account", "subscriptionType"}, []string{"account", "plan"})
	}
	state := "unauthenticated"
	if loggedIn {
		state = "authenticated"
	}
	return model.ProviderReport{
		ObservedAt: time.Now().UTC(),
		AuthState:  state,
		Plan:       plan,
		Source:     "claude-auth-status",
		Windows:    model.ProviderWindows{},
	}, nil
}

// ParseClaudeStatusLine parses the structured rate_limits object documented by
// Claude Code. Windows are independently optional because Claude may omit one
// until the first API response or when the account does not have that window.
func ParseClaudeStatusLine(payload []byte, observedAt time.Time) (model.ProviderReport, error) {
	return parseClaudeRateLimits(payload, observedAt, "claude-statusline")
}

// ParseClaudeUsage scans every stream-json line and nested object for a
// rate_limits structure. This handles the current stream event envelope without
// binding the monitor to unrelated Claude message fields.
func ParseClaudeUsage(payload []byte, observedAt time.Time) (model.ProviderReport, error) {
	if report, err := parseClaudeRateLimits(payload, observedAt, "claude-usage"); err == nil {
		return report, nil
	}
	var quotaText []string
	for _, line := range bytes.Split(payload, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var value any
		if json.Unmarshal(line, &value) != nil {
			continue
		}
		if limits := findRateLimits(value); limits != nil {
			encoded, _ := json.Marshal(map[string]any{"rate_limits": limits})
			return parseClaudeRateLimits(encoded, observedAt, "claude-usage")
		}
		collectQuotaText(value, &quotaText)
	}
	if len(quotaText) > 0 {
		if report, err := parseClaudeUsageText(strings.Join(quotaText, "\n"), observedAt); err == nil {
			return report, nil
		}
	}
	return parseClaudeUsageText(stripANSI(string(payload)), observedAt)
}

func collectQuotaText(value any, output *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok && (key == "result" || key == "text") {
				lower := strings.ToLower(text)
				if strings.Contains(lower, "% used") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "current session") {
					*output = append(*output, text)
				}
			}
			collectQuotaText(child, output)
		}
	case []any:
		for _, child := range typed {
			collectQuotaText(child, output)
		}
	}
}

func parseClaudeRateLimits(payload []byte, observedAt time.Time, source string) (model.ProviderReport, error) {
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return model.ProviderReport{}, err
	}
	limits := findRateLimits(root)
	if limits == nil {
		return model.ProviderReport{}, errors.New("rate_limits object not found")
	}
	report := model.ProviderReport{
		ObservedAt: observedAt.UTC(),
		AuthState:  "authenticated",
		Source:     source,
		Windows:    model.ProviderWindows{},
	}
	if value := objectValue(limits, "five_hour", "fiveHour", "5h"); value != nil {
		window, err := parseClaudeWindow(value)
		if err != nil {
			return model.ProviderReport{}, fmt.Errorf("parse five-hour limit: %w", err)
		}
		report.Windows.FiveHour = &window
	}
	if value := objectValue(limits, "seven_day", "sevenDay", "7d"); value != nil {
		window, err := parseClaudeWindow(value)
		if err != nil {
			return model.ProviderReport{}, fmt.Errorf("parse seven-day limit: %w", err)
		}
		report.Windows.SevenDay = &window
	}
	if report.Windows.FiveHour == nil && report.Windows.SevenDay == nil {
		return model.ProviderReport{}, errors.New("no supported Claude rate-limit windows")
	}
	return report, nil
}

func findRateLimits(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"rate_limits", "rateLimits"} {
			if child, ok := typed[key].(map[string]any); ok {
				return child
			}
		}
		for _, child := range typed {
			if found := findRateLimits(child); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findRateLimits(child); found != nil {
				return found
			}
		}
	}
	return nil
}

func parseClaudeWindow(value any) (model.LimitWindow, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return model.LimitWindow{}, errors.New("window is not an object")
	}
	used, ok := numberValue(object, "used_percentage", "usedPercent", "used_percent", "utilization")
	if !ok {
		return model.LimitWindow{}, errors.New("used percentage is missing")
	}
	resetRaw := objectValue(object, "resets_at", "resetsAt", "reset_at", "resetAt")
	reset, err := parseTimestamp(resetRaw)
	if err != nil {
		return model.LimitWindow{}, fmt.Errorf("reset timestamp: %w", err)
	}
	return normalizeWindow(used, reset), nil
}

func parseTimestamp(value any) (time.Time, error) {
	switch typed := value.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UTC(), nil
			}
		}
		if number, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return unixTimestamp(number), nil
		}
	case json.Number:
		if number, err := typed.Int64(); err == nil {
			return unixTimestamp(number), nil
		}
	case float64:
		return unixTimestamp(int64(typed)), nil
	case int64:
		return unixTimestamp(typed), nil
	}
	return time.Time{}, errors.New("missing or invalid timestamp")
}

func unixTimestamp(value int64) time.Time {
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

var (
	ansiPattern          = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	percentPattern       = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*(?:used)?`)
	timestampPattern     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})`)
	resetPhrasePattern   = regexp.MustCompile(`(?i)\bresets?\s+(.+?)\s*$`)
	relativeResetPattern = regexp.MustCompile(`(?i)^in\s+(.+?)\s*$`)
	relativeResetPart    = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)`)
	absoluteResetPattern = regexp.MustCompile(`(?i)^([a-z]+)\s+(\d{1,2})(?:,\s*(\d{4}))?(?:,\s*|\s+at\s+)(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(?:\s*\(([^)]+)\))?$`)
	currentWeekHeading   = regexp.MustCompile(`(?i)^\s*current\s+week(?:\s*\(([^)]*)\))?(?:\s*:|\s*$)`)
	modelOnlyHeading     = regexp.MustCompile(`(?i)^\s*[^:]{1,80}\bonly(?:\s*:|\s*$)`)
	fiveHourHeading      = regexp.MustCompile(`(?i)^\s*(?:current\s+session|5h|5\s*hours?|5-hour)(?:\s+(?:limit|usage))?(?:\s*:|\s*$)`)
	sevenDayHeading      = regexp.MustCompile(`(?i)^\s*(?:all\s+models|7d|7\s*days?|7-day|weekly\s+limits?)(?:\s*:|\s*$)`)
)

const (
	claudeResetPastTolerance = 24 * time.Hour
	claudeResetMaxHorizon    = 8 * 24 * time.Hour
)

func stripANSI(value string) string { return ansiPattern.ReplaceAllString(value, "") }

func parseClaudeUsageText(text string, observedAt time.Time) (model.ProviderReport, error) {
	report := model.ProviderReport{ObservedAt: observedAt.UTC(), AuthState: "authenticated", Source: "claude-usage", Windows: model.ProviderWindows{}}
	type pendingWindow struct {
		kind  string
		used  *float64
		reset *time.Time
	}
	pending := pendingWindow{}
	commit := func() {
		if pending.used == nil || pending.reset == nil {
			return
		}
		window := normalizeWindow(*pending.used, *pending.reset)
		switch pending.kind {
		case "five":
			report.Windows.FiveHour = &window
		case "seven":
			report.Windows.SevenDay = &window
		}
	}
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		if kind, isHeading := classifyClaudeUsageHeading(line); isHeading {
			commit()
			pending = pendingWindow{kind: kind}
		}
		if pending.kind == "" {
			continue
		}
		percentage := percentPattern.FindStringSubmatch(line)
		if len(percentage) >= 2 {
			used, _ := strconv.ParseFloat(percentage[1], 64)
			if strings.Contains(lower, "remaining") && !strings.Contains(lower, "used") {
				used = 100 - used
			}
			pending.used = &used
		}
		if reset, ok := parseClaudeUsageReset(line, observedAt); ok {
			pending.reset = &reset
		}
	}
	commit()
	if report.Windows.FiveHour == nil && report.Windows.SevenDay == nil {
		return model.ProviderReport{}, errors.New("Claude /usage did not contain structured quota windows")
	}
	return report, nil
}

// classifyClaudeUsageHeading recognizes only headings at the start of a line.
// In particular, quota reset values such as "Resets in 5h" and "2d5h" must
// never be mistaken for a five-hour heading. Any model-specific current-week
// heading is a boundary, but only the all-models window is displayed.
func classifyClaudeUsageHeading(line string) (string, bool) {
	if match := currentWeekHeading.FindStringSubmatch(line); len(match) == 2 {
		label := strings.Join(strings.Fields(match[1]), " ")
		if strings.EqualFold(label, "all models") {
			return "seven", true
		}
		return "", true
	}
	if modelOnlyHeading.MatchString(line) {
		return "", true
	}
	if fiveHourHeading.MatchString(line) {
		return "five", true
	}
	if sevenDayHeading.MatchString(line) {
		return "seven", true
	}
	return "", false
}

// parseClaudeUsageReset accepts both the older machine-shaped timestamp and
// the human-readable reset strings emitted by current Claude Code /usage.
// Current headless output wraps text such as these in the JSON result field:
//
//	resets Aug 25, 8:09am (UTC)
//	resets Jun 22 at 8:10pm (Asia/Tokyo)
//
// The interactive usage panel can instead render a relative value such as
// "resets in 17h6m", so that form is accepted as well. The parser remains
// deliberately English-only because the collector launches Claude with
// NO_COLOR and current Claude Code emits these labels in English.
func parseClaudeUsageReset(line string, observedAt time.Time) (time.Time, bool) {
	match := resetPhrasePattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(match) != 2 {
		return time.Time{}, false
	}
	value := strings.TrimSpace(match[1])
	if stamp := timestampPattern.FindString(value); stamp != "" {
		parsed, err := time.Parse(time.RFC3339, stamp)
		if err != nil || !validClaudeResetHorizon(parsed, observedAt) {
			return time.Time{}, false
		}
		return parsed.UTC(), true
	}
	if parsed, ok := parseClaudeRelativeReset(value, observedAt); ok {
		return parsed, validClaudeResetHorizon(parsed, observedAt)
	}
	return parseClaudeAbsoluteReset(value, observedAt)
}

func parseClaudeRelativeReset(value string, observedAt time.Time) (time.Time, bool) {
	match := relativeResetPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 2 {
		return time.Time{}, false
	}
	parts := relativeResetPart.FindAllStringSubmatch(match[1], -1)
	if len(parts) == 0 {
		return time.Time{}, false
	}
	remainder := relativeResetPart.ReplaceAllString(match[1], "")
	remainder = strings.ReplaceAll(strings.ToLower(remainder), "and", "")
	if strings.Trim(remainder, " \t,·") != "" {
		return time.Time{}, false
	}

	var duration time.Duration
	for _, part := range parts {
		amount, err := strconv.ParseInt(part[1], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		var unit time.Duration
		switch strings.ToLower(part[2]) {
		case "d", "day", "days":
			unit = 24 * time.Hour
		case "h", "hr", "hrs", "hour", "hours":
			unit = time.Hour
		case "m", "min", "mins", "minute", "minutes":
			unit = time.Minute
		case "s", "sec", "secs", "second", "seconds":
			unit = time.Second
		default:
			return time.Time{}, false
		}
		// Claude's supported quota windows reset within seven days. Reject
		// implausible input before multiplying to avoid duration overflow.
		if amount > int64((8*24*time.Hour)/unit) {
			return time.Time{}, false
		}
		duration += time.Duration(amount) * unit
		if duration > 8*24*time.Hour {
			return time.Time{}, false
		}
	}
	return observedAt.Add(duration).UTC(), true
}

func parseClaudeAbsoluteReset(value string, observedAt time.Time) (time.Time, bool) {
	match := absoluteResetPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 8 {
		return time.Time{}, false
	}
	month, ok := englishMonth(match[1])
	if !ok {
		return time.Time{}, false
	}
	day, dayErr := strconv.Atoi(match[2])
	hour, hourErr := strconv.Atoi(match[4])
	minute := 0
	var minuteErr error
	if match[5] != "" {
		minute, minuteErr = strconv.Atoi(match[5])
	}
	if dayErr != nil || hourErr != nil || minuteErr != nil || day < 1 || day > 31 || minute < 0 || minute > 59 {
		return time.Time{}, false
	}
	meridiem := strings.ToLower(match[6])
	if meridiem != "" {
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		if hour == 12 {
			hour = 0
		}
		if meridiem == "pm" {
			hour += 12
		}
	} else if hour < 0 || hour > 23 {
		return time.Time{}, false
	}

	location := observedAt.Location()
	if zoneName := strings.TrimSpace(match[7]); zoneName != "" {
		var err error
		location, err = time.LoadLocation(zoneName)
		if err != nil {
			return time.Time{}, false
		}
	}
	localObserved := observedAt.In(location)
	explicitYear := match[3] != ""
	if explicitYear {
		year, err := strconv.Atoi(match[3])
		if err != nil {
			return time.Time{}, false
		}
		parsed, ok := buildClaudeAbsoluteReset(year, month, day, hour, minute, location)
		if !ok || !validClaudeResetHorizon(parsed, observedAt) {
			return time.Time{}, false
		}
		return parsed.UTC(), true
	}

	// The CLI normally omits the year. Considering the adjacent years handles
	// both directions of the boundary: late December -> early January and a
	// briefly stale December value observed just after New Year. At most one
	// candidate can fall inside a quota window-sized horizon.
	for _, year := range []int{localObserved.Year() - 1, localObserved.Year(), localObserved.Year() + 1} {
		parsed, ok := buildClaudeAbsoluteReset(year, month, day, hour, minute, location)
		if ok && validClaudeResetHorizon(parsed, observedAt) {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func buildClaudeAbsoluteReset(year int, month time.Month, day, hour, minute int, location *time.Location) (time.Time, bool) {
	parsed := time.Date(year, month, day, hour, minute, 0, 0, location)
	local := parsed.In(location)
	// time.Date normalizes invalid dates and daylight-saving gaps. Reject them
	// instead of silently storing a different day or wall-clock reset time.
	if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
		return time.Time{}, false
	}
	return parsed, true
}

func validClaudeResetHorizon(reset, observedAt time.Time) bool {
	delta := reset.Sub(observedAt)
	return delta >= -claudeResetPastTolerance && delta <= claudeResetMaxHorizon
}

func englishMonth(value string) (time.Month, bool) {
	lower := strings.ToLower(strings.TrimSpace(value))
	for month := time.January; month <= time.December; month++ {
		name := strings.ToLower(month.String())
		if lower == name || lower == name[:3] {
			return month, true
		}
	}
	return 0, false
}

// MergeProviderReports prefers newer observations but fills omitted windows
// from the older source. This is used to combine /usage polling and statusLine
// updates, which can arrive independently.
func MergeProviderReports(a, b model.ProviderReport) model.ProviderReport {
	newer, older := a, b
	if b.ObservedAt.After(a.ObservedAt) {
		newer, older = b, a
	}
	// A successful auth-status observation is authoritative. Carrying quota
	// windows across an explicit logout would make old data look freshly
	// authenticated, so clear defensive caller-supplied windows as well.
	if newer.AuthState == "unauthenticated" {
		newer.Windows = model.ProviderWindows{}
		return newer
	}
	if newer.Windows.FiveHour == nil && reusableWindow(older.Windows.FiveHour, newer.ObservedAt) {
		newer.Windows.FiveHour = older.Windows.FiveHour
	}
	if newer.Windows.SevenDay == nil && reusableWindow(older.Windows.SevenDay, newer.ObservedAt) {
		newer.Windows.SevenDay = older.Windows.SevenDay
	}
	if newer.Plan == "" {
		newer.Plan = older.Plan
	}
	if newer.AuthState == "" {
		newer.AuthState = older.AuthState
	}
	return newer
}

func reusableWindow(window *model.LimitWindow, observedAt time.Time) bool {
	return window != nil && !window.ResetsAt.IsZero() && window.ResetsAt.After(observedAt)
}

func objectValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}

func numberValue(object map[string]any, keys ...string) (float64, bool) {
	value := objectValue(object, keys...)
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case string:
		number, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(typed), "%"), 64)
		return number, err == nil
	}
	return 0, false
}

func boolValue(object map[string]any, keys ...string) (bool, bool) {
	value := objectValue(object, keys...)
	result, ok := value.(bool)
	return result, ok
}

func stringValue(object map[string]any, keys ...string) (string, bool) {
	value := objectValue(object, keys...)
	result, ok := value.(string)
	return result, ok
}

func stringValueNested(object map[string]any, paths ...[]string) (string, bool) {
	for _, path := range paths {
		var value any = object
		for _, key := range path {
			child, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			value = child[key]
		}
		if result, ok := value.(string); ok {
			return result, true
		}
	}
	return "", false
}
