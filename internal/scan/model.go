package scan

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
)

type Risk string

const (
	RiskSafe        Risk = "safe"
	RiskReview      Risk = "review"
	RiskDestructive Risk = "destructive"
	RiskProtected   Risk = "protected"
	RiskInfo        Risk = "info"
)

type ActionKind string

const (
	ActionCommand    ActionKind = "command"
	ActionTrash      ActionKind = "trash"
	ActionEmptyTrash ActionKind = "empty-trash"
)

type Action struct {
	Kind      ActionKind               `json:"kind"`
	Command   string                   `json:"command,omitempty"`
	Args      []string                 `json:"args,omitempty"`
	Paths     []string                 `json:"paths,omitempty"`
	Immediate bool                     `json:"immediate"`
	Identity  *storage.CommandIdentity `json:"-"`
}

func (a Action) Display() string {
	switch a.Kind {
	case ActionCommand:
		parts := make([]string, 0, 1+len(a.Args))
		parts = append(parts, shellQuote(a.Command))
		for _, arg := range a.Args {
			parts = append(parts, shellQuote(arg))
		}
		return strings.Join(parts, " ")
	case ActionTrash:
		return "move to ~/.Trash: " + strings.Join(a.Paths, ", ")
	case ActionEmptyTrash:
		return "permanently empty ~/.Trash"
	default:
		return "no action"
	}
}

type Item struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Group       string           `json:"group"`
	Category    storage.Category `json:"category"`
	Detail      string           `json:"detail"`
	Source      string           `json:"source,omitempty"`
	Risk        Risk             `json:"risk"`
	Bytes       int64            `json:"bytes"`
	Modified    *time.Time       `json:"modified,omitempty"`
	Estimate    string           `json:"estimate"`
	Action      *Action          `json:"action,omitempty"`
	Unavailable string           `json:"unavailable,omitempty"`
}

func (i Item) Selectable() bool {
	return i.Action != nil && i.Unavailable == "" && i.Risk != RiskProtected && i.Risk != RiskInfo
}

type Report struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Home        string       `json:"home"`
	Rootful     bool         `json:"rootful"`
	Disk        storage.Disk `json:"disk"`
	Surface     *Surface     `json:"surface,omitempty"`
	Health      *Health      `json:"health,omitempty"`
	Items       []Item       `json:"items"`
	Issues      []string     `json:"issues,omitempty"`
}

func (r *Report) Sort() {
	sort.SliceStable(r.Items, func(a, b int) bool {
		left := storage.CategoryOrder(r.Items[a].Category)
		right := storage.CategoryOrder(r.Items[b].Category)
		if left != right {
			return left < right
		}
		if r.Items[a].Bytes != r.Items[b].Bytes {
			return r.Items[a].Bytes > r.Items[b].Bytes
		}
		left = RiskOrder(r.Items[a].Risk)
		right = RiskOrder(r.Items[b].Risk)
		if left != right {
			return left < right
		}
		return r.Items[a].Name < r.Items[b].Name
	})
}

func RiskOrder(risk Risk) int {
	switch risk {
	case RiskSafe:
		return 0
	case RiskReview:
		return 1
	case RiskDestructive:
		return 2
	case RiskProtected:
		return 3
	default:
		return 4
	}
}

func shellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`;&|()<>*?[]{}!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func ItemByID(report Report, id string) (Item, bool) {
	for _, item := range report.Items {
		if item.ID == id {
			return item, true
		}
	}
	return Item{}, false
}

func SelectedItems(report Report, ids map[string]bool) []Item {
	items := make([]Item, 0, len(ids))
	for _, item := range report.Items {
		if ids[item.ID] && item.Selectable() {
			items = append(items, item)
		}
	}
	return items
}

func SelectionTotals(items []Item) (direct int64, toTrash int64, emptiesTrash bool) {
	for _, item := range items {
		if item.Action == nil {
			continue
		}
		switch item.Action.Kind {
		case ActionTrash:
			toTrash += max64(item.Bytes, 0)
		case ActionEmptyTrash:
			direct += max64(item.Bytes, 0)
			emptiesTrash = true
		default:
			direct += max64(item.Bytes, 0)
		}
	}
	if emptiesTrash {
		direct += toTrash
		toTrash = 0
	}
	return direct, toTrash, emptiesTrash
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func DescribeSelection(items []Item) string {
	direct, toTrash, _ := SelectionTotals(items)
	parts := make([]string, 0, 2)
	if direct > 0 {
		parts = append(parts, fmt.Sprintf("up to %s reclaimed", storage.HumanBytes(direct)))
	}
	if toTrash > 0 {
		parts = append(parts, fmt.Sprintf("%s moved to Trash", storage.HumanBytes(toTrash)))
	}
	if unknown := UnknownSizeCount(items); unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d action size unknown", unknown))
	}
	if len(parts) == 0 {
		return "size unknown until the cleanup runs"
	}
	return strings.Join(parts, ", ")
}

func UnknownSizeCount(items []Item) int {
	count := 0
	for _, item := range items {
		if item.Bytes < 0 {
			count++
		}
	}
	return count
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}
