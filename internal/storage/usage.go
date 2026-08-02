package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type InodeKey struct {
	Device uint64
	Inode  uint64
}

type Usage struct {
	Bytes   int64
	Files   int64
	Denied  int64
	Crossed int64
}

func PathUsage(ctx context.Context, root string) (Usage, error) {
	return PathUsageExcluding(ctx, root, nil)
}

func PathUsageExcluding(ctx context.Context, root string, excludedPaths []string) (Usage, error) {
	result := Usage{}
	seen := make(map[InodeKey]struct{})
	excluded := make(map[string]bool, len(excludedPaths))
	for _, path := range excludedPaths {
		excluded[filepath.Clean(path)] = true
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return result, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return result, nil
	}
	rootDevice := uint64(0)
	if stat, ok := rootInfo.Sys().(*syscall.Stat_t); ok {
		rootDevice = DeviceID(stat)
	}

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if excluded[filepath.Clean(path)] {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				result.Denied++
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			if errors.Is(infoErr, fs.ErrPermission) {
				result.Denied++
				return nil
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if rootDevice != 0 && DeviceID(stat) != rootDevice {
				result.Crossed++
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			key := InodeKey{Device: DeviceID(stat), Inode: stat.Ino}
			if _, exists := seen[key]; exists {
				return nil
			}
			seen[key] = struct{}{}
			result.Bytes += stat.Blocks * 512
		} else {
			result.Bytes += info.Size()
		}
		if !info.IsDir() {
			result.Files++
		}
		return nil
	})
	return result, err
}

func DiskUsage(path string) (Disk, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Disk{}, err
	}
	blockSize := int64(stat.Bsize)
	return Disk{
		Path:  path,
		Total: int64(stat.Blocks) * blockSize,
		Free:  int64(stat.Bavail) * blockSize,
	}, nil
}

func HumanBytes(bytes int64) string {
	if bytes < 0 {
		return "unknown"
	}
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(bytes)
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == "TiB" {
			if value >= 10 {
				return fmt.Sprintf("%.0f %s", value, unit)
			}
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", bytes)
}

var sizePattern = regexp.MustCompile(`(?i)\b([0-9]+(?:\.[0-9]+)?)\s*([kmgt]?i?b)\b`)

func ParseLargestSize(output string) int64 {
	var largest int64
	for _, parsed := range parsedSizes(output) {
		if parsed > largest {
			largest = parsed
		}
	}
	return largest
}

func SumSizes(output string) int64 {
	var total int64
	for _, parsed := range parsedSizes(output) {
		total += parsed
	}
	return total
}

func parsedSizes(output string) []int64 {
	var result []int64
	for _, match := range sizePattern.FindAllStringSubmatch(output, -1) {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		multiplier := float64(1)
		switch strings.ToUpper(match[2]) {
		case "KB":
			multiplier = 1000
		case "KIB":
			multiplier = 1024
		case "MB":
			multiplier = 1000 * 1000
		case "MIB":
			multiplier = 1024 * 1024
		case "GB":
			multiplier = 1000 * 1000 * 1000
		case "GIB":
			multiplier = 1024 * 1024 * 1024
		case "TB":
			multiplier = 1000 * 1000 * 1000 * 1000
		case "TIB":
			multiplier = 1024 * 1024 * 1024 * 1024
		}
		result = append(result, int64(math.Round(value*multiplier)))
	}
	return result
}

func RelativeHome(home, path string) string {
	relative, err := filepath.Rel(home, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return path
	}
	return "~/" + relative
}
