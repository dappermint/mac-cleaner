package metrics

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
)

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSuffix(name, ".local")
}

func readHardware(ctx context.Context) Hardware {
	keys := []string{
		"hw.model",
		"machdep.cpu.brand_string",
		"hw.logicalcpu",
		"hw.physicalcpu",
		"hw.memsize",
		"kern.osproductversion",
		"kern.boottime",
	}
	output, err := storage.CaptureCommand(ctx, quickTimeout, "/usr/sbin/sysctl", append([]string{"-n"}, keys...)...)
	if err != nil {
		return Hardware{}
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < len(keys) {
		return Hardware{}
	}
	hardware := Hardware{
		Model:    strings.TrimSpace(lines[0]),
		Chip:     strings.TrimSpace(lines[1]),
		Cores:    atoi(lines[2]),
		Physical: atoi(lines[3]),
		OS:       strings.TrimSpace(lines[5]),
	}
	hardware.MemoryTotal = int64(atoi(lines[4]))
	hardware.Uptime = uptimeFrom(lines[6])
	return hardware
}

// uptimeFrom reads "{ sec = 1785486206, usec = 176574 } ...". Only the seconds
// matter, and a value that does not parse yields no uptime rather than a
// nonsense duration.
func uptimeFrom(line string) time.Duration {
	// "usec = " contains "sec = ", so the marker has to carry the brace that
	// only the seconds field follows.
	_, rest, found := strings.Cut(line, "{ sec = ")
	if !found {
		return 0
	}
	end := strings.IndexAny(rest, ",} ")
	if end < 0 {
		return 0
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	booted := time.Unix(seconds, 0)
	if booted.After(time.Now()) {
		return 0
	}
	return time.Since(booted)
}

// readIOStat is the one source that answers three questions at once. Two
// samples one second apart give a real CPU delta; the first row is an average
// since boot and is deliberately discarded.
func readIOStat(ctx context.Context) (CPU, []Disk, string) {
	output, err := storage.CaptureCommand(ctx, sampleTimeout, "/usr/sbin/iostat", "-c", "2", "-w", "1")
	if err != nil {
		return CPU{}, nil, "iostat: " + storage.CompactError(err)
	}
	return parseIOStat(output)
}

func parseIOStat(output string) (CPU, []Disk, string) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) < 4 {
		return CPU{}, nil, "iostat: not enough output to measure a delta"
	}
	// The header names the disks and then "cpu" and "load average". Only the
	// tokens before "cpu" are devices; treating the label words as devices was
	// what silently shifted every column and read CPU as zero.
	devices := deviceNames(lines[0])
	// The last row is the second sample, which is the one that covers a real
	// interval rather than the whole uptime.
	fields := strings.Fields(lines[len(lines)-1])
	// Each disk contributes KB/t, tps and MB/s, then cpu us/sy/id, then three
	// load figures.
	if len(fields) < len(devices)*3+6 {
		return CPU{}, nil, "iostat: unexpected column layout"
	}

	disks := make([]Disk, 0, len(devices))
	for index, device := range devices {
		base := index * 3
		disks = append(disks, Disk{
			Device:      device,
			KBPerAccess: parseFloat(fields[base]),
			Transfers:   parseFloat(fields[base+1]),
			Throughput:  parseFloat(fields[base+2]),
		})
	}

	cpuBase := len(devices) * 3
	cpu := CPU{
		User:   parseFloat(fields[cpuBase]),
		System: parseFloat(fields[cpuBase+1]),
		Idle:   parseFloat(fields[cpuBase+2]),
	}
	cpu.Busy = cpu.User + cpu.System
	cpu.Load = [3]float64{
		parseFloat(fields[cpuBase+3]),
		parseFloat(fields[cpuBase+4]),
		parseFloat(fields[cpuBase+5]),
	}
	return cpu, disks, ""
}

func deviceNames(header string) []string {
	var devices []string
	for _, field := range strings.Fields(header) {
		if field == "cpu" || field == "load" || field == "average" {
			break
		}
		devices = append(devices, field)
	}
	return devices
}

func readMemory(ctx context.Context, total int64) (Memory, string) {
	memory := Memory{Total: total}

	output, err := storage.CaptureCommand(ctx, quickTimeout, "/usr/bin/vm_stat")
	if err != nil {
		return memory, "vm_stat: " + storage.CompactError(err)
	}
	pages, pageSize := parseVMStat(output)
	if pageSize == 0 {
		return memory, "vm_stat: could not read the page size"
	}

	memory.Free = pages["free"] * pageSize
	memory.Wired = pages["wired down"] * pageSize
	memory.Compressed = pages["occupied by compressor"] * pageSize
	memory.App = max(pages["anonymous"]-pages["purgeable"], 0) * pageSize
	memory.Used = memory.App + memory.Wired + memory.Compressed
	// Inactive, speculative and purgeable pages hold data the kernel is willing
	// to drop, so they are available even though Used has counted some of them.
	memory.Available = (pages["free"] + pages["inactive"] + pages["speculative"] + pages["purgeable"]) * pageSize

	if memory.Total > 0 {
		if memory.Used > memory.Total {
			memory.Used = memory.Total
		}
		if memory.Available > memory.Total {
			memory.Available = memory.Total
		}
		memory.UsedShare = float64(memory.Used) / float64(memory.Total) * 100
		memory.AvailableShare = float64(memory.Available) / float64(memory.Total) * 100
		memory.Pressure = 100 - memory.AvailableShare
	}

	if swap, swapErr := storage.CaptureCommand(ctx, quickTimeout, "/usr/sbin/sysctl", "-n", "vm.swapusage"); swapErr == nil {
		memory.SwapTotal, memory.SwapUsed = parseSwap(swap)
	}
	return memory, ""
}

// parseVMStat reads the "Pages x: n." lines. The key is the label with its
// "Pages " prefix and trailing punctuation removed, so callers name the thing
// rather than a column index.
func parseVMStat(output string) (map[string]int64, int64) {
	pages := make(map[string]int64)
	pageSize := int64(0)
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			if index := strings.Index(line, "page size of "); index >= 0 {
				rest := line[index+len("page size of "):]
				pageSize = int64(atoi(strings.Fields(rest)[0]))
			}
			continue
		}
		label, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		// vm_stat writes both "Pages wired down:" and "Anonymous pages:", so
		// stripping only the prefix leaves the second form under a key nobody
		// looks up and silently reads as zero.
		label = strings.TrimSpace(label)
		label = strings.TrimPrefix(label, "Pages ")
		label = strings.TrimSuffix(label, " pages")
		label = strings.Trim(label, `"`)
		count, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil {
			continue
		}
		pages[strings.ToLower(label)] = count
	}
	return pages, pageSize
}

// parseSwap reads "total = 2048.00M  used = 1396.75M  free = 651.25M".
func parseSwap(output string) (total, used int64) {
	for _, field := range strings.Fields(output) {
		if !strings.HasSuffix(field, "M") {
			continue
		}
		amount := parseFloat(strings.TrimSuffix(field, "M"))
		bytes := int64(amount * 1024 * 1024)
		switch {
		case total == 0:
			total = bytes
		case used == 0:
			used = bytes
			return total, used
		}
	}
	return total, used
}

// readNetwork takes two counter samples rather than reporting totals, because a
// dashboard wants a rate and the counters are cumulative since boot. The -n
// flag matters more than it looks: without it netstat resolves every address
// and takes five seconds instead of seven milliseconds.
func readNetwork(ctx context.Context) (Network, string) {
	first, err := storage.CaptureCommand(ctx, quickTimeout, "/usr/sbin/netstat", "-inb")
	if err != nil {
		return Network{}, "netstat: " + storage.CompactError(err)
	}
	firstDown, firstUp, name := parseNetstat(first)

	select {
	case <-time.After(sampleWindow):
	case <-ctx.Done():
		return Network{Interface: name, DownTotal: firstDown, UpTotal: firstUp}, ""
	}

	second, err := storage.CaptureCommand(ctx, quickTimeout, "/usr/sbin/netstat", "-inb")
	if err != nil {
		return Network{Interface: name, DownTotal: firstDown, UpTotal: firstUp}, ""
	}
	secondDown, secondUp, _ := parseNetstat(second)

	seconds := sampleWindow.Seconds()
	network := Network{Interface: name, DownTotal: secondDown, UpTotal: secondUp}
	if secondDown >= firstDown {
		network.DownRate = float64(secondDown-firstDown) / seconds
	}
	if secondUp >= firstUp {
		network.UpRate = float64(secondUp-firstUp) / seconds
	}
	return network, ""
}

// parseNetstat sums every physical interface and names the busiest one. Loopback
// is excluded because traffic a machine sends to itself is not network use.
func parseNetstat(output string) (down, up int64, busiest string) {
	seen := make(map[string]bool)
	var busiestBytes int64
	for index, line := range strings.Split(output, "\n") {
		if index == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		name := fields[0]
		if seen[name] || strings.HasPrefix(name, "lo") || strings.HasPrefix(name, "gif") || strings.HasPrefix(name, "stf") {
			continue
		}
		if !strings.HasPrefix(fields[2], "<Link") {
			continue
		}
		seen[name] = true
		inBytes, inErr := strconv.ParseInt(fields[6], 10, 64)
		outBytes, outErr := strconv.ParseInt(fields[9], 10, 64)
		if inErr != nil || outErr != nil {
			continue
		}
		down += inBytes
		up += outBytes
		if inBytes+outBytes > busiestBytes {
			busiestBytes = inBytes + outBytes
			busiest = name
		}
	}
	return down, up, busiest
}

func readProcesses(ctx context.Context) ([]Process, string) {
	output, err := storage.CaptureCommand(ctx, quickTimeout, "/bin/ps", "-Aceo", "pid,pcpu,pmem,comm", "-r")
	if err != nil {
		return nil, "ps: " + storage.CompactError(err)
	}
	return parseProcesses(output, 8), ""
}

func parseProcesses(output string, limit int) []Process {
	processes := make([]Process, 0, limit)
	for index, line := range strings.Split(output, "\n") {
		if index == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		processes = append(processes, Process{
			PID:    pid,
			CPU:    parseFloat(fields[1]),
			Memory: parseFloat(fields[2]),
			Name:   strings.Join(fields[3:], " "),
		})
		if len(processes) >= limit {
			break
		}
	}
	return processes
}

// readPower returns nil on a machine with no battery, which is a desktop rather
// than an error, so it reports no issue either way.
func readPower(ctx context.Context) *Power {
	output, err := storage.CaptureCommand(ctx, quickTimeout, "/usr/bin/pmset", "-g", "batt")
	if err != nil {
		return nil
	}
	return parsePower(output)
}

func parsePower(output string) *Power {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return nil
	}
	power := &Power{Percent: -1}
	if index := strings.Index(lines[0], "'"); index >= 0 {
		rest := lines[0][index+1:]
		if end := strings.Index(rest, "'"); end >= 0 {
			power.Source = rest[:end]
		}
	}
	fields := strings.Split(lines[1], ";")
	for _, field := range fields {
		field = strings.TrimSpace(field)
		switch {
		case strings.HasSuffix(field, "%"):
			power.Percent = percentFrom(field)
		case field == "charging", field == "not charging", field == "discharging", field == "charged", field == "AC attached":
			if power.State == "" {
				power.State = field
			}
			power.Charging = field == "charging"
		case strings.Contains(field, "remaining"):
			power.Remaining = strings.TrimSpace(strings.TrimSuffix(field, "remaining"))
		}
	}
	if power.Percent < 0 {
		return nil
	}
	return power
}

func percentFrom(field string) int {
	for _, token := range strings.Fields(field) {
		if strings.HasSuffix(token, "%") {
			return atoi(strings.TrimSuffix(token, "%"))
		}
	}
	return -1
}

func atoi(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

func parseFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return parsed
}
