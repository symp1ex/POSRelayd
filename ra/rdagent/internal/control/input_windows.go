//go:build windows

package control

import (
	"fmt"
	"math"
	"rdagent/internal/logger"
	"sync"
	"syscall"
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
	smCXScreen = 0
	smCYScreen = 1

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
	}
}

func (i *Injector) SetFocus(focused bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.focused = focused
	if !focused {
		i.releaseAllLocked()
	}
}

func (i *Injector) MouseMoveNormalized(x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.focused {
		return nil
	}

	px, py := normalizedToScreen(x, y)

	ret, _, err := procSetCursorPos.Call(
		uintptr(px),
		uintptr(py),
	)
	if ret == 0 {
		return fmt.Errorf("SetCursorPos failed: %w", err)
	}

	return nil
}

func (i *Injector) MouseButton(x, y float64, button string, down bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.focused {
		return nil
	}

	px, py := normalizedToScreen(x, y)
	_, _, _ = procSetCursorPos.Call(uintptr(px), uintptr(py))

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

	return sendMouse(flags, 0)
}

func (i *Injector) MouseWheel(x, y float64, deltaX, deltaY int32) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.focused {
		return nil
	}

	px, py := normalizedToScreen(x, y)
	_, _, _ = procSetCursorPos.Call(uintptr(px), uintptr(py))

	if deltaY != 0 {
		if err := sendMouse(mouseEventFWheel, uint32(-deltaY)); err != nil {
			return err
		}
	}

	if deltaX != 0 {
		if err := sendMouse(mouseEventFHWheel, uint32(deltaX)); err != nil {
			return err
		}
	}

	return nil
}

func (i *Injector) Key(code string, down bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.focused {
		return nil
	}

	vk := vkFromBrowserCode(code)
	if vk == 0 {
		return nil
	}

	if down {
		if i.downKeys[vk] {
			return nil
		}
		i.downKeys[vk] = true
		return sendKey(vk, false)
	}

	delete(i.downKeys, vk)
	return sendKey(vk, true)
}

func (i *Injector) ReleaseAll() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.releaseAllLocked()
}

func (i *Injector) releaseAllLocked() {
	for vk := range i.downKeys {
		_ = sendKey(vk, true)
		delete(i.downKeys, vk)
	}
}

func normalizedToScreen(x, y float64) (int, int) {
	w, _, _ := procGetSystemMetrics.Call(smCXScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYScreen)

	if w == 0 {
		w = 1
	}
	if h == 0 {
		h = 1
	}

	x = math.Max(0, math.Min(1, x))
	y = math.Max(0, math.Min(1, y))

	px := int(math.Round(x * float64(int(w)-1)))
	py := int(math.Round(y * float64(int(h)-1)))

	return px, py
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
