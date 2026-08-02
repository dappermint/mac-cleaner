package scan

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	surfaceCacheFile    = "surface-v2.json"
	surfaceCacheSchema  = 2
	maxSurfaceCacheSize = 64 << 20
	growthFloor         = 256 << 20
)

type Insight struct {
	Kind   string   `json:"kind"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Bytes  int64    `json:"bytes,omitempty"`
	Paths  []string `json:"paths,omitempty"`
}

type cachedReport struct {
	Schema  int       `json:"schema"`
	SavedAt time.Time `json:"saved_at"`
	Report  Report    `json:"report"`
}

func SurfaceCachePath(home string) string {
	return config.Path(home, surfaceCacheFile)
}

func LoadCachedReport(home string, rootful bool) (Report, error) {
	path := SurfaceCachePath(home)
	file, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > maxSurfaceCacheSize {
		return Report{}, errors.New("surface cache has an invalid size")
	}
	var cached cachedReport
	decoder := json.NewDecoder(io.LimitReader(file, maxSurfaceCacheSize+1))
	if err := decoder.Decode(&cached); err != nil {
		return Report{}, err
	}
	if cached.Schema != surfaceCacheSchema || cached.Report.Surface == nil || cached.Report.Home != filepath.Clean(home) || cached.Report.Rootful != rootful {
		return Report{}, errors.New("surface cache does not match this scan")
	}
	if cached.SavedAt.IsZero() || time.Since(cached.SavedAt) > 30*24*time.Hour {
		return Report{}, errors.New("surface cache is stale")
	}
	cached.Report.Cached = true
	return cached.Report, nil
}

func saveCachedReport(report Report) error {
	if report.Surface == nil || report.Home == "" {
		return nil
	}
	path := SurfaceCachePath(report.Home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".surface-v2-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }() //nolint:forbidigo // this is a private temp file created above
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(cachedReport{Schema: surfaceCacheSchema, SavedAt: time.Now(), Report: report}); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path) //nolint:forbidigo // atomically replaces only this package's cache file
}

func buildInsights(ctx context.Context, current Report, previous *Report) []Insight {
	var insights []Insight
	if current.Surface != nil && previous != nil && previous.Surface != nil {
		before := nodeSizes(previous.Surface.Root)
		var growth []Insight
		walkNodes(current.Surface.Root, func(node *SurfaceNode) {
			old, ok := before[node.Path]
			delta := node.Bytes - old
			if !ok || node.Path == "" || old <= 0 || delta < growthFloor || float64(delta)/float64(old) < 0.20 {
				return
			}
			growth = append(growth, Insight{
				Kind: "growth", Title: node.Name + " grew", Bytes: delta, Paths: []string{node.Path},
				Detail: storage.HumanBytes(old) + " to " + storage.HumanBytes(node.Bytes) + " since the previous walk",
			})
		})
		sort.SliceStable(growth, func(left, right int) bool { return growth[left].Bytes > growth[right].Bytes })
		if len(growth) > 5 {
			growth = growth[:5]
		}
		insights = append(insights, growth...)
	}

	now := time.Now()
	for _, item := range current.Items {
		if item.Group != "large downloads" || item.Modified == nil || now.Sub(*item.Modified) < 30*24*time.Hour {
			continue
		}
		insights = append(insights, Insight{
			Kind: "old-download", Title: item.Name, Detail: "large download untouched for " + days(now.Sub(*item.Modified)), Bytes: item.Bytes,
		})
		if len(insights) >= 10 {
			break
		}
	}
	if current.Surface != nil {
		insights = append(insights, duplicateInsights(ctx, current.Surface.LargeFiles)...)
	}
	return insights
}

func nodeSizes(root *SurfaceNode) map[string]int64 {
	result := make(map[string]int64)
	walkNodes(root, func(node *SurfaceNode) {
		if node.Path != "" {
			result[node.Path] = node.Bytes
		}
	})
	return result
}

func walkNodes(node *SurfaceNode, visit func(*SurfaceNode)) {
	if node == nil {
		return
	}
	visit(node)
	for _, child := range node.Children {
		walkNodes(child, visit)
	}
}

func duplicateInsights(ctx context.Context, files []LargeFile) []Insight {
	bySize := make(map[int64][]LargeFile)
	for _, file := range files {
		if file.Bytes > 0 {
			bySize[file.Bytes] = append(bySize[file.Bytes], file)
		}
	}
	var result []Insight
	for size, candidates := range bySize {
		if len(candidates) < 2 || len(result) >= 3 || ctx.Err() != nil {
			continue
		}
		byDigest := make(map[string][]string)
		for _, candidate := range candidates {
			digest, err := digestFile(ctx, candidate.Path)
			if err == nil {
				byDigest[digest] = append(byDigest[digest], candidate.Path)
			}
		}
		for _, paths := range byDigest {
			if len(paths) < 2 {
				continue
			}
			result = append(result, Insight{
				Kind: "duplicate", Title: "identical large files", Detail: strings.Join(paths, " and "), Bytes: size * int64(len(paths)-1), Paths: paths,
			})
			if len(result) >= 3 {
				break
			}
		}
	}
	return result
}

func digestFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	reader := bufio.NewReaderSize(file, 256*1024)
	buffer := make([]byte, 256*1024)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func days(duration time.Duration) string {
	count := int(duration / (24 * time.Hour))
	if count == 1 {
		return "1 day"
	}
	return strconv.Itoa(count) + " days"
}
