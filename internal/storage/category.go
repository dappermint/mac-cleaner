package storage

import (
	"path/filepath"
	"strings"
)

const DataVolume = "/System/Volumes/Data"

type Category string

const (
	CategoryApplications Category = "Applications"
	CategoryDocuments    Category = "Documents"
	CategoryDeveloper    Category = "Developer"
	CategoryICloudDrive  Category = "iCloud Drive"
	CategoryIOSFiles     Category = "iOS Files"
	CategoryTV           Category = "TV"
	CategoryMusic        Category = "Music"
	CategoryBooks        Category = "Books"
	CategoryPodcasts     Category = "Podcasts"
	CategoryMail         Category = "Mail"
	CategoryMessages     Category = "Messages"
	CategoryMusicCreate  Category = "Music Creation"
	CategoryPhotos       Category = "Photos"
	CategoryTrash        Category = "Trash"
	CategoryOtherUsers   Category = "Other Users & Shared"
	CategoryMacOS        Category = "macOS"
	CategorySystemData   Category = "System Data"
	CategoryUnclassified Category = "Unclassified"
)

type Disk struct {
	Path      string `json:"path"`
	Total     int64  `json:"total_bytes"`
	Free      int64  `json:"free_bytes"`
	InUse     int64  `json:"volume_in_use_bytes,omitempty"`
	Container string `json:"container,omitempty"`
}

func CategoryOrder(category Category) int {
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

func DisplayCategory(category Category) string {
	if category == "" {
		return string(CategoryUnclassified)
	}
	return string(category)
}

func CategoryForUserData(path string) Category {
	lower := strings.ToLower(filepath.Clean(path))
	switch {
	case strings.Contains(lower, "clouddocs"), strings.Contains(lower, "mobile documents"):
		return CategoryICloudDrive
	case strings.Contains(lower, "mobilesync"):
		return CategoryIOSFiles
	case strings.Contains(lower, string(filepath.Separator)+"mail"):
		return CategoryMail
	case strings.Contains(lower, "messages"), strings.Contains(lower, "imessage"):
		return CategoryMessages
	case strings.Contains(lower, "garageband"), strings.Contains(lower, "logic"), strings.Contains(lower, "mainstage"):
		return CategoryMusicCreate
	case strings.Contains(lower, "photos"), strings.HasSuffix(lower, ".photoslibrary"):
		return CategoryPhotos
	default:
		return CategorySystemData
	}
}
