// Package model defines the versioned JSON contract shared by agents, the
// central server, and the ESP32 display client.
package model

import "time"

const SchemaVersion = 1

type ProviderName string

const (
	ProviderCodex  ProviderName = "codex"
	ProviderClaude ProviderName = "claude"
)

type TaskKind string

const (
	TaskMain TaskKind = "main"
	TaskSub  TaskKind = "sub"
)

type Freshness string

const (
	FreshnessFresh       Freshness = "fresh"
	FreshnessStale       Freshness = "stale"
	FreshnessUnavailable Freshness = "unavailable"
)

// LimitWindow is intentionally provider-neutral. Providers generally expose
// used percentage, while clients want both used and remaining values.
type LimitWindow struct {
	UsedPercent      float64   `json:"usedPercent"`
	RemainingPercent float64   `json:"remainingPercent"`
	ResetsAt         time.Time `json:"resetsAt"`
}

type ProviderWindows struct {
	FiveHour *LimitWindow `json:"fiveHour"`
	SevenDay *LimitWindow `json:"sevenDay"`
}

type ProviderReport struct {
	ObservedAt time.Time       `json:"observedAt"`
	AuthState  string          `json:"authState,omitempty"`
	Plan       string          `json:"plan,omitempty"`
	Source     string          `json:"source,omitempty"`
	Windows    ProviderWindows `json:"windows"`
	ErrorCode  string          `json:"errorCode,omitempty"`
}

type ActiveTask struct {
	Provider   ProviderName `json:"provider"`
	Kind       TaskKind     `json:"kind"`
	SessionID  string       `json:"sessionId"`
	StartedAt  time.Time    `json:"startedAt"`
	LastSeenAt time.Time    `json:"lastSeenAt"`
}

// AgentReport is a full-state report. Replacing rather than appending task
// state makes recovery from dropped hook events deterministic.
type AgentReport struct {
	SchemaVersion int                             `json:"schemaVersion"`
	AgentID       string                          `json:"agentId"`
	SentAt        time.Time                       `json:"sentAt"`
	Providers     map[ProviderName]ProviderReport `json:"providers"`
	ActiveTasks   []ActiveTask                    `json:"activeTasks"`
}

type ProviderSnapshot struct {
	// ObservedAt is absent while a provider is unavailable and the server has
	// never received a usable sample. A pointer is required here because
	// encoding/json does not apply omitempty to a zero-valued time.Time struct.
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	Freshness  Freshness  `json:"freshness"`
	// LoginRequired is derived from a fixed set of internal authentication
	// states. Arbitrary collector error text is never exposed to the display.
	LoginRequired bool            `json:"loginRequired,omitempty"`
	Plan          string          `json:"plan,omitempty"`
	Source        string          `json:"source,omitempty"`
	Windows       ProviderWindows `json:"windows"`
}

type TaskCount struct {
	Main int `json:"main"`
	Sub  int `json:"sub"`
}

type TaskSummary struct {
	Codex  TaskCount `json:"codex"`
	Claude TaskCount `json:"claude"`
	Total  TaskCount `json:"total"`
}

type AgentSummary struct {
	Online int `json:"online"`
	Total  int `json:"total"`
}

type DisplaySnapshot struct {
	SchemaVersion int                               `json:"schemaVersion"`
	GeneratedAt   time.Time                         `json:"generatedAt"`
	Providers     map[ProviderName]ProviderSnapshot `json:"providers"`
	Tasks         TaskSummary                       `json:"tasks"`
	Agents        AgentSummary                      `json:"agents"`
	Warnings      []string                          `json:"warnings"`
}
