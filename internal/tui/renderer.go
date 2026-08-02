package tui

import (
	"fmt"
	"io"
	"strings"
)

const (
	ansiEnterScreen = "\x1b[?1049h"
	ansiLeaveScreen = "\x1b[?1049l"
	ansiClearLine   = "\x1b[K"
)

type screenRenderer struct {
	out      io.Writer
	height   int
	width    int
	previous []string
	active   bool
}

func newScreenRenderer(out io.Writer) *screenRenderer {
	height, width := terminalSize()
	return &screenRenderer{out: out, height: height, width: width}
}

func (renderer *screenRenderer) Enter() {
	if renderer.active {
		return
	}
	renderer.active = true
	renderer.Resize()
	fmt.Fprint(renderer.out, ansiEnterScreen+ansiHideCursor+ansiClear)
}

func (renderer *screenRenderer) Exit() {
	if !renderer.active {
		return
	}
	fmt.Fprint(renderer.out, ansiShowCursor+ansiReset+ansiLeaveScreen)
	renderer.active = false
	renderer.previous = nil
}

func (renderer *screenRenderer) Resize() {
	renderer.height, renderer.width = terminalSize()
	renderer.previous = nil
}

func (renderer *screenRenderer) Invalidate() {
	renderer.previous = nil
}

func (renderer *screenRenderer) Size() (height, width int) {
	return renderer.height, renderer.width
}

func (renderer *screenRenderer) Render(lines []string) {
	if !renderer.active {
		return
	}
	frame := make([]string, renderer.height)
	copy(frame, lines)
	full := len(renderer.previous) != renderer.height
	var output strings.Builder
	if full {
		output.WriteString(ansiClear)
	}
	for row, line := range frame {
		if !full && line == renderer.previous[row] {
			continue
		}
		fmt.Fprintf(&output, "\x1b[%d;1H%s%s", row+1, line, ansiClearLine)
	}
	if output.Len() > 0 {
		fmt.Fprint(renderer.out, output.String())
	}
	renderer.previous = frame
}
