package storage

import "testing"

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"":         0,
		"0":        0,
		"512":      512,
		"1k":       1000,
		"1kb":      1000,
		"1KiB":     1024,
		"100M":     100 * 1000 * 1000,
		"100MiB":   100 * 1024 * 1024,
		"1.5GiB":   1610612736,
		"2 GB":     2 * 1000 * 1000 * 1000,
		"1TiB":     1024 * 1024 * 1024 * 1024,
		"  256k  ": 256 * 1000,
	}
	for input, want := range cases {
		got, err := ParseSize(input)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", input, got, want)
		}
	}
}

// A size the parser cannot read has to be an error, because silently reading it
// as zero would turn a filter into no filter at all.
func TestParseSizeRejectsNonsense(t *testing.T) {
	for _, input := range []string{"big", "1x", "1 parsec", "-5", "1.2.3", "MiB", "1MiB extra"} {
		if got, err := ParseSize(input); err == nil {
			t.Errorf("ParseSize(%q) accepted it and returned %d", input, got)
		}
	}
}

func TestHumanBytesRoundTripsThroughParseSize(t *testing.T) {
	for _, want := range []int64{0, 512, 1024, 1536, 100 * 1024 * 1024, 3 * 1024 * 1024 * 1024} {
		rendered := HumanBytes(want)
		got, err := ParseSize(rendered)
		if err != nil {
			t.Errorf("HumanBytes(%d) = %q, which ParseSize rejects: %v", want, rendered, err)
			continue
		}
		// HumanBytes rounds for display, so the round trip is close rather than
		// exact. A 2% band catches a unit mistake without failing on rounding.
		difference := got - want
		if difference < 0 {
			difference = -difference
		}
		if want > 0 && float64(difference)/float64(want) > 0.02 {
			t.Errorf("HumanBytes(%d) = %q, which parses back as %d", want, rendered, got)
		}
	}
}
