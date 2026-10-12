package control

import (
	"image"
	"sync"
	"time"

	"github.com/coral-coder/windstream/internal/display"
)

// pointerMapper places the PC's cursor where the viewer points on the
// video when the mouse is not captured. It maps a position on the streamed
// screen onto the whole Windows desktop, in the 0..65535 units SendInput
// uses for absolute moves. Monitor positions come from DXGI (physical
// pixels), so the result is right at any display scaling.
type pointerMapper struct {
	disp *display.Manager
	list func() ([]display.Output, error)

	mu      sync.Mutex
	screen  image.Rectangle // the streamed output
	desktop image.Rectangle // every output together
	ok      bool
	at      time.Time
}

func newPointerMapper(disp *display.Manager) *pointerMapper {
	return &pointerMapper{disp: disp, list: display.List}
}

// Map implements stream.Config.PointerMap.
func (p *pointerMapper) Map(x, y uint16) (int32, int32, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Monitors can be added or rearranged while streaming; looking them up
	// costs a DXGI enumeration, so not on every mouse move.
	if time.Since(p.at) > 2*time.Second {
		p.at = time.Now()
		p.screen, p.desktop, p.ok = p.lookup()
	}
	if !p.ok {
		return 0, 0, false
	}
	nx, ny := mapPointer(x, y, p.screen, p.desktop)
	return nx, ny, true
}

func (p *pointerMapper) lookup() (screen, desktop image.Rectangle, ok bool) {
	t, err := p.disp.Target()
	if err != nil || t.Test {
		return screen, desktop, false
	}
	outs, err := p.list()
	if err != nil {
		return screen, desktop, false
	}
	for _, o := range outs {
		r := image.Rect(o.X, o.Y, o.X+o.Width, o.Y+o.Height)
		desktop = desktop.Union(r)
		if o.Adapter == t.Adapter && o.Output == t.Output {
			screen, ok = r, true
		}
	}
	return screen, desktop, ok && desktop.Dx() > 1 && desktop.Dy() > 1
}

// mapPointer converts a 0..65535 position on screen into SendInput's
// 0..65535 coordinates across desktop.
func mapPointer(x, y uint16, screen, desktop image.Rectangle) (int32, int32) {
	px := screen.Min.X + int(int64(x)*int64(screen.Dx()-1)/65535)
	py := screen.Min.Y + int(int64(y)*int64(screen.Dy()-1)/65535)
	nx := int64(px-desktop.Min.X) * 65535 / int64(desktop.Dx()-1)
	ny := int64(py-desktop.Min.Y) * 65535 / int64(desktop.Dy()-1)
	return int32(nx), int32(ny)
}

// PointerMap returns a stream.Config.PointerMap for the display disp streams.
func PointerMap(disp *display.Manager) func(x, y uint16) (int32, int32, bool) {
	return newPointerMapper(disp).Map
}
