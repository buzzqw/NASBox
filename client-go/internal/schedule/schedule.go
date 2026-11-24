// Package schedule implements the recurring pause windows shared with the
// Python client. Days use the Python convention (Monday = 0) because the same
// client.json is read by both implementations. A window whose end is at or
// before its start wraps past midnight; the start day is the listed one.
package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Window is one validated pause window.
type Window struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`

	startMin int
	endMin   int
}

// FromConfig decodes the pause_schedule value, ignoring invalid entries instead
// of failing: a manually edited client.json must not stop the client.
func FromConfig(value any) []Window {
	raw, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]map[string]any); ok {
			raw = make([]any, len(typed))
			for index, item := range typed {
				raw[index] = item
			}
		} else {
			return nil
		}
	}
	windows := make([]Window, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if window, ok := Normalize(entry); ok {
			windows = append(windows, window)
		}
	}
	return windows
}

// ToConfig encodes windows back into the generic JSON shape client.json uses.
func ToConfig(windows []Window) []any {
	out := make([]any, 0, len(windows))
	for _, window := range windows {
		days := make([]any, 0, len(window.Days))
		for _, day := range window.Days {
			days = append(days, day)
		}
		out = append(out, map[string]any{
			"days": days, "start": window.Start, "end": window.End,
		})
	}
	return out
}

// Normalize validates one decoded window.
func Normalize(entry map[string]any) (Window, bool) {
	start, ok := parseHHMM(entry["start"])
	if !ok {
		return Window{}, false
	}
	end, ok := parseHHMM(entry["end"])
	if !ok || start == end {
		return Window{}, false
	}
	days := normalizeDays(entry["days"])
	if len(days) == 0 {
		return Window{}, false
	}
	return Window{
		Days: days, Start: formatMinutes(start), End: formatMinutes(end),
		startMin: start, endMin: end,
	}, true
}

func normalizeDays(value any) []int {
	seen := map[int]bool{}
	switch list := value.(type) {
	case []any:
		for _, item := range list {
			if day, ok := dayValue(item); ok {
				seen[day] = true
			}
		}
	case []int:
		for _, day := range list {
			if day >= 0 && day <= 6 {
				seen[day] = true
			}
		}
	case []float64:
		for _, day := range list {
			if day >= 0 && day <= 6 && day == float64(int(day)) {
				seen[int(day)] = true
			}
		}
	}
	days := make([]int, 0, len(seen))
	for day := range seen {
		days = append(days, day)
	}
	sort.Ints(days)
	return days
}

func dayValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		if number >= 0 && number <= 6 {
			return number, true
		}
	case int64:
		if number >= 0 && number <= 6 {
			return int(number), true
		}
	case float64:
		if number >= 0 && number <= 6 && number == float64(int(number)) {
			return int(number), true
		}
	}
	return 0, false
}

// parseHHMM accepts only a strict "HH:MM" value.
func parseHHMM(value any) (int, bool) {
	text, ok := value.(string)
	if !ok || !strings.Contains(text, ":") {
		return 0, false
	}
	hours, minutes, found := strings.Cut(text, ":")
	if !found {
		return 0, false
	}
	hour, err1 := strconv.Atoi(hours)
	minute, err2 := strconv.Atoi(minutes)
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func formatMinutes(value int) string {
	return fmt.Sprintf("%02d:%02d", value/60, value%60)
}

// Active reports whether the window covers the instant. Python weekday() is
// Monday=0 while time.Weekday() is Sunday=0, hence the conversion.
func (w Window) Active(when time.Time) bool {
	if w.startMin == w.endMin || len(w.Days) == 0 {
		return false
	}
	minutes := when.Hour()*60 + when.Minute()
	weekday := (int(when.Weekday()) + 6) % 7
	inDays := map[int]bool{}
	for _, day := range w.Days {
		inDays[day] = true
	}
	if w.startMin < w.endMin {
		return inDays[weekday] && minutes >= w.startMin && minutes < w.endMin
	}
	if inDays[weekday] && minutes >= w.startMin {
		return true
	}
	previous := (weekday + 6) % 7
	return inDays[previous] && minutes < w.endMin
}

// Remaining returns the seconds until the window ends at the given instant,
// or 0 when the window is not active.
func (w Window) Remaining(when time.Time) int {
	if !w.Active(when) {
		return 0
	}
	minutes := when.Hour()*60 + when.Minute()
	var seconds int
	if w.startMin < w.endMin {
		seconds = (w.endMin-minutes)*60 - when.Second()
	} else if minutes >= w.startMin {
		seconds = ((24*60-minutes)+w.endMin)*60 - when.Second()
	} else {
		seconds = (w.endMin-minutes)*60 - when.Second()
	}
	if seconds < 0 {
		return 0
	}
	return seconds
}

// AnyActive reports whether at least one window is active.
func AnyActive(windows []Window, when time.Time) bool {
	for _, window := range windows {
		if window.Active(when) {
			return true
		}
	}
	return false
}

// NextRemaining returns the shortest remaining seconds among active windows.
func NextRemaining(windows []Window, when time.Time) int {
	best := -1
	for _, window := range windows {
		if !window.Active(when) {
			continue
		}
		remaining := window.Remaining(when)
		if best < 0 || remaining < best {
			best = remaining
		}
	}
	if best < 0 {
		return 0
	}
	return best
}
