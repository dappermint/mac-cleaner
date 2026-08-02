package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	mountNoWait   = 2
	mountReadOnly = 0x00000001
)

var wholeDiskPattern = regexp.MustCompile(`^(disk[0-9]+)`)

type Mount struct {
	Source    string `json:"source"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	ReadOnly  bool   `json:"read_only"`
	Total     int64  `json:"total_bytes"`
	Available int64  `json:"available_bytes"`
	Device    uint64 `json:"-"`
}

type PhysicalStore struct {
	Device string `json:"device"`
	Size   int64  `json:"size_bytes"`
}

type Volume struct {
	Device    string   `json:"device"`
	Name      string   `json:"name"`
	Roles     []string `json:"roles,omitempty"`
	InUse     int64    `json:"in_use_bytes"`
	MountedAt string   `json:"mounted_at,omitempty"`
	ReadOnly  bool     `json:"read_only"`
	DeviceID  uint64   `json:"-"`
}

type Container struct {
	Reference string          `json:"reference"`
	Ceiling   int64           `json:"ceiling_bytes"`
	Free      int64           `json:"free_bytes"`
	Physical  []PhysicalStore `json:"physical,omitempty"`
	Volumes   []Volume        `json:"volumes"`
}

func (c Container) VolumesInUse() int64 {
	var total int64
	for _, volume := range c.Volumes {
		total += volume.InUse
	}
	return total
}

func (c Container) Unattributed() int64 {
	return c.Ceiling - c.Free - c.VolumesInUse()
}

func (c Container) Holds(path string) bool {
	for _, volume := range c.Volumes {
		if volume.MountedAt == path {
			return true
		}
	}
	return false
}

func (v Volume) Role() string {
	if len(v.Roles) == 0 {
		return "data store"
	}
	return strings.ToLower(strings.Join(v.Roles, "+"))
}

type StorageDevice struct {
	Device   string           `json:"device"`
	Media    string           `json:"media,omitempty"`
	Status   string           `json:"smart_status,omitempty"`
	Internal bool             `json:"internal"`
	Metrics  map[string]int64 `json:"metrics,omitempty"`
}

func (d StorageDevice) Metric(low, high string) (int64, bool) {
	lowValue, ok := d.Metrics[low]
	if !ok {
		return 0, false
	}
	return lowValue | d.Metrics[high]<<32, true
}

func MountedFilesystems() ([]Mount, error) {
	count, err := syscall.Getfsstat(nil, mountNoWait)
	if err != nil {
		return nil, err
	}
	buffer := make([]syscall.Statfs_t, count+8)
	filled, err := syscall.Getfsstat(buffer, mountNoWait)
	if err != nil {
		return nil, err
	}
	if filled > len(buffer) {
		filled = len(buffer)
	}
	mounts := make([]Mount, 0, filled)
	for _, entry := range buffer[:filled] {
		mount := Mount{
			Source:    fixedString(entry.Mntfromname[:]),
			Path:      fixedString(entry.Mntonname[:]),
			Type:      fixedString(entry.Fstypename[:]),
			ReadOnly:  entry.Flags&mountReadOnly != 0,
			Total:     int64(entry.Blocks) * int64(entry.Bsize),
			Available: int64(entry.Bavail) * int64(entry.Bsize),
		}
		if info, statErr := os.Lstat(mount.Path); statErr == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				mount.Device = DeviceID(stat)
			}
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func DeviceID(stat *syscall.Stat_t) uint64 {
	return uint64(uint32(stat.Dev))
}

func fixedString(value []int8) string {
	raw := make([]byte, 0, len(value))
	for _, character := range value {
		if character == 0 {
			break
		}
		raw = append(raw, byte(character))
	}
	return string(raw)
}

type apfsListDocument struct {
	Containers []struct {
		ContainerReference string `json:"ContainerReference"`
		CapacityCeiling    int64  `json:"CapacityCeiling"`
		CapacityFree       int64  `json:"CapacityFree"`
		PhysicalStores     []struct {
			DeviceIdentifier string `json:"DeviceIdentifier"`
			Size             int64  `json:"Size"`
		} `json:"PhysicalStores"`
		Volumes []struct {
			DeviceIdentifier string   `json:"DeviceIdentifier"`
			Name             string   `json:"Name"`
			Roles            []string `json:"Roles"`
			CapacityInUse    int64    `json:"CapacityInUse"`
		} `json:"Volumes"`
	} `json:"Containers"`
}

func APFSContainers(ctx context.Context, timeout time.Duration, mounts []Mount) ([]Container, error) {
	raw, err := plistAsJSON(ctx, timeout, "/usr/sbin/diskutil", "apfs", "list", "-plist")
	if err != nil {
		return nil, err
	}
	var document apfsListDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode apfs inventory: %w", err)
	}
	bySource := make(map[string]Mount, len(mounts))
	for _, mount := range mounts {
		bySource[mount.Source] = mount
	}
	containers := make([]Container, 0, len(document.Containers))
	for _, entry := range document.Containers {
		container := Container{
			Reference: entry.ContainerReference,
			Ceiling:   entry.CapacityCeiling,
			Free:      entry.CapacityFree,
		}
		for _, store := range entry.PhysicalStores {
			container.Physical = append(container.Physical, PhysicalStore{Device: store.DeviceIdentifier, Size: store.Size})
		}
		for _, volume := range entry.Volumes {
			mapped := Volume{
				Device: volume.DeviceIdentifier,
				Name:   volume.Name,
				Roles:  volume.Roles,
				InUse:  volume.CapacityInUse,
			}
			if mount, ok := bySource["/dev/"+volume.DeviceIdentifier]; ok {
				mapped.MountedAt = mount.Path
				mapped.ReadOnly = mount.ReadOnly
				mapped.DeviceID = mount.Device
			}
			container.Volumes = append(container.Volumes, mapped)
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func StorageDevices(ctx context.Context, timeout time.Duration, containers []Container) ([]StorageDevice, []string) {
	seen := make(map[string]bool)
	devices := make([]StorageDevice, 0, len(containers))
	var issues []string
	for _, container := range containers {
		for _, store := range container.Physical {
			match := wholeDiskPattern.FindStringSubmatch(store.Device)
			if match == nil || seen[match[1]] {
				continue
			}
			seen[match[1]] = true
			device, err := storageDevice(ctx, timeout, match[1])
			if err != nil {
				issues = append(issues, "device health "+match[1]+": "+CompactError(err))
				continue
			}
			devices = append(devices, device)
		}
	}
	return devices, issues
}

func storageDevice(ctx context.Context, timeout time.Duration, device string) (StorageDevice, error) {
	raw, err := plistAsJSON(ctx, timeout, "/usr/sbin/diskutil", "info", "-plist", device)
	if err != nil {
		return StorageDevice{}, err
	}
	var document struct {
		SMARTStatus string                 `json:"SMARTStatus"`
		MediaName   string                 `json:"MediaName"`
		Internal    bool                   `json:"Internal"`
		Metrics     map[string]json.Number `json:"SMARTDeviceSpecificKeysMayVaryNotGuaranteed"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return StorageDevice{}, fmt.Errorf("decode device info: %w", err)
	}
	result := StorageDevice{
		Device:   device,
		Media:    document.MediaName,
		Status:   document.SMARTStatus,
		Internal: document.Internal,
	}
	if len(document.Metrics) > 0 {
		result.Metrics = make(map[string]int64, len(document.Metrics))
		for key, value := range document.Metrics {
			if parsed, parseErr := value.Int64(); parseErr == nil {
				result.Metrics[key] = parsed
			}
		}
	}
	return result, nil
}

func VerifyVolume(ctx context.Context, timeout time.Duration, device string) (string, error) {
	output, err := CaptureCommand(ctx, timeout, "/usr/sbin/diskutil", "VerifyVolume", device)
	return output, err
}

func plistAsJSON(ctx context.Context, timeout time.Duration, command string, args ...string) ([]byte, error) {
	raw, err := captureStdout(ctx, timeout, command, args...)
	if err != nil {
		return nil, err
	}
	converted, err := captureStdinStdout(ctx, timeout, raw, "/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	if err != nil {
		return nil, err
	}
	return converted, nil
}

func captureStdout(ctx context.Context, timeout time.Duration, command string, args ...string) ([]byte, error) {
	return captureStdinStdout(ctx, timeout, nil, command, args...)
}

func captureStdinStdout(ctx context.Context, timeout time.Duration, input []byte, command string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &cappedBuffer{limit: 8 * 1024 * 1024}
	stderr := &cappedBuffer{limit: 8 * 1024}
	cmd := exec.CommandContext(commandCtx, command, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("timed out after %s", timeout)
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, errors.New(CompactError(errors.New(message)))
		}
		return nil, err
	}
	return []byte(stdout.String()), nil
}
