package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Risk string

const (
	RiskSafe        Risk = "safe"
	RiskReview      Risk = "review"
	RiskDestructive Risk = "destructive"
	RiskProtected   Risk = "protected"
	RiskInfo        Risk = "info"
)

type StorageCategory string

const (
	CategoryApplications StorageCategory = "Applications"
	CategoryDocuments    StorageCategory = "Documents"
	CategoryDeveloper    StorageCategory = "Developer"
	CategoryICloudDrive  StorageCategory = "iCloud Drive"
	CategoryIOSFiles     StorageCategory = "iOS Files"
	CategoryTV           StorageCategory = "TV"
	CategoryMusic        StorageCategory = "Music"
	CategoryBooks        StorageCategory = "Books"
	CategoryPodcasts     StorageCategory = "Podcasts"
	CategoryMail         StorageCategory = "Mail"
	CategoryMessages     StorageCategory = "Messages"
	CategoryMusicCreate  StorageCategory = "Music Creation"
	CategoryPhotos       StorageCategory = "Photos"
	CategoryTrash        StorageCategory = "Trash"
	CategoryOtherUsers   StorageCategory = "Other Users & Shared"
	CategoryMacOS        StorageCategory = "macOS"
	CategorySystemData   StorageCategory = "System Data"
	CategoryUnclassified StorageCategory = "Unclassified"
)

type ActionKind string

const (
	ActionCommand    ActionKind = "command"
	ActionTrash      ActionKind = "trash"
	ActionEmptyTrash ActionKind = "empty-trash"
)

type Action struct {
	Kind      ActionKind       `json:"kind"`
	Command   string           `json:"command,omitempty"`
	Args      []string         `json:"args,omitempty"`
	Paths     []string         `json:"paths,omitempty"`
	Immediate bool             `json:"immediate"`
	Identity  *commandIdentity `json:"-"`
}

func (a Action) Display() string {
	switch a.Kind {
	case ActionCommand:
		parts := []string{shellQuote(a.Command)}
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
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Group       string          `json:"group"`
	Category    StorageCategory `json:"category"`
	Detail      string          `json:"detail"`
	Source      string          `json:"source,omitempty"`
	Risk        Risk            `json:"risk"`
	Bytes       int64           `json:"bytes"`
	Modified    *time.Time      `json:"modified,omitempty"`
	Estimate    string          `json:"estimate"`
	Action      *Action         `json:"action,omitempty"`
	Unavailable string          `json:"unavailable,omitempty"`
}

func (i Item) Selectable() bool {
	return i.Action != nil && i.Unavailable == "" && i.Risk != RiskProtected && i.Risk != RiskInfo
}

type Disk struct {
	Path      string `json:"path"`
	Total     int64  `json:"total_bytes"`
	Free      int64  `json:"free_bytes"`
	InUse     int64  `json:"volume_in_use_bytes,omitempty"`
	Container string `json:"container,omitempty"`
}

type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Home        string    `json:"home"`
	Rootful     bool      `json:"rootful"`
	Disk        Disk      `json:"disk"`
	Surface     *Surface  `json:"surface,omitempty"`
	Health      *Health   `json:"health,omitempty"`
	Items       []Item    `json:"items"`
	Issues      []string  `json:"issues,omitempty"`
}

func (r *Report) Sort() {
	sort.SliceStable(r.Items, func(a, b int) bool {
		left := categoryOrder(r.Items[a].Category)
		right := categoryOrder(r.Items[b].Category)
		if left != right {
			return left < right
		}
		if r.Items[a].Bytes != r.Items[b].Bytes {
			return r.Items[a].Bytes > r.Items[b].Bytes
		}
		left = riskOrder(r.Items[a].Risk)
		right = riskOrder(r.Items[b].Risk)
		if left != right {
			return left < right
		}
		return r.Items[a].Name < r.Items[b].Name
	})
}

func categoryOrder(category StorageCategory) int {
	switch category {
	case CategoryApplications:
		return 0
	case CategoryDocuments:
		return 1
	case CategoryDeveloper:
		return 2
	case CategoryICloudDrive:
		return 3
	case CategoryIOSFiles:
		return 4
	case CategoryTV:
		return 5
	case CategoryMusic:
		return 6
	case CategoryBooks:
		return 7
	case CategoryPodcasts:
		return 8
	case CategoryMail:
		return 9
	case CategoryMessages:
		return 10
	case CategoryMusicCreate:
		return 11
	case CategoryPhotos:
		return 12
	case CategoryTrash:
		return 13
	case CategoryOtherUsers:
		return 14
	case CategoryMacOS:
		return 15
	case CategorySystemData:
		return 16
	default:
		return 100
	}
}

func displayCategory(category StorageCategory) string {
	if category == "" {
		return string(CategoryUnclassified)
	}
	return string(category)
}

func riskOrder(risk Risk) int {
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

func itemByID(report Report, id string) (Item, bool) {
	for _, item := range report.Items {
		if item.ID == id {
			return item, true
		}
	}
	return Item{}, false
}

func selectedItems(report Report, ids map[string]bool) []Item {
	items := make([]Item, 0, len(ids))
	for _, item := range report.Items {
		if ids[item.ID] && item.Selectable() {
			items = append(items, item)
		}
	}
	return items
}

func selectionTotals(items []Item) (direct int64, toTrash int64, emptiesTrash bool) {
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

func describeSelection(items []Item) string {
	direct, toTrash, _ := selectionTotals(items)
	parts := make([]string, 0, 2)
	if direct > 0 {
		parts = append(parts, fmt.Sprintf("up to %s reclaimed", humanBytes(direct)))
	}
	if toTrash > 0 {
		parts = append(parts, fmt.Sprintf("%s moved to Trash", humanBytes(toTrash)))
	}
	if unknown := unknownSizeCount(items); unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d action size unknown", unknown))
	}
	if len(parts) == 0 {
		return "size unknown until the cleanup runs"
	}
	return strings.Join(parts, ", ")
}

func unknownSizeCount(items []Item) int {
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
