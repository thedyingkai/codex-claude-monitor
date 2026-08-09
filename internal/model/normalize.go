package model

import (
	"math"
	"time"
)

// ClampPercent normalizes provider values before they cross the wire. NaN and
// infinities are treated as zero because encoding/json cannot represent them.
func ClampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func NewLimitWindow(used float64, resetsAt time.Time) *LimitWindow {
	used = ClampPercent(used)
	return &LimitWindow{
		UsedPercent:      used,
		RemainingPercent: 100 - used,
		ResetsAt:         resetsAt.UTC(),
	}
}

func ValidProvider(provider ProviderName) bool {
	return provider == ProviderCodex || provider == ProviderClaude
}

func ValidTaskKind(kind TaskKind) bool {
	return kind == TaskMain || kind == TaskSub
}
