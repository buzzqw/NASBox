package diagnostics

import "strings"

// Event groups used by the log tab. They mirror the Python client's filter
// menu, so the same action lands in the same category in both clients.
var actionGroups = map[string]map[string]bool{
	"transfers": {
		"UPLOAD": true, "DOWNLOAD": true, "DELETE_LOCAL": true,
		"DELETE_REMOTE": true, "RENAME_REMOTE": true,
	},
	"errors": {
		"ERROR": true, "SAFETY_BLOCK": true, "JOURNAL_ERROR": true,
		"SERVER_DOWN": true, "SERVER_OUTDATED": true, "CANCELLED": true,
	},
	"safety": {
		"CONFLICT": true, "CONFLICT_AUTO": true, "STALE_DELETE": true,
		"SAFETY_BLOCK": true, "JOURNAL_BLOCK": true, "JOURNAL_ERROR": true,
	},
	"history": {
		"RESTORE_REMOTE_VERSION": true, "PRUNE_LOCAL_TRASH": true,
		"PRUNE_REMOTE_TRIGGER": true, "BROWSE_DELETE": true,
		"BROWSE_RENAME": true, "BROWSE_DOWNLOAD": true,
	},
}

// Filter keeps the entries in the requested group whose action, path or detail
// contains query (case-insensitive). An empty group means "all actions".
func Filter(entries []Entry, group, query string) []Entry {
	group = strings.ToLower(strings.TrimSpace(group))
	terms := strings.ToLower(strings.TrimSpace(query))
	groupSet, grouped := actionGroups[group]
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if grouped && !groupSet[strings.ToUpper(entry.Action)] {
			continue
		}
		if terms != "" {
			haystack := strings.ToLower(strings.Join([]string{
				entry.Action, entry.Path, entry.Detail,
			}, " "))
			if !strings.Contains(haystack, terms) {
				continue
			}
		}
		result = append(result, entry)
	}
	return result
}

// FormatEntry renders one event for a single-line UI row.
func FormatEntry(entry Entry) string {
	text := entry.Timestamp.Local().Format("15:04:05") + "  " + entry.Action
	if entry.Path != "" && entry.Path != "-" {
		text += "  " + entry.Path
	}
	if entry.Detail != "" {
		text += "  " + singleLine(entry.Detail)
	}
	return text
}
