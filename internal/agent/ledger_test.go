package agent

import (
	"testing"
	"time"

	"codex-claude-monitor/internal/hooks"
	"codex-claude-monitor/internal/model"
)

func TestLedgerMainAndSubagentLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	ledger := NewLedger(15 * time.Minute)
	ledger.now = func() time.Time { return now }
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "UserPromptSubmit", SessionID: "main-1", At: now})
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "SubagentStart", SessionID: "main-1", TaskID: "sub-1", At: now})
	ledger.Apply(hooks.Event{Provider: model.ProviderClaude, Name: "UserPromptSubmit", SessionID: "probe", At: now, Probe: true})
	tasks := ledger.Snapshot()
	if len(tasks) != 2 || tasks[0].Kind != model.TaskMain || tasks[1].Kind != model.TaskSub {
		t.Fatalf("unexpected active tasks: %+v", tasks)
	}
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "Stop", SessionID: "main-1", At: now})
	if tasks := ledger.Snapshot(); len(tasks) != 1 || tasks[0].Kind != model.TaskSub {
		t.Fatalf("Stop should end only main task: %+v", tasks)
	}
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "SessionEnd", SessionID: "main-1", At: now})
	if tasks := ledger.Snapshot(); len(tasks) != 0 {
		t.Fatalf("SessionEnd should remove descendants: %+v", tasks)
	}
}

func TestLedgerExpiresOrphanAfterTTL(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	ledger := NewLedger(15 * time.Minute)
	ledger.now = func() time.Time { return now }
	ledger.Apply(hooks.Event{Provider: model.ProviderClaude, Name: "UserPromptSubmit", SessionID: "session", At: now})
	now = now.Add(15*time.Minute + time.Nanosecond)
	if tasks := ledger.Snapshot(); len(tasks) != 0 {
		t.Fatalf("expired tasks remain: %+v", tasks)
	}
}

func TestLedgerPostToolUseIsHeartbeatOnly(t *testing.T) {
	now := time.Now().UTC()
	ledger := NewLedger(time.Minute)
	ledger.now = func() time.Time { return now }
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "PostToolUse", SessionID: "never-started", At: now})
	if len(ledger.Snapshot()) != 0 {
		t.Fatal("heartbeat invented a task")
	}
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "UserPromptSubmit", SessionID: "started", At: now})
	now = now.Add(45 * time.Second)
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "PostToolUse", SessionID: "started", At: now})
	now = now.Add(45 * time.Second)
	if len(ledger.Snapshot()) != 1 {
		t.Fatal("heartbeat did not refresh the task")
	}
}

func TestLedgerSubagentStopWithoutIDClearsParentChildren(t *testing.T) {
	now := time.Now().UTC()
	ledger := NewLedger(time.Minute)
	ledger.now = func() time.Time { return now }
	for _, id := range []string{"one", "two"} {
		ledger.Apply(hooks.Event{Provider: model.ProviderClaude, Name: "SubagentStart", SessionID: "parent", TaskID: id, At: now})
	}
	ledger.Apply(hooks.Event{Provider: model.ProviderClaude, Name: "SubagentStop", SessionID: "parent", At: now})
	if tasks := ledger.Snapshot(); len(tasks) != 0 {
		t.Fatalf("children remain after id-less stop: %+v", tasks)
	}
}

func TestLedgerRejectsEventAlreadyOlderThanTTL(t *testing.T) {
	now := time.Now().UTC()
	ledger := NewLedger(time.Minute)
	ledger.now = func() time.Time { return now }
	ledger.Apply(hooks.Event{Provider: model.ProviderCodex, Name: "UserPromptSubmit", SessionID: "old", At: now.Add(-2 * time.Minute)})
	if tasks := ledger.Snapshot(); len(tasks) != 0 {
		t.Fatalf("old event was replayed: %+v", tasks)
	}
}
