// Package installer finds the disk images and packages left over from
// installing software. They are downloads, so removing one costs a download
// rather than the thing it installed.
package installer

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/safety"
)

const (
	CommandName    = "installer"
	defaultMinSize = int64(16 * 1024 * 1024)
	zipInspectCap  = 512 << 20
)

// extensions are formats whose whole purpose is to carry an installation.
var extensions = []string{".dmg", ".pkg", ".mpkg", ".iso", ".xip"}

type source struct {
	label string
	path  string
}

func sources(home string) []source {
	return []source{
		{"Downloads", filepath.Join(home, "Downloads")},
		{"Desktop", filepath.Join(home, "Desktop")},
		{"Library", filepath.Join(home, "Library", "Downloads")},
		{"Shared", "/Users/Shared/Downloads"},
		{"Homebrew", filepath.Join(home, "Library", "Caches", "Homebrew", "downloads")},
		{"iCloud", filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "Downloads")},
		{"Mail", filepath.Join(home, "Library", "Containers", "com.apple.mail", "Data", "Library", "Mail Downloads")},
		{"Telegram", filepath.Join(home, "Downloads", "Telegram Desktop")},
	}
}

type File struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Source   string    `json:"source"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
}

type Options struct {
	MinSize int64
}

func (o Options) minSize() int64 {
	if o.MinSize > 0 {
		return o.MinSize
	}
	return defaultMinSize
}

// Find lists installer files across every known download location. A zip is
// only included when it actually contains an app or a package at its root,
// because most zips are not installers.
func Find(ctx context.Context, home string, options Options) []File {
	var files []File
	seen := make(map[string]bool)

	for _, place := range sources(home) {
		entries, err := os.ReadDir(place.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return files
			}
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(place.path, entry.Name())
			if seen[path] {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.Size() < options.minSize() {
				continue
			}
			if !isInstaller(path, info.Size()) {
				continue
			}
			if err := safety.ValidateForDeletion(path); err != nil {
				continue
			}
			seen[path] = true
			files = append(files, File{
				Path:     path,
				Name:     displayName(place.label, entry.Name()),
				Source:   place.label,
				Bytes:    info.Size(),
				Modified: info.ModTime(),
			})
		}
	}

	sort.SliceStable(files, func(a, b int) bool {
		if files[a].Bytes != files[b].Bytes {
			return files[a].Bytes > files[b].Bytes
		}
		return files[a].Path < files[b].Path
	})
	return files
}

func isInstaller(path string, size int64) bool {
	extension := strings.ToLower(filepath.Ext(path))
	for _, known := range extensions {
		if extension == known {
			return true
		}
	}
	if extension == ".zip" {
		return zipHoldsAnInstaller(path, size)
	}
	return false
}

// zipHoldsAnInstaller opens the archive directory only, never its contents, and
// gives up on anything large enough that reading the index is not free.
func zipHoldsAnInstaller(path string, size int64) bool {
	if size > zipInspectCap {
		return false
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer func() { _ = reader.Close() }()
	for _, file := range reader.File {
		root, _, _ := strings.Cut(strings.TrimPrefix(file.Name, "./"), "/")
		lowered := strings.ToLower(root)
		if strings.HasSuffix(lowered, ".app") || strings.HasSuffix(lowered, ".pkg") {
			return true
		}
	}
	return false
}

// displayName strips Homebrew's cache prefix, which is a sha256 followed by the
// real name, so the list reads as software rather than as hashes.
func displayName(label, name string) string {
	if label != "Homebrew" {
		return name
	}
	if _, rest, found := strings.Cut(name, "--"); found {
		return rest
	}
	return name
}

// Remove routes each file to Trash. The plan is re-checked as it runs: a file
// that changed size or timestamp since it was listed is skipped rather than
// removed on stale information.
func Remove(ctx context.Context, funnel *safety.Funnel, files []File) ([]string, []string, int64, []error) {
	var removed, skipped []string
	var reclaimed int64
	var failures []error

	for _, file := range files {
		info, err := os.Lstat(file.Path)
		if err != nil {
			skipped = append(skipped, file.Path+": already gone")
			continue
		}
		if info.Size() != file.Bytes || !info.ModTime().Equal(file.Modified) {
			skipped = append(skipped, file.Path+": changed since it was listed")
			continue
		}
		_, err = funnel.Trash(ctx, safety.Request{
			Command: CommandName,
			Item:    file.Source,
			Path:    file.Path,
			Bytes:   file.Bytes,
		})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		removed = append(removed, file.Path)
		reclaimed += file.Bytes
	}
	return removed, skipped, reclaimed, failures
}

func Total(files []File) int64 {
	var total int64
	for _, file := range files {
		total += file.Bytes
	}
	return total
}
