package scan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
)

const verifyTimeout = 20 * time.Minute

type HealthLevel string

const (
	HealthOK      HealthLevel = "ok"
	HealthWatch   HealthLevel = "watch"
	HealthAlarm   HealthLevel = "alarm"
	HealthUnknown HealthLevel = "unknown"
)

type HealthSignal struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Level  HealthLevel `json:"level"`
	Value  string      `json:"value"`
	Detail string      `json:"detail"`
	Source string      `json:"source"`
}

type Health struct {
	Level    HealthLevel    `json:"level"`
	Verified bool           `json:"verified"`
	Signals  []HealthSignal `json:"signals"`
}

func HealthOrder(level HealthLevel) int {
	switch level {
	case HealthAlarm:
		return 0
	case HealthWatch:
		return 1
	case HealthUnknown:
		return 2
	default:
		return 3
	}
}

func WorseLevel(a, b HealthLevel) HealthLevel {
	if HealthOrder(a) <= HealthOrder(b) {
		return a
	}
	return b
}

func (h Health) Summary() string {
	counts := map[HealthLevel]int{}
	for _, signal := range h.Signals {
		counts[signal.Level]++
	}
	parts := make([]string, 0, 3)
	for _, level := range []HealthLevel{HealthAlarm, HealthWatch, HealthUnknown} {
		if counts[level] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", level, counts[level]))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("all %d checks clean", len(h.Signals))
	}
	return strings.Join(parts, "  ")
}

func EvaluateHealth(surface Surface, dataPath string) Health {
	health := Health{Level: HealthOK}
	health.Signals = append(health.Signals, deviceSignals(surface.Devices)...)
	health.Signals = append(health.Signals, containerSignals(surface.Containers)...)
	health.Signals = append(health.Signals, mountSignals(surface.Containers, surface.Mounts, dataPath)...)
	health.Signals = append(health.Signals, walkSignals(surface)...)
	for _, signal := range health.Signals {
		health.Level = WorseLevel(health.Level, signal.Level)
	}
	return health
}

func deviceSignals(devices []storage.StorageDevice) []HealthSignal {
	signals := make([]HealthSignal, 0, len(devices)*6)
	for _, device := range devices {
		if strings.Contains(strings.ToLower(device.Media), "disk image") {
			continue
		}
		signals = append(signals, smartStatusSignal(device))
		signals = append(signals, nvmeSignals(device)...)
	}
	return signals
}

func smartStatusSignal(device storage.StorageDevice) HealthSignal {
	status := strings.TrimSpace(device.Status)
	level, detail := HealthOK, "the controller reports no self-test failure"
	switch {
	case strings.EqualFold(status, "Verified"):
	case status == "" || strings.EqualFold(status, "Not Supported"):
		detail = "this device exposes no SMART verdict, nothing to judge"
	default:
		level, detail = HealthAlarm, "the controller is reporting a failure, back up before anything else"
	}
	if status == "" {
		status = "not reported"
	}
	signal := deviceSignal(device, "smart", "smart status "+device.Device, status, level, detail)
	signal.Detail = strings.TrimSpace(device.Media + " " + signal.Detail)
	return signal
}

func nvmeSignals(device storage.StorageDevice) []HealthSignal {
	var signals []HealthSignal

	if count, ok := device.Metric("MEDIA_ERRORS_0", "MEDIA_ERRORS_1"); ok {
		level, detail := HealthOK, "no unrecovered read or write errors on the media"
		if count > 0 {
			level, detail = HealthAlarm, "the media returned data the controller could not recover, filesystem damage is likely"
		}
		signals = append(signals, deviceSignal(device, "media-errors", "media errors", fmt.Sprintf("%d", count), level, detail))
	}

	if count, ok := device.Metric("NUM_ERROR_INFO_LOG_ENTRIES_0", "NUM_ERROR_INFO_LOG_ENTRIES_1"); ok {
		level, detail := HealthOK, "the controller error log is empty"
		if count > 0 {
			level, detail = HealthWatch, "the controller logged command failures, worth a verify pass"
		}
		signals = append(signals, deviceSignal(device, "error-log", "controller error log", fmt.Sprintf("%d entries", count), level, detail))
	}

	if spare, ok := device.Metrics["AVAILABLE_SPARE"]; ok {
		threshold := device.Metrics["AVAILABLE_SPARE_THRESHOLD"]
		level, detail := HealthOK, "spare blocks are above the vendor threshold"
		if spare < threshold {
			level, detail = HealthAlarm, "spare blocks fell below the vendor threshold, the drive is retiring"
		}
		value := fmt.Sprintf("%d%% / threshold %d%%", spare, threshold)
		signals = append(signals, deviceSignal(device, "spare", "spare blocks", value, level, detail))
	}

	if used, ok := device.Metrics["PERCENTAGE_USED"]; ok {
		level, detail := HealthOK, "write endurance consumed so far"
		switch {
		case used >= 100:
			level, detail = HealthAlarm, "the drive is past its rated write endurance"
		case used >= 80:
			level, detail = HealthWatch, "the drive is approaching its rated write endurance"
		}
		signals = append(signals, deviceSignal(device, "wear", "endurance used", fmt.Sprintf("%d%%", used), level, detail))
	}

	if count, ok := device.Metric("UNSAFE_SHUTDOWNS_0", "UNSAFE_SHUTDOWNS_1"); ok {
		level, detail := HealthOK, "power loss without a clean flush, apfs replays its journal on the next mount"
		if count >= 50 {
			level, detail = HealthWatch, "frequent unclean power loss, the usual cause of apfs metadata damage"
		}
		signals = append(signals, deviceSignal(device, "unsafe-shutdowns", "unsafe shutdowns", fmt.Sprintf("%d", count), level, detail))
	}

	return signals
}

func deviceSignal(device storage.StorageDevice, id, name, value string, level HealthLevel, detail string) HealthSignal {
	return HealthSignal{
		ID:     id + "-" + device.Device,
		Name:   name,
		Level:  level,
		Value:  value,
		Detail: detail,
		Source: "diskutil info " + device.Device,
	}
}

func containerSignals(containers []storage.Container) []HealthSignal {
	signals := make([]HealthSignal, 0, len(containers))
	for _, container := range containers {
		if container.Ceiling <= 0 {
			continue
		}
		gap := container.Unattributed()
		tolerance := container.Ceiling / 100
		if tolerance < 2*1024*1024*1024 {
			tolerance = 2 * 1024 * 1024 * 1024
		}
		level := HealthOK
		detail := "volume totals reconcile with the container"
		switch {
		case gap < -tolerance:
			level = HealthAlarm
			detail = "volumes claim more space than the container reports, the space manager disagrees with itself"
		case gap > tolerance:
			level = HealthWatch
			detail = "space held by the container but claimed by no volume, usually snapshots, sometimes orphaned blocks"
		}
		signals = append(signals, HealthSignal{
			ID:     "container-accounting-" + container.Reference,
			Name:   "container " + container.Reference + " accounting",
			Level:  level,
			Value:  storage.HumanBytes(gap) + " unattributed",
			Detail: detail,
			Source: "diskutil apfs list",
		})
	}
	return signals
}

func mountSignals(containers []storage.Container, mounts []storage.Mount, dataPath string) []HealthSignal {
	var signals []HealthSignal
	for _, container := range containers {
		for _, volume := range container.Volumes {
			if !volume.ReadOnly || !HasRole(volume, "Data") {
				continue
			}
			signals = append(signals, HealthSignal{
				ID:     "read-only-" + volume.Device,
				Name:   "data volume mounted read-only",
				Level:  HealthAlarm,
				Value:  volume.Name + " at " + volume.MountedAt,
				Detail: "the kernel forced the data volume read-only, which it does after refusing to trust the filesystem",
				Source: "getfsstat",
			})
		}
	}
	for _, mount := range mounts {
		if mount.Path != dataPath || mount.Total <= 0 {
			continue
		}
		free := float64(mount.Available) / float64(mount.Total) * 100
		level := HealthOK
		detail := "apfs has room to write metadata and run its space manager"
		switch {
		case free < 2:
			level = HealthAlarm
			detail = "apfs needs headroom to check itself, below two percent it starts failing writes"
		case free < 5:
			level = HealthWatch
			detail = "low headroom raises the chance of a failed write leaving inconsistent metadata"
		}
		signals = append(signals, HealthSignal{
			ID:     "headroom",
			Name:   "write headroom",
			Level:  level,
			Value:  fmt.Sprintf("%s free / %.1f%%", storage.HumanBytes(mount.Available), free),
			Detail: detail,
			Source: "statfs " + mount.Path,
		})
	}
	return signals
}

func HasRole(volume storage.Volume, role string) bool {
	for _, candidate := range volume.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func walkSignals(surface Surface) []HealthSignal {
	var signals []HealthSignal
	level := HealthOK
	detail := "every directory the walk opened returned readable metadata"
	if surface.Hardware > 0 {
		level = HealthAlarm
		detail = "the filesystem returned io errors while reading directory metadata, this is what a damaged tree looks like"
	}
	signals = append(signals, HealthSignal{
		ID:     "walk-io",
		Name:   "io errors during walk",
		Level:  level,
		Value:  fmt.Sprintf("%d", surface.Hardware),
		Detail: detail,
		Source: "surface walk",
	})

	loopLevel := HealthOK
	loopDetail := "no directory was reachable through more than one path"
	if surface.Loops > 0 {
		if surface.Dedicated {
			loopLevel = HealthAlarm
			loopDetail = "a directory inode appeared twice, which means a cycle in the tree"
		} else {
			loopDetail = "this machine has no dedicated data volume, so firmlinks account for the repeats"
		}
	}
	signals = append(signals, HealthSignal{
		ID:     "walk-loops",
		Name:   "directory loops",
		Level:  loopLevel,
		Value:  fmt.Sprintf("%d", surface.Loops),
		Detail: loopDetail,
		Source: "surface walk",
	})

	if surface.Claimed > 0 {
		gap := surface.Claimed - surface.Walked
		share := float64(gap) / float64(surface.Claimed) * 100
		coverageLevel := HealthOK
		coverageDetail := "the walk reached essentially everything the volume claims to hold"
		switch {
		case gap < 0 && -share > 2:
			coverageLevel = HealthWatch
			coverageDetail = "the walk counted more than the volume claims, usually clones counted twice"
		case surface.Denied > 0 && share > 2:
			coverageLevel = HealthWatch
			coverageDetail = "the gap is explained by unreadable trees, grant Full Disk Access or rerun with sudo --root"
			if surface.Rootful {
				coverageDetail = "the remaining unreadable trees are protected from root as well, so this gap is expected"
			}
		case share > 5:
			coverageLevel = HealthWatch
			coverageDetail = "space the volume claims that no readable file accounts for, run a verify pass"
		}
		signals = append(signals, HealthSignal{
			ID:     "walk-coverage",
			Name:   "surface coverage",
			Level:  coverageLevel,
			Value:  fmt.Sprintf("%s walked / %s claimed", storage.HumanBytes(surface.Walked), storage.HumanBytes(surface.Claimed)),
			Detail: coverageDetail,
			Source: "surface walk vs apfs",
		})
	}

	if surface.Denied > 0 {
		detail := "these trees were skipped, so their bytes are reported as unaccounted rather than guessed"
		if surface.Rootful {
			detail = "SIP and data vaults refuse root too, so these bytes stay unaccounted rather than guessed"
		}
		signals = append(signals, HealthSignal{
			ID:     "walk-denied",
			Name:   "unreadable entries",
			Level:  HealthUnknown,
			Value:  fmt.Sprintf("%d", surface.Denied),
			Detail: detail,
			Source: "surface walk",
		})
	}
	return signals
}

func VerifySignals(ctx context.Context, containers []storage.Container, dataPath string) []HealthSignal {
	var signals []HealthSignal
	for _, container := range containers {
		for _, volume := range container.Volumes {
			if volume.MountedAt != dataPath {
				continue
			}
			output, err := storage.VerifyVolume(ctx, verifyTimeout, volume.Device)
			level := HealthOK
			value := "filesystem appears to be OK"
			detail := "live fsck_apfs found no fault it could name"
			if err != nil || !strings.Contains(output, "appears to be OK") {
				level = HealthAlarm
				value = verifyVerdict(output)
				detail = "a live check cannot repair anything, boot to recovery and run First Aid on the container"
			}
			signals = append(signals, HealthSignal{
				ID:     "verify-" + volume.Device,
				Name:   "live verify " + volume.Name,
				Level:  level,
				Value:  value,
				Detail: detail,
				Source: "diskutil verifyVolume " + volume.Device,
			})
		}
	}
	return signals
}

func verifyVerdict(output string) string {
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line != "" {
			return text.Clean(line)
		}
	}
	return "verify produced no output"
}
