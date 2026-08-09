// Package agent coordinates local collectors, the hook task ledger, and full
// state uploads to the central service.
package agent

import (
	"sort"
	"strings"
	"sync"
	"time"

	"codex-claude-monitor/internal/hooks"
	"codex-claude-monitor/internal/model"
)

const DefaultTaskTTL = 15 * time.Minute

type taskRecord struct {
	Task   model.ActiveTask
	Parent string
}

// Ledger tracks only currently active tasks. It is local and in-memory by
// design; after an agent restart uncertain tasks disappear rather than being
// falsely reported forever.
type Ledger struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	tasks map[string]taskRecord
}

func NewLedger(ttl time.Duration) *Ledger {
	if ttl <= 0 {
		ttl = DefaultTaskTTL
	}
	return &Ledger{ttl: ttl, now: time.Now, tasks: make(map[string]taskRecord)}
}

func (l *Ledger) Apply(event hooks.Event) {
	if event.Probe || (event.Provider != model.ProviderCodex && event.Provider != model.ProviderClaude) {
		return
	}
	now := l.now().UTC()
	if !event.At.IsZero() {
		if event.At.Before(now.Add(-l.ttl)) {
			return // Do not replay an event that is already outside the task TTL.
		}
		if event.At.Before(now.Add(time.Minute)) {
			now = event.At.UTC()
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)

	mainKey := ledgerKey(event.Provider, model.TaskMain, event.SessionID)
	switch event.Name {
	case "UserPromptSubmit":
		if event.SessionID == "" {
			return
		}
		if record, ok := l.tasks[mainKey]; ok {
			record.Task.LastSeenAt = now
			l.tasks[mainKey] = record
		} else {
			l.tasks[mainKey] = taskRecord{Task: model.ActiveTask{
				Provider: event.Provider, Kind: model.TaskMain, SessionID: event.SessionID,
				StartedAt: now, LastSeenAt: now,
			}, Parent: event.SessionID}
		}
	case "PostToolUse":
		if record, ok := l.tasks[mainKey]; ok {
			record.Task.LastSeenAt = now
			l.tasks[mainKey] = record
		}
		if strings.TrimSpace(event.TaskID) != "" {
			key := ledgerKey(event.Provider, model.TaskSub, subTaskID(event))
			if record, ok := l.tasks[key]; ok {
				record.Task.LastSeenAt = now
				l.tasks[key] = record
			}
		}
	case "Stop", "StopFailure":
		delete(l.tasks, mainKey)
	case "SessionEnd":
		for key, record := range l.tasks {
			if record.Task.Provider == event.Provider && record.Parent == event.SessionID {
				delete(l.tasks, key)
			}
		}
	case "SubagentStart":
		if event.SessionID == "" {
			return
		}
		id := subTaskID(event)
		key := ledgerKey(event.Provider, model.TaskSub, id)
		if record, ok := l.tasks[key]; ok {
			record.Task.LastSeenAt = now
			l.tasks[key] = record
		} else {
			l.tasks[key] = taskRecord{Task: model.ActiveTask{
				Provider: event.Provider, Kind: model.TaskSub, SessionID: id,
				StartedAt: now, LastSeenAt: now,
			}, Parent: event.SessionID}
		}
	case "SubagentStop":
		if strings.TrimSpace(event.TaskID) != "" {
			delete(l.tasks, ledgerKey(event.Provider, model.TaskSub, subTaskID(event)))
			break
		}
		// Some provider versions omit an agent id on SubagentStop. Ending all
		// children for that parent is safer than leaving phantom work active.
		for key, record := range l.tasks {
			if record.Task.Provider == event.Provider && record.Task.Kind == model.TaskSub && record.Parent == event.SessionID {
				delete(l.tasks, key)
			}
		}
	}
}

func (l *Ledger) Snapshot() []model.ActiveTask {
	now := l.now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	tasks := make([]model.ActiveTask, 0, len(l.tasks))
	for _, record := range l.tasks {
		tasks = append(tasks, record.Task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		left := string(tasks[i].Provider) + "\x00" + string(tasks[i].Kind) + "\x00" + tasks[i].SessionID
		right := string(tasks[j].Provider) + "\x00" + string(tasks[j].Kind) + "\x00" + tasks[j].SessionID
		return left < right
	})
	return tasks
}

func (l *Ledger) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.ttl)
	for key, record := range l.tasks {
		if record.Task.LastSeenAt.Before(cutoff) {
			delete(l.tasks, key)
		}
	}
}

func ledgerKey(provider model.ProviderName, kind model.TaskKind, id string) string {
	return string(provider) + "\x00" + string(kind) + "\x00" + id
}

func subTaskID(event hooks.Event) string {
	id := strings.TrimSpace(event.TaskID)
	if id == "" {
		id = "unknown"
	}
	return event.SessionID + ":" + id
}
