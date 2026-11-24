package schedule

import (
	"testing"
	"time"
)

func at(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.Local)
}

func TestSameDayWindow(t *testing.T) {
	window, ok := Normalize(map[string]any{"days": []any{3}, "start": "09:00", "end": "18:00"})
	if !ok {
		t.Fatal("window rejected")
	}
	thursday := func(hour, minute int) time.Time { return at(2026, time.October, 15, hour, minute) }
	if !window.Active(thursday(12, 0)) || !window.Active(thursday(9, 0)) {
		t.Fatal("window should be active inside")
	}
	if window.Active(thursday(18, 0)) || window.Active(thursday(8, 59)) {
		t.Fatal("window should be inactive outside")
	}
	if window.Remaining(thursday(17, 30)) != 30*60 {
		t.Fatalf("remaining = %d", window.Remaining(thursday(17, 30)))
	}
}

func TestWindowOnlyMatchesSelectedDays(t *testing.T) {
	window, _ := Normalize(map[string]any{"days": []any{3}, "start": "09:00", "end": "18:00"})
	if window.Active(at(2026, time.October, 16, 12, 0)) {
		t.Fatal("friday must not match a thursday window")
	}
}

func TestOvernightWindowWrapsPastMidnight(t *testing.T) {
	window, ok := Normalize(map[string]any{"days": []any{3}, "start": "22:00", "end": "07:00"})
	if !ok {
		t.Fatal("window rejected")
	}
	if !window.Active(at(2026, time.October, 15, 23, 0)) {
		t.Fatal("thursday evening should be paused")
	}
	if !window.Active(at(2026, time.October, 16, 2, 0)) {
		t.Fatal("friday early morning belongs to thursday")
	}
	if window.Active(at(2026, time.October, 16, 23, 0)) {
		t.Fatal("friday evening starts a new, unselected day")
	}
	if window.Active(at(2026, time.October, 15, 12, 0)) {
		t.Fatal("thursday daytime is outside")
	}
	if window.Remaining(at(2026, time.October, 16, 6, 30)) != 30*60 {
		t.Fatalf("remaining across midnight = %d", window.Remaining(at(2026, time.October, 16, 6, 30)))
	}
}

func TestMultipleWindowsTakeTheSoonestEnd(t *testing.T) {
	windows := FromConfig([]any{
		map[string]any{"days": []any{3}, "start": "08:00", "end": "20:00"},
		map[string]any{"days": []any{3}, "start": "12:00", "end": "13:00"},
	})
	when := at(2026, time.October, 15, 12, 30)
	if !AnyActive(windows, when) {
		t.Fatal("expected an active window")
	}
	if NextRemaining(windows, when) != 30*60 {
		t.Fatalf("remaining = %d", NextRemaining(windows, when))
	}
}

func TestNormalizationRejectsBadWindows(t *testing.T) {
	cases := []map[string]any{
		{"days": []any{1}, "start": "09:00", "end": "09:00"},
		{"days": []any{}, "start": "09:00", "end": "10:00"},
		{"days": []any{1}, "start": "25:00", "end": "10:00"},
		{"days": []any{1}, "start": "09:60", "end": "10:00"},
		{"days": []any{9}, "start": "09:00", "end": "10:00"},
	}
	for index, entry := range cases {
		if _, ok := Normalize(entry); ok {
			t.Fatalf("case %d accepted", index)
		}
	}
	window, ok := Normalize(map[string]any{"days": []any{1, 1, 2}, "start": "9:00", "end": "18:00"})
	if !ok || window.Start != "09:00" || len(window.Days) != 2 {
		t.Fatalf("normalized = %+v, ok = %v", window, ok)
	}
}

func TestFromConfigAcceptsTypedValues(t *testing.T) {
	windows := FromConfig([]any{map[string]any{
		"days": []any{float64(1), 2}, "start": "09:00", "end": "10:00",
	}})
	if len(windows) != 1 || len(windows[0].Days) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	encoded := ToConfig(windows)
	if len(encoded) != 1 {
		t.Fatalf("encoded = %+v", encoded)
	}
	roundTrip := FromConfig(encoded)
	if len(roundTrip) != 1 || roundTrip[0].Start != "09:00" {
		t.Fatalf("round trip = %+v", roundTrip)
	}
}
