//go:build windows

package control

import (
	"fmt"
	"math"
	"rdagent/internal/logger"
	"rdagent/internal/screen"
	"rdagent/internal/winsta"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procSendInput        = user32.NewProc("SendInput")
	procSetCursorPos     = user32.NewProc("SetCursorPos")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procMapVirtualKeyW   = user32.NewProc("MapVirtualKeyW")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	inputMouse    = 0
	inputKeyboard = 1

	mouseEventFLeftDown   = 0x0002
	mouseEventFLeftUp     = 0x0004
	mouseEventFRightDown  = 0x0008
	mouseEventFRightUp    = 0x0010
	mouseEventFMiddleDown = 0x0020
	mouseEventFMiddleUp   = 0x0040
	mouseEventFWheel      = 0x0800
	mouseEventFHWheel     = 0x01000

	keyEventFKeyUp    = 0x0002
	keyEventFScancode = 0x0008
	keyEventFExtended = 0x0001

	mapvkVKToVSC = 0
)

type input struct {
	Type uint32
	_    uint32
	U    [32]byte
}

type keyboardInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type mouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type inputOp uint8

const (
	inputOpMouseMove inputOp = iota + 1
	inputOpMouseButton
	inputOpMouseWheel
	inputOpKey
	inputOpReleaseKeys
)

type inputRequest struct {
	op inputOp

	x float64
	y float64

	button string
	down   bool

	deltaX int32
	deltaY int32

	code string // оставляем для backward-compatible JSON, но hot path использует vk
	vk   uint16

	keys []uint16

	reply chan error
}

type inputWorker struct {
	req  chan inputRequest
	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	mouseMu             sync.Mutex
	pendingMouseMove    inputRequest
	hasPendingMouseMove bool
}

func completeInputRequest(req inputRequest, err error) {
	if req.reply != nil {
		req.reply <- err
		return
	}

	if err != nil {
		logger.RDAgent.Warnf("async input request failed: op=%d error=%v", req.op, err)
	}
}

type virtualScreenGeometry struct {
	left   int
	top    int
	width  int
	height int
	valid  bool
}

type cursorCache struct {
	x     int
	y     int
	valid bool
}

var inputCache = struct {
	mu              sync.Mutex
	geom            virtualScreenGeometry
	geometryVersion uint64
	cursor          cursorCache
}{}

var getSystemMetrics = func(index int) int {
	ret, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(ret)
}

var setCursorPos = func(x, y int) error {
	ret, _, err := procSetCursorPos.Call(uintptr(x), uintptr(y))
	if ret == 0 {
		return err
	}
	return nil
}

func newInputWorker() *inputWorker {
	w := &inputWorker{
		req:  make(chan inputRequest, 64),
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}

	go w.run()

	return w
}

func (w *inputWorker) run() {
	defer close(w.done)

	desktop, err := bindInputDesktop("input-worker-start")
	if err != nil {
		logger.RDAgent.Warnf("Input worker initial desktop bind failed: %v", err)
		desktop = nil
	}

	if desktop != nil {
		logger.RDAgent.Infof("Input worker started on desktop=%s", desktop.Name())
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	defer func() {
		if desktop != nil {
			logger.RDAgent.Infof("Input worker stopped from desktop=%s", desktop.Name())
			desktop.Close()
		}
	}()

	for {
		select {
		case <-w.stop:
			return
		default:
		}

		select {
		case req := <-w.req:
			desktop = w.processRequest(desktop, req)
			continue
		default:
		}

		if req, ok := w.takePendingMouseMove(); ok {
			desktop = w.processRequest(desktop, req)
			continue
		}

		select {
		case <-w.stop:
			return

		case <-w.wake:
			continue

		case <-ticker.C:
			if desktop == nil {
				continue
			}

			name, err := winsta.CurrentInputDesktopName()
			if err != nil || name == "" || name == "unknown" || name == desktop.Name() {
				continue
			}

			newDesktop, err := winsta.RebindCurrentThreadToInputDesktop("input-worker-desktop-change", desktop)
			if err != nil {
				logger.RDAgent.Warnf(
					"Input worker desktop rebind failed: old=%s new=%s error=%v",
					desktop.Name(),
					name,
					err,
				)
				continue
			}

			logger.RDAgent.Infof(
				"Input worker rebound to desktop: old=%s new=%s",
				desktop.Name(),
				newDesktop.Name(),
			)

			desktop = newDesktop

		case req := <-w.req:
			if desktop == nil {
				newDesktop, err := bindInputDesktop("input-worker-lazy-bind")
				if err != nil {
					completeInputRequest(req, fmt.Errorf("input worker bind desktop: %w", err))
					continue
				}
				desktop = newDesktop
				logger.RDAgent.Infof("Input worker lazy-bound to desktop=%s", desktop.Name())
			}

			err := w.handleRequest(desktop, req)
			if err != nil {
				newDesktop, bindErr := winsta.RebindCurrentThreadToInputDesktop("input-worker-error-rebind", desktop)
				if bindErr != nil {
					completeInputRequest(req, fmt.Errorf("%w; rebind failed: %v", err, bindErr))
					continue
				}

				logger.RDAgent.Warnf(
					"Input worker rebound after injection error: old=%s new=%s original_error=%v",
					desktop.Name(),
					newDesktop.Name(),
					err,
				)

				desktop = newDesktop

				// Один retry после rebind. Для sync-запросов ошибка вернется вызывающему коду,
				// для async mouse_move будет только залогирована.
				err = w.handleRequest(desktop, req)
			}
			completeInputRequest(req, err)
		}
	}
}

func (w *inputWorker) processRequest(desktop *winsta.BoundDesktop, req inputRequest) *winsta.BoundDesktop {
	if desktop == nil {
		newDesktop, err := bindInputDesktop("input-worker-lazy-bind")
		if err != nil {
			completeInputRequest(req, fmt.Errorf("input worker bind desktop: %w", err))
			return desktop
		}
		desktop = newDesktop
		logger.RDAgent.Infof("Input worker lazy-bound to desktop=%s", desktop.Name())
	}

	err := w.handleRequest(desktop, req)
	if err != nil {
		newDesktop, bindErr := winsta.RebindCurrentThreadToInputDesktop("input-worker-error-rebind", desktop)
		if bindErr != nil {
			completeInputRequest(req, fmt.Errorf("%w; rebind failed: %v", err, bindErr))
			return desktop
		}

		logger.RDAgent.Warnf(
			"Input worker rebound after injection error: old=%s new=%s original_error=%v",
			desktop.Name(),
			newDesktop.Name(),
			err,
		)

		desktop = newDesktop
		err = w.handleRequest(desktop, req)
	}

	completeInputRequest(req, err)
	return desktop
}

func (w *inputWorker) handleRequest(desktop *winsta.BoundDesktop, req inputRequest) error {
	switch req.op {
	case inputOpMouseMove:
		return mouseMoveOnBoundDesktop(desktop, req.x, req.y)

	case inputOpMouseButton:
		return mouseButtonOnBoundDesktop(desktop, req.x, req.y, req.button, req.down)

	case inputOpMouseWheel:
		return mouseWheelOnBoundDesktop(desktop, req.x, req.y, req.deltaX, req.deltaY)

	case inputOpKey:
		if req.vk != 0 {
			return keyVKOnBoundDesktop(desktop, req.vk, req.down)
		}
		return keyOnBoundDesktop(desktop, req.code, req.down)

	case inputOpReleaseKeys:
		for _, vk := range req.keys {
			if err := sendKey(vk, true); err != nil {
				logger.RDAgent.Warnf(
					"input worker release key failed: vk=%d desktop=%s error=%v",
					vk,
					desktop.Name(),
					err,
				)
			}
		}
		return nil

	default:
		return nil
	}
}

func (w *inputWorker) call(req inputRequest) error {
	req.reply = make(chan error, 1)

	select {
	case w.req <- req:
	case <-w.done:
		return fmt.Errorf("input worker is stopped")
	}

	select {
	case err := <-req.reply:
		return err
	case <-w.done:
		return fmt.Errorf("input worker stopped before reply")
	}
}

func (w *inputWorker) post(req inputRequest) error {
	req.reply = nil

	select {
	case w.req <- req:
		return nil
	case <-w.done:
		return fmt.Errorf("input worker is stopped")
	}
}

func (w *inputWorker) tryPost(req inputRequest) bool {
	req.reply = nil

	if req.op == inputOpMouseMove {
		return w.postLatestMouseMove(req)
	}

	select {
	case w.req <- req:
		return true
	case <-w.done:
		return false
	default:
		return false
	}
}

func (w *inputWorker) postLatestMouseMove(req inputRequest) bool {
	select {
	case <-w.done:
		return false
	default:
	}

	w.mouseMu.Lock()
	w.pendingMouseMove = req
	w.hasPendingMouseMove = true
	w.mouseMu.Unlock()

	select {
	case w.wake <- struct{}{}:
	default:
	}

	return true
}

func (w *inputWorker) takePendingMouseMove() (inputRequest, bool) {
	w.mouseMu.Lock()
	defer w.mouseMu.Unlock()

	if !w.hasPendingMouseMove {
		return inputRequest{}, false
	}

	req := w.pendingMouseMove
	w.pendingMouseMove = inputRequest{}
	w.hasPendingMouseMove = false
	return req, true
}

func (w *inputWorker) close() {
	select {
	case <-w.done:
		return
	default:
	}

	close(w.stop)

	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		logger.RDAgent.Warn("Input worker stop timed out")
	}
}

func newMouseInput(flags uint32, data uint32) input {
	var in input
	in.Type = inputMouse

	mi := (*mouseInput)(unsafe.Pointer(&in.U[0]))
	mi.MouseData = data
	mi.DwFlags = flags

	return in
}

func newKeyboardInput(vk uint16, sc uint16, flags uint32) input {
	var in input
	in.Type = inputKeyboard

	ki := (*keyboardInput)(unsafe.Pointer(&in.U[0]))
	ki.WVk = vk
	ki.WScan = sc
	ki.DwFlags = flags

	return in
}

type Injector struct {
	mu       sync.Mutex
	focused  bool
	downKeys map[uint16]bool

	worker *inputWorker
}

func NewInjector() *Injector {
	logger.RDAgent.Debugf(
		"Windows input ABI sizes: input=%d mouseInput=%d keyboardInput=%d uintptr=%d",
		unsafe.Sizeof(input{}),
		unsafe.Sizeof(mouseInput{}),
		unsafe.Sizeof(keyboardInput{}),
		unsafe.Sizeof(uintptr(0)),
	)

	return &Injector{
		focused:  false,
		downKeys: make(map[uint16]bool),
		worker:   newInputWorker(),
	}
}

func bindInputDesktop(reason string) (*winsta.BoundDesktop, error) {
	desktop, err := winsta.BindCurrentThreadToInputDesktop(reason)
	if err != nil {
		return nil, err
	}

	return desktop, nil
}

func (i *Injector) SetFocus(focused bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.focused = focused
	if !focused {
		invalidateCursorCache()
		i.releaseAllLocked()
	}
}

func invalidateCursorCache() {
	inputCache.mu.Lock()
	inputCache.cursor.valid = false
	inputCache.mu.Unlock()
}

func (i *Injector) IsFocused() bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.focused
}

func (i *Injector) MouseMoveNormalized(x, y float64) error {
	i.mu.Lock()
	focused := i.focused
	i.mu.Unlock()

	if !focused {
		return nil
	}

	_ = i.worker.tryPost(inputRequest{
		op: inputOpMouseMove,
		x:  x,
		y:  y,
	})

	return nil
}

func (i *Injector) mouseMoveFallback(x, y float64) error {
	desktop, err := bindInputDesktop("mouse-move-fallback")
	if err != nil {
		return fmt.Errorf("mouse move fallback bind desktop: %w", err)
	}
	defer desktop.Close()

	return mouseMoveOnBoundDesktop(desktop, x, y)
}

func (i *Injector) MouseButton(x, y float64, button string, down bool) error {
	i.mu.Lock()
	focused := i.focused
	i.mu.Unlock()

	if !focused {
		return nil
	}

	err := i.worker.post(inputRequest{
		op:     inputOpMouseButton,
		x:      x,
		y:      y,
		button: button,
		down:   down,
	})
	if err == nil {
		return nil
	}

	logger.RDAgent.Warnf("input worker mouse button failed, using fallback: %v", err)
	return i.mouseButtonFallback(x, y, button, down)
}

func (i *Injector) mouseButtonFallback(x, y float64, button string, down bool) error {
	desktop, err := bindInputDesktop("mouse-button-fallback")
	if err != nil {
		return fmt.Errorf("mouse button fallback bind desktop: %w", err)
	}
	defer desktop.Close()

	return mouseButtonOnBoundDesktop(desktop, x, y, button, down)
}

func (i *Injector) MouseWheel(x, y float64, deltaX, deltaY int32) error {
	i.mu.Lock()
	focused := i.focused
	i.mu.Unlock()

	if !focused {
		return nil
	}

	err := i.worker.post(inputRequest{
		op:     inputOpMouseWheel,
		x:      x,
		y:      y,
		deltaX: deltaX,
		deltaY: deltaY,
	})
	if err == nil {
		return nil
	}

	logger.RDAgent.Warnf("input worker mouse wheel failed, using fallback: %v", err)
	return i.mouseWheelFallback(x, y, deltaX, deltaY)
}

func (i *Injector) mouseWheelFallback(x, y float64, deltaX, deltaY int32) error {
	desktop, err := bindInputDesktop("mouse-wheel-fallback")
	if err != nil {
		return fmt.Errorf("mouse wheel fallback bind desktop: %w", err)
	}
	defer desktop.Close()

	return mouseWheelOnBoundDesktop(desktop, x, y, deltaX, deltaY)
}

func (i *Injector) Key(code string, down bool) error {
	vk := vkFromBrowserCode(code)
	if vk == 0 {
		return nil
	}

	return i.KeyVK(vk, down)
}

func (i *Injector) KeyVK(vk uint16, down bool) error {
	if vk == 0 {
		return nil
	}

	i.mu.Lock()
	if !i.focused {
		i.mu.Unlock()
		return nil
	}

	if down {
		if i.downKeys[vk] {
			i.mu.Unlock()
			return nil
		}
		i.downKeys[vk] = true
	} else {
		delete(i.downKeys, vk)
	}
	i.mu.Unlock()

	err := i.worker.post(inputRequest{
		op:   inputOpKey,
		vk:   vk,
		down: down,
	})
	if err == nil {
		return nil
	}

	logger.RDAgent.Warnf("input worker key failed, using fallback: %v", err)
	return i.keyVKFallback(vk, down)
}

func (i *Injector) keyVKFallback(vk uint16, down bool) error {
	desktop, err := bindInputDesktop("keyboard-fallback")
	if err != nil {
		return fmt.Errorf("keyboard fallback bind desktop: %w", err)
	}
	defer desktop.Close()

	return keyVKOnBoundDesktop(desktop, vk, down)
}

func (i *Injector) keyFallback(code string, down bool) error {
	desktop, err := bindInputDesktop("keyboard-fallback")
	if err != nil {
		return fmt.Errorf("keyboard fallback bind desktop: %w", err)
	}
	defer desktop.Close()

	return keyOnBoundDesktop(desktop, code, down)
}

func (i *Injector) ReleaseAll() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.releaseAllLocked()
}

func (i *Injector) Close() {
	i.ReleaseAll()

	if i.worker != nil {
		i.worker.close()
	}
}

func (i *Injector) releaseAllLocked() {
	keys := make([]uint16, 0, len(i.downKeys))
	for vk := range i.downKeys {
		keys = append(keys, vk)
		delete(i.downKeys, vk)
	}

	if len(keys) == 0 {
		return
	}

	if err := i.worker.call(inputRequest{
		op:   inputOpReleaseKeys,
		keys: keys,
	}); err == nil {
		return
	} else {
		logger.RDAgent.Warnf("input worker release all failed, using fallback: %v", err)
	}

	desktop, err := bindInputDesktop("release-all-keys-fallback")
	if err != nil {
		logger.RDAgent.Warnf("release all fallback bind desktop failed: %v", err)
		return
	}
	defer desktop.Close()

	for _, vk := range keys {
		if err := sendKey(vk, true); err != nil {
			logger.RDAgent.Warnf(
				"release key fallback failed: vk=%d desktop=%s error=%v",
				vk,
				desktop.Name(),
				err,
			)
		}
	}
}

func refreshVirtualScreenGeometryLocked() {
	if g, version, ok := screen.CaptureGeometry(); ok {
		inputCache.geom = virtualScreenGeometry{
			left:   g.X,
			top:    g.Y,
			width:  maxInt(g.Width, 1),
			height: maxInt(g.Height, 1),
			valid:  true,
		}
		inputCache.geometryVersion = version
		inputCache.cursor.valid = false
		return
	}

	width := getSystemMetrics(smCXVirtualScreen)
	height := getSystemMetrics(smCYVirtualScreen)

	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}

	inputCache.geom = virtualScreenGeometry{
		left:   getSystemMetrics(smXVirtualScreen),
		top:    getSystemMetrics(smYVirtualScreen),
		width:  width,
		height: height,
		valid:  true,
	}
	inputCache.geometryVersion = 0
	inputCache.cursor.valid = false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func RefreshInputGeometry() {
	inputCache.mu.Lock()
	defer inputCache.mu.Unlock()

	refreshVirtualScreenGeometryLocked()
	inputCache.cursor.valid = false
}

func normalizedToScreen(x, y float64) (int, int) {
	inputCache.mu.Lock()
	defer inputCache.mu.Unlock()

	_, version, hasCaptureGeometry := screen.CaptureGeometry()
	if !inputCache.geom.valid ||
		(hasCaptureGeometry && inputCache.geometryVersion != version) ||
		(!hasCaptureGeometry && inputCache.geometryVersion != 0) {
		refreshVirtualScreenGeometryLocked()
	}

	x = math.Max(0, math.Min(1, x))
	y = math.Max(0, math.Min(1, y))

	g := inputCache.geom

	px := g.left + int(math.Round(x*float64(g.width-1)))
	py := g.top + int(math.Round(y*float64(g.height-1)))

	return px, py
}

func mouseMoveOnBoundDesktop(desktop *winsta.BoundDesktop, x, y float64) error {
	px, py := normalizedToScreen(x, y)

	inputCache.mu.Lock()
	if inputCache.cursor.valid && inputCache.cursor.x == px && inputCache.cursor.y == py {
		inputCache.mu.Unlock()
		return nil
	}
	inputCache.mu.Unlock()

	if err := setCursorPos(px, py); err != nil {
		return fmt.Errorf("SetCursorPos desktop=%s failed: %w", desktop.Name(), err)
	}

	inputCache.mu.Lock()
	inputCache.cursor = cursorCache{
		x:     px,
		y:     py,
		valid: true,
	}
	inputCache.mu.Unlock()

	return nil
}

func mouseButtonOnBoundDesktop(desktop *winsta.BoundDesktop, x, y float64, button string, down bool) error {
	if err := mouseMoveOnBoundDesktop(desktop, x, y); err != nil {
		return err
	}

	var flags uint32
	switch button {
	case "left":
		if down {
			flags = mouseEventFLeftDown
		} else {
			flags = mouseEventFLeftUp
		}
	case "right":
		if down {
			flags = mouseEventFRightDown
		} else {
			flags = mouseEventFRightUp
		}
	case "middle":
		if down {
			flags = mouseEventFMiddleDown
		} else {
			flags = mouseEventFMiddleUp
		}
	default:
		return nil
	}

	if err := sendMouse(flags, 0); err != nil {
		return fmt.Errorf("send mouse desktop=%s: %w", desktop.Name(), err)
	}

	return nil
}

func mouseWheelOnBoundDesktop(desktop *winsta.BoundDesktop, x, y float64, deltaX, deltaY int32) error {
	if err := mouseMoveOnBoundDesktop(desktop, x, y); err != nil {
		return err
	}

	if deltaY != 0 {
		if err := sendMouse(mouseEventFWheel, uint32(-deltaY)); err != nil {
			return fmt.Errorf("send vertical wheel desktop=%s: %w", desktop.Name(), err)
		}
	}

	if deltaX != 0 {
		if err := sendMouse(mouseEventFHWheel, uint32(deltaX)); err != nil {
			return fmt.Errorf("send horizontal wheel desktop=%s: %w", desktop.Name(), err)
		}
	}

	return nil
}

func keyOnBoundDesktop(desktop *winsta.BoundDesktop, code string, down bool) error {
	vk := vkFromBrowserCode(code)
	if vk == 0 {
		return nil
	}

	return keyVKOnBoundDesktop(desktop, vk, down)
}

func keyVKOnBoundDesktop(desktop *winsta.BoundDesktop, vk uint16, down bool) error {
	if vk == 0 {
		return nil
	}

	if err := sendKey(vk, !down); err != nil {
		if down {
			return fmt.Errorf("send key down desktop=%s: %w", desktop.Name(), err)
		}
		return fmt.Errorf("send key up desktop=%s: %w", desktop.Name(), err)
	}

	return nil
}

func sendMouse(flags uint32, data uint32) error {
	in := newMouseInput(flags, data)

	return sendInputOne("mouse", &in)
}

func sendKey(vk uint16, keyUp bool) error {
	sc, _, _ := procMapVirtualKeyW.Call(uintptr(vk), mapvkVKToVSC)

	flags := uint32(keyEventFScancode)
	if keyUp {
		flags |= keyEventFKeyUp
	}
	if isExtendedVK(vk) {
		flags |= keyEventFExtended
	}

	in := newKeyboardInput(0, uint16(sc), flags)

	return sendInputOne("key", &in)
}

func sendInputOne(kind string, in *input) error {
	size := unsafe.Sizeof(*in)

	ret, _, err := procSendInput.Call(
		1,
		uintptr(unsafe.Pointer(in)),
		size,
	)

	logger.RDAgent.Debugf(
		"SendInput result: kind=%s ret=%d cbSize=%d err=%v",
		kind,
		ret,
		size,
		err,
	)

	if ret != 1 {
		return fmt.Errorf("SendInput %s failed: ret=%d cbSize=%d err=%w", kind, ret, size, err)
	}

	return nil
}

func isExtendedVK(vk uint16) bool {
	switch vk {
	case 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x2D, 0x2E:
		return true
	case 0xA3, 0xA5:
		return true
	default:
		return false
	}
}

func vkFromBrowserCode(code string) uint16 {
	if len(code) == 4 && code[:3] == "Key" {
		return uint16(code[3])
	}
	if len(code) == 6 && code[:5] == "Digit" {
		return uint16(code[5])
	}

	switch code {
	case "Enter":
		return 0x0D
	case "Escape":
		return 0x1B
	case "Backspace":
		return 0x08
	case "Tab":
		return 0x09
	case "Space":
		return 0x20
	case "ArrowLeft":
		return 0x25
	case "ArrowUp":
		return 0x26
	case "ArrowRight":
		return 0x27
	case "ArrowDown":
		return 0x28
	case "Delete":
		return 0x2E
	case "Insert":
		return 0x2D
	case "Home":
		return 0x24
	case "End":
		return 0x23
	case "PageUp":
		return 0x21
	case "PageDown":
		return 0x22
	case "ShiftLeft", "ShiftRight":
		return 0x10
	case "ControlLeft", "ControlRight":
		return 0x11
	case "AltLeft", "AltRight":
		return 0x12
	case "MetaLeft", "MetaRight":
		return 0x5B
	case "F1":
		return 0x70
	case "F2":
		return 0x71
	case "F3":
		return 0x72
	case "F4":
		return 0x73
	case "F5":
		return 0x74
	case "F6":
		return 0x75
	case "F7":
		return 0x76
	case "F8":
		return 0x77
	case "F9":
		return 0x78
	case "F10":
		return 0x79
	case "F11":
		return 0x7A
	case "F12":
		return 0x7B
	default:
		return 0
	}
}
