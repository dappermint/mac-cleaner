package metrics

// The load score is one number standing in for several, which makes it easy to
// mislead with. The weights and thresholds are therefore written down here
// rather than buried in an expression, and Explain returns them so the number
// printed by --json can be checked rather than trusted.
//
// It is called load, not health. Whether the filesystem is damaged is a
// different question, and the health view already owns that word.
const (
	weightCPU      = 0.35
	weightMemory   = 0.30
	weightPressure = 0.20
	weightSwap     = 0.15
)

type Component struct {
	Name    string  `json:"name"`
	Weight  float64 `json:"weight"`
	Percent float64 `json:"percent"`
	Penalty float64 `json:"penalty"`
}

// Score is 100 for an idle machine and falls as the four components climb.
func Score(snapshot Snapshot) int {
	var penalty float64
	for _, component := range Explain(snapshot) {
		penalty += component.Penalty
	}
	score := 100 - penalty
	switch {
	case score < 0:
		return 0
	case score > 100:
		return 100
	default:
		return int(score + 0.5)
	}
}

// Explain returns the components the score is built from, so a caller can show
// the working rather than the verdict.
func Explain(snapshot Snapshot) []Component {
	components := []Component{
		{Name: "cpu busy", Weight: weightCPU, Percent: clampPercent(snapshot.CPU.Busy)},
		{Name: "memory pressure", Weight: weightMemory, Percent: clampPercent(snapshot.Memory.Pressure)},
		{Name: "memory wired", Weight: weightPressure, Percent: wiredShare(snapshot.Memory)},
		{Name: "swap used", Weight: weightSwap, Percent: swapShare(snapshot.Memory)},
	}
	for index := range components {
		components[index].Penalty = components[index].Weight * components[index].Percent
	}
	return components
}

// wiredShare is the part of memory nothing can reclaim under any pressure.
func wiredShare(memory Memory) float64 {
	if memory.Total <= 0 {
		return 0
	}
	return clampPercent(float64(memory.Wired+memory.Compressed) / float64(memory.Total) * 100)
}

func swapShare(memory Memory) float64 {
	if memory.SwapTotal <= 0 {
		return 0
	}
	return clampPercent(float64(memory.SwapUsed) / float64(memory.SwapTotal) * 100)
}

func clampPercent(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 100:
		return 100
	default:
		return value
	}
}

// Level is the word to print beside the number. The bands are wide because a
// machine at 70 is not in trouble, it is being used.
func Level(score int) string {
	switch {
	case score >= 75:
		return "idle"
	case score >= 50:
		return "working"
	case score >= 25:
		return "busy"
	default:
		return "saturated"
	}
}
