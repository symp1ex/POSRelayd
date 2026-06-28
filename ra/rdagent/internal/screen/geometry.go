package screen

import "sync"

type Geometry struct {
	X      int
	Y      int
	Width  int
	Height int
}

var state = struct {
	mu      sync.RWMutex
	geom    Geometry
	ok      bool
	version uint64
}{}

func SetCaptureGeometry(g Geometry) {
	if g.Width <= 0 || g.Height <= 0 {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.ok && state.geom == g {
		return
	}

	state.geom = g
	state.ok = true
	state.version++
}

func ClearCaptureGeometry() {
	state.mu.Lock()
	defer state.mu.Unlock()

	if !state.ok && state.geom == (Geometry{}) {
		return
	}

	state.geom = Geometry{}
	state.ok = false
	state.version++
}

func CaptureGeometry() (Geometry, uint64, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()

	return state.geom, state.version, state.ok
}
