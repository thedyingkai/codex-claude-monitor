package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"codex-claude-monitor/internal/collector"
	"codex-claude-monitor/internal/model"
)

const (
	DefaultReportInterval  = 15 * time.Second
	DefaultCollectInterval = 60 * time.Second
)

var (
	agentIDPattern      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	planOverridePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,31}$`)
)

type ProviderCollector interface {
	Collect(context.Context) (model.ProviderReport, error)
	Close() error
}

// idleResourceReleaser is implemented by collectors that can discard a
// provider helper process after a probe and recreate it on the next cycle.
// Sequential collection uses this to keep provider CLI memory from
// overlapping on constrained hosts. It intentionally remains optional so the
// default Agent behaviour and third-party collectors are unchanged.
type idleResourceReleaser interface {
	ReleaseIdleResources()
}

// ReportSink lets the standalone runtime persist reports directly without an
// internal HTTP hop or an agent bearer token. Distributed agents leave this
// nil and continue to use the authenticated HTTPS endpoint.
type ReportSink interface {
	Report(context.Context, model.AgentReport) error
}

type ReportSinkFunc func(context.Context, model.AgentReport) error

func (f ReportSinkFunc) Report(ctx context.Context, report model.AgentReport) error {
	return f(ctx, report)
}

type Config struct {
	AgentID   string
	ServerURL string
	Token     string

	Codex  ProviderCollector
	Claude ProviderCollector
	Ledger *Ledger
	// PlanOverrides fills provider tier details that a CLI does not expose.
	// For example, Claude may report only "max" while the user knows the
	// subscription is max5 or max20. Overrides affect only the display report;
	// they never alter authentication or quota values.
	PlanOverrides map[model.ProviderName]string

	HookAddr   string
	HookSecret string

	ReportInterval  time.Duration
	CollectInterval time.Duration
	// SequentialCollection probes Codex and then Claude in order instead of
	// concurrently. Collectors that support idle resource release are also
	// asked to discard their helper process before the next provider starts.
	// The zero value preserves the normal Agent's concurrent behaviour.
	SequentialCollection bool
	MaxBackoff           time.Duration
	HTTPClient           *http.Client
	Logger               *log.Logger
	Now                  func() time.Time
	ReportSink           ReportSink
	ProviderStatus       func(model.ProviderName, model.ProviderReport)

	// AllowInsecureHTTP is intended only for loopback development and tests.
	AllowInsecureHTTP bool
}

type Agent struct {
	cfg Config

	mu        sync.RWMutex
	providers map[model.ProviderName]model.ProviderReport
}

func New(cfg Config) (*Agent, error) {
	if !agentIDPattern.MatchString(cfg.AgentID) {
		return nil, errors.New("agent id must match [A-Za-z0-9._-]{1,64}")
	}
	if cfg.ReportSink == nil {
		if cfg.Token == "" {
			return nil, errors.New("agent token is required")
		}
		parsed, err := url.Parse(cfg.ServerURL)
		if err != nil || parsed.Host == "" {
			return nil, errors.New("valid server URL is required")
		}
		if parsed.User != nil {
			return nil, errors.New("server URL must not contain credentials")
		}
		if parsed.Scheme != "https" && !(cfg.AllowInsecureHTTP && parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
			return nil, errors.New("server URL must use HTTPS")
		}
	}
	if cfg.Ledger == nil {
		cfg.Ledger = NewLedger(DefaultTaskTTL)
	}
	if cfg.ReportInterval <= 0 {
		cfg.ReportInterval = DefaultReportInterval
	}
	if cfg.CollectInterval <= 0 {
		cfg.CollectInterval = DefaultCollectInterval
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 2 * time.Minute
	}
	if cfg.ReportSink == nil {
		if cfg.HTTPClient == nil {
			cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
		}
		// Reports are fixed-endpoint writes. A redirect is a deployment error and
		// must never receive the Agent bearer token, even on a related hostname or
		// after an HTTPS-to-HTTP downgrade.
		client := *cfg.HTTPClient
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		cfg.HTTPClient = &client
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	normalizedOverrides := make(map[model.ProviderName]string, len(cfg.PlanOverrides))
	for provider, rawPlan := range cfg.PlanOverrides {
		if !model.ValidProvider(provider) {
			return nil, fmt.Errorf("invalid plan override provider %q", provider)
		}
		plan := strings.TrimSpace(rawPlan)
		if plan == "" {
			continue
		}
		if !planOverridePattern.MatchString(plan) {
			return nil, fmt.Errorf("invalid %s plan override", provider)
		}
		normalizedOverrides[provider] = plan
	}
	cfg.PlanOverrides = normalizedOverrides
	return &Agent{cfg: cfg, providers: make(map[model.ProviderName]model.ProviderReport)}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run starts local ingestion, performs an immediate provider collection and
// full report, then keeps collection and reporting on independent schedules.
func Run(ctx context.Context, cfg Config) error {
	agent, err := New(cfg)
	if err != nil {
		return err
	}
	return agent.Run(ctx)
}

func (a *Agent) Run(ctx context.Context) error {
	return a.run(ctx, true)
}

// RunAfterInitialCollection starts the schedulers without queuing another
// immediate provider probe. Standalone uses this after it has synchronously
// collected and persisted the first snapshot, avoiding a transient empty
// report while also avoiding duplicate CLI probes at startup.
func (a *Agent) RunAfterInitialCollection(ctx context.Context) error {
	return a.run(ctx, false)
}

func (a *Agent) run(ctx context.Context, requestInitialCollection bool) error {
	hookErrors := make(chan error, 1)
	if a.cfg.HookSecret != "" {
		go func() {
			hookErrors <- RunHookServer(ctx, HookServerConfig{
				Addr: a.cfg.HookAddr, Secret: a.cfg.HookSecret, Ledger: a.cfg.Ledger,
				StatusLine: a.IngestStatusLine,
			})
		}()
	}

	// Provider CLIs can legitimately take longer than the 15-second task
	// reporting interval (Claude's probe timeout is 30 seconds). Keep one
	// serial collection worker off the scheduler loop so a slow CLI cannot
	// delay task heartbeats. The buffered request coalesces ticks while a
	// collection is in progress and guarantees providers are never queried by
	// overlapping cycles.
	collectionCtx, cancelCollection := context.WithCancel(ctx)
	collectionRequests := make(chan struct{}, 1)
	var collectionWorker sync.WaitGroup
	collectionWorker.Add(1)
	go func() {
		defer collectionWorker.Done()
		for {
			select {
			case <-collectionCtx.Done():
				return
			case <-collectionRequests:
				a.CollectOnce(collectionCtx)
			}
		}
	}()
	defer func() {
		cancelCollection()
		collectionWorker.Wait()
		a.closeCollectors()
	}()
	requestCollection := func() {
		select {
		case collectionRequests <- struct{}{}:
		default:
		}
	}
	if requestInitialCollection {
		requestCollection()
	}

	reportDelay := time.Duration(0)
	collectTicker := time.NewTicker(a.cfg.CollectInterval)
	defer collectTicker.Stop()
	reportTimer := time.NewTimer(reportDelay)
	defer reportTimer.Stop()
	backoff := a.cfg.ReportInterval

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-hookErrors:
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("hook server: %w", err)
			}
		case <-collectTicker.C:
			requestCollection()
		case <-reportTimer.C:
			attemptStarted := time.Now()
			if err := a.ReportOnce(ctx); err != nil {
				a.cfg.Logger.Printf("agent report failed; retrying")
				reportTimer.Reset(backoff)
				backoff *= 2
				if backoff > a.cfg.MaxBackoff {
					backoff = a.cfg.MaxBackoff
				}
			} else {
				backoff = a.cfg.ReportInterval
				// Keep successful reports on a start-to-start schedule. Otherwise
				// a slow but successful HTTPS response adds its entire latency to
				// the 15-second task heartbeat interval.
				reportTimer.Reset(successfulReportDelay(a.cfg.ReportInterval, time.Since(attemptStarted)))
			}
		}
	}
}

func successfulReportDelay(interval, elapsed time.Duration) time.Duration {
	if elapsed >= interval {
		return 0
	}
	return interval - elapsed
}

type collectionResult struct {
	provider model.ProviderName
	report   model.ProviderReport
	err      error
}

type configuredCollector struct {
	provider model.ProviderName
	adapter  ProviderCollector
}

func (a *Agent) configuredCollectors() []configuredCollector {
	collectors := make([]configuredCollector, 0, 2)
	// Keep this order explicit: low-memory standalone deployments finish and
	// release Codex app-server before starting the one-shot Claude probe.
	if a.cfg.Codex != nil {
		collectors = append(collectors, configuredCollector{model.ProviderCodex, a.cfg.Codex})
	}
	if a.cfg.Claude != nil {
		collectors = append(collectors, configuredCollector{model.ProviderClaude, a.cfg.Claude})
	}
	return collectors
}

// CollectOnce queries provider CLIs concurrently by default so one slow
// provider does not delay the other. SequentialCollection is an opt-in mode
// for memory-constrained hosts. Both paths use storeCollectionResult so error
// retention, Claude merging and status notifications remain identical.
func (a *Agent) CollectOnce(ctx context.Context) {
	collectors := a.configuredCollectors()
	if a.cfg.SequentialCollection {
		for _, item := range collectors {
			report, err := item.adapter.Collect(ctx)
			a.storeCollectionResult(collectionResult{provider: item.provider, report: report, err: err})
			if releaser, ok := item.adapter.(idleResourceReleaser); ok {
				releaser.ReleaseIdleResources()
			}
		}
		return
	}

	results := make(chan collectionResult, len(collectors))
	for _, item := range collectors {
		go func(item configuredCollector) {
			report, err := item.adapter.Collect(ctx)
			results <- collectionResult{provider: item.provider, report: report, err: err}
		}(item)
	}
	for range collectors {
		a.storeCollectionResult(<-results)
	}
}

func (a *Agent) storeCollectionResult(item collectionResult) {
	a.mu.Lock()
	previous, exists := a.providers[item.provider]
	if item.err != nil {
		a.cfg.Logger.Printf("%s collection failed; retaining last safe state", item.provider)
		if exists && (previous.Windows.FiveHour != nil || previous.Windows.SevenDay != nil) {
			if item.report.ErrorCode != "" {
				previous.ErrorCode = item.report.ErrorCode
			} else {
				previous.ErrorCode = "collection_failed"
			}
			a.providers[item.provider] = previous
		} else {
			a.providers[item.provider] = item.report
		}
	} else if item.provider == model.ProviderClaude && exists {
		a.providers[item.provider] = collector.MergeProviderReports(previous, item.report)
	} else {
		a.providers[item.provider] = item.report
	}
	stored := a.providers[item.provider]
	a.mu.Unlock()
	if a.cfg.ProviderStatus != nil {
		a.cfg.ProviderStatus(item.provider, stored)
	}
}

func (a *Agent) IngestStatusLine(report model.ProviderReport) {
	a.mu.Lock()
	if previous, ok := a.providers[model.ProviderClaude]; ok {
		a.providers[model.ProviderClaude] = collector.MergeProviderReports(previous, report)
	} else {
		a.providers[model.ProviderClaude] = report
	}
	stored := a.providers[model.ProviderClaude]
	a.mu.Unlock()
	if a.cfg.ProviderStatus != nil {
		a.cfg.ProviderStatus(model.ProviderClaude, stored)
	}
}

func (a *Agent) SnapshotReport() model.AgentReport {
	a.mu.RLock()
	providers := make(map[model.ProviderName]model.ProviderReport, len(a.providers))
	for provider, report := range a.providers {
		if override := a.cfg.PlanOverrides[provider]; override != "" {
			report.Plan = override
		}
		providers[provider] = report
	}
	a.mu.RUnlock()
	return model.AgentReport{
		SchemaVersion: model.SchemaVersion,
		AgentID:       a.cfg.AgentID,
		SentAt:        a.cfg.Now().UTC(),
		Providers:     providers,
		ActiveTasks:   a.cfg.Ledger.Snapshot(),
	}
}

func (a *Agent) ReportOnce(ctx context.Context) error {
	report := a.SnapshotReport()
	if a.cfg.ReportSink != nil {
		return a.cfg.ReportSink.Report(ctx, report)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(a.cfg.ServerURL, "/") + "/api/v1/agent/report"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "quota-monitor-agent/1")
	response, err := a.cfg.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server returned %s", response.Status)
	}
	return nil
}

func (a *Agent) closeCollectors() {
	if a.cfg.Codex != nil {
		_ = a.cfg.Codex.Close()
	}
	if a.cfg.Claude != nil {
		_ = a.cfg.Claude.Close()
	}
}

// Close releases provider child processes. It is safe to call after Run,
// whose normal shutdown path already closes the same collectors.
func (a *Agent) Close() {
	a.closeCollectors()
}
