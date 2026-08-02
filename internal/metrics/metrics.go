// Package metrics reads live system state. macOS exposes most of it through
// Mach APIs that need cgo, which this binary does not use, so each source here
// is the cheapest command that answers a whole question at once rather than one
// fork per number.
package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	// sampleWindow is how long the CPU and network deltas are measured over.
	// Anything shorter reads as noise on a busy machine.
	sampleWindow  = time.Second
	quickTimeout  = 5 * time.Second
	sampleTimeout = 15 * time.Second
)

type Snapshot struct {
	At        time.Time     `json:"at"`
	Host      string        `json:"host"`
	LoadScore int           `json:"load_score"`
	Hardware  Hardware      `json:"hardware"`
	CPU       CPU           `json:"cpu"`
	Memory    Memory        `json:"memory"`
	Disks     []Disk        `json:"disks"`
	Network   Network       `json:"network"`
	Power     *Power        `json:"power,omitempty"`
	Processes []Process     `json:"processes,omitempty"`
	Issues    []string      `json:"issues,omitempty"`
	Elapsed   time.Duration `json:"elapsed_ns"`
}

type Hardware struct {
	Model       string        `json:"model"`
	Chip        string        `json:"chip"`
	Cores       int           `json:"cores"`
	Physical    int           `json:"physical_cores"`
	MemoryTotal int64         `json:"memory_total"`
	OS          string        `json:"macos"`
	Uptime      time.Duration `json:"uptime_ns"`
}

type CPU struct {
	User   float64    `json:"user_percent"`
	System float64    `json:"system_percent"`
	Idle   float64    `json:"idle_percent"`
	Busy   float64    `json:"busy_percent"`
	Load   [3]float64 `json:"load_average"`
	Cores  int        `json:"cores"`
}

// Memory reports two numbers that are not complements of each other, because
// macOS does not treat them as such. Used is what Activity Monitor calls Memory
// Used: application memory plus wired plus compressed. Available is what the
// kernel can still hand out without swapping, which includes inactive and
// purgeable pages that Used has already counted. Reporting only one of them is
// how a tool ends up claiming a healthy machine is at 99%.
type Memory struct {
	Total          int64   `json:"total"`
	Used           int64   `json:"used"`
	Available      int64   `json:"available"`
	Free           int64   `json:"free"`
	Wired          int64   `json:"wired"`
	Compressed     int64   `json:"compressed"`
	App            int64   `json:"app"`
	UsedShare      float64 `json:"used_percent"`
	AvailableShare float64 `json:"available_percent"`
	SwapTotal      int64   `json:"swap_total"`
	SwapUsed       int64   `json:"swap_used"`
	// Pressure is the share of memory this tool cannot account as reclaimable:
	// 100 minus AvailableShare. It is not the number `memory_pressure` prints,
	// which uses a kernel heuristic that also counts file-backed and swappable
	// pages as free, and it is deliberately the more pessimistic of the two.
	// The load score uses it because Used runs high on any healthy macOS
	// machine and would make the score permanently gloomy.
	Pressure float64 `json:"pressure_percent"`
}

type Disk struct {
	Device string `json:"device"`
	// Throughput is iostat's MB/s column, which is reads and writes together.
	// It is not split, so it is not named as though it were.
	Throughput  float64 `json:"throughput_mb_per_second"`
	Transfers   float64 `json:"transfers_per_second"`
	KBPerAccess float64 `json:"kb_per_transfer"`
}

type Volume struct {
	Path      string  `json:"path"`
	Total     int64   `json:"total"`
	Free      int64   `json:"free"`
	UsedShare float64 `json:"used_percent"`
}

type Network struct {
	Interface string  `json:"interface"`
	DownRate  float64 `json:"down_bytes_per_second"`
	UpRate    float64 `json:"up_bytes_per_second"`
	DownTotal int64   `json:"down_bytes_total"`
	UpTotal   int64   `json:"up_bytes_total"`
}

type Power struct {
	Percent   int    `json:"percent"`
	State     string `json:"state"`
	Source    string `json:"source"`
	Charging  bool   `json:"charging"`
	Remaining string `json:"remaining,omitempty"`
}

type Process struct {
	PID    int     `json:"pid"`
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu_percent"`
	Memory float64 `json:"memory_percent"`
}

// hardwareOnce caches the static facts. Model and core count do not change
// between samples, and re-reading them every second would be a fork for nothing.
var hardwareOnce = sync.OnceValue(func() Hardware {
	ctx, cancel := context.WithTimeout(context.Background(), quickTimeout)
	defer cancel()
	return readHardware(ctx)
})

// Collect takes one snapshot. The sources that need a time window run
// concurrently, so the whole thing costs about one sample window rather than
// the sum of them.
func Collect(ctx context.Context) Snapshot {
	started := time.Now()
	snapshot := Snapshot{At: started, Hardware: hardwareOnce()}
	snapshot.Host = hostname()
	snapshot.CPU.Cores = snapshot.Hardware.Cores

	var waiter sync.WaitGroup
	var mutex sync.Mutex
	note := func(issue string) {
		if issue == "" {
			return
		}
		mutex.Lock()
		snapshot.Issues = append(snapshot.Issues, issue)
		mutex.Unlock()
	}

	waiter.Add(1)
	go func() {
		defer waiter.Done()
		cpu, disks, issue := readIOStat(ctx)
		mutex.Lock()
		snapshot.CPU.User, snapshot.CPU.System, snapshot.CPU.Idle = cpu.User, cpu.System, cpu.Idle
		snapshot.CPU.Busy, snapshot.CPU.Load = cpu.Busy, cpu.Load
		snapshot.Disks = disks
		mutex.Unlock()
		note(issue)
	}()

	waiter.Add(1)
	go func() {
		defer waiter.Done()
		network, issue := readNetwork(ctx)
		mutex.Lock()
		snapshot.Network = network
		mutex.Unlock()
		note(issue)
	}()

	waiter.Add(1)
	go func() {
		defer waiter.Done()
		memory, issue := readMemory(ctx, snapshot.Hardware.MemoryTotal)
		mutex.Lock()
		snapshot.Memory = memory
		mutex.Unlock()
		note(issue)
	}()

	waiter.Add(1)
	go func() {
		defer waiter.Done()
		processes, issue := readProcesses(ctx)
		mutex.Lock()
		snapshot.Processes = processes
		mutex.Unlock()
		note(issue)
	}()

	waiter.Add(1)
	go func() {
		defer waiter.Done()
		power := readPower(ctx)
		mutex.Lock()
		snapshot.Power = power
		mutex.Unlock()
	}()

	waiter.Wait()
	snapshot.Elapsed = time.Since(started)
	snapshot.LoadScore = Score(snapshot)
	snapshot.Issues = storage.UniqueStrings(snapshot.Issues)
	return snapshot
}

// Volumes reports free space per mounted volume. It needs no subprocess, so it
// is separate from the sampled sources.
func Volumes(mounts []storage.Mount) []Volume {
	volumes := make([]Volume, 0, len(mounts))
	for _, mount := range mounts {
		if mount.Total <= 0 {
			continue
		}
		used := mount.Total - mount.Available
		volumes = append(volumes, Volume{
			Path:      mount.Path,
			Total:     mount.Total,
			Free:      mount.Available,
			UsedShare: float64(used) / float64(mount.Total) * 100,
		})
	}
	return volumes
}
