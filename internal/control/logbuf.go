package control

import (
	"strings"
	"sync"
)

// LogBuffer keeps the last N log lines for the dashboard.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
	part  string
}

// NewLogBuffer keeps up to max lines.
func NewLogBuffer(max int) *LogBuffer { return &LogBuffer{max: max} }

// Write implements io.Writer.
func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data := b.part + string(p)
	parts := strings.Split(data, "\n")
	b.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if l == "" {
			continue
		}
		b.lines = append(b.lines, l)
	}
	if over := len(b.lines) - b.max; over > 0 {
		b.lines = append([]string(nil), b.lines[over:]...)
	}
	return len(p), nil
}

// Lines returns a copy of the buffered lines.
func (b *LogBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}
