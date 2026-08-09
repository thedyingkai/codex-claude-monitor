package model

import (
	"math"
	"testing"
	"time"
)

func TestClampPercent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"negative", -1, 0},
		{"zero", 0, 0},
		{"middle", 37.5, 37.5},
		{"hundred", 100, 100},
		{"over", 101, 100},
		{"nan", math.NaN(), 0},
		{"infinity", math.Inf(1), 0},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ClampPercent(test.in); got != test.want {
				t.Fatalf("ClampPercent(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func TestNewLimitWindow(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 8, 2, 9, 0, 0, 0, time.FixedZone("test", 8*60*60))
	window := NewLimitWindow(12.25, reset)
	if window.UsedPercent != 12.25 || window.RemainingPercent != 87.75 {
		t.Fatalf("unexpected percentages: %#v", window)
	}
	if window.ResetsAt.Location() != time.UTC {
		t.Fatalf("reset time must be normalized to UTC: %v", window.ResetsAt)
	}
}
