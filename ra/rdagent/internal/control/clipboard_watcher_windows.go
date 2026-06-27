//go:build windows

package control

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"rdagent/internal/logger"
	"rdagent/internal/winsta"
)

var (
	user32watcher = windows.NewLazySystemDLL("user32.dll")

	procRegisterClassExW            = user32watcher.NewProc("RegisterClassExW")
	procDefWindowProcW              = user32watcher.NewProc("DefWindowProcW")
	procAddClipboardFormatListener  = user32watcher.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListen = user32watcher.NewProc("RemoveClipboardFormatListener")
	procGetMessageW                 = user32watcher.NewProc("GetMessageW")
	procTranslateMessage            = user32watcher.NewProc("TranslateMessage")
	procDispatchMessageW            = user32watcher.NewProc("DispatchMessageW")
	procPostMessageW                = user32watcher.NewProc("PostMessageW")
)

const (
	wmClipboardUpdate = 0x031D
	wmCloseWatcher    = 0x8001
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct {
		X int32
		Y int32
	}
}

type ClipboardWatcher struct {
	onChange func()

	started atomic.Bool

	mu   sync.Mutex
	hwnd uintptr
	done chan struct{}
}

var (
	clipboardWatcherProcOnce sync.Once
	clipboardWatcherProc     uintptr
	clipboardWatcherByHwnd   sync.Map
)

func NewClipboardWatcher(onChange func()) *ClipboardWatcher {
	return &ClipboardWatcher{
		onChange: onChange,
	}
}

func (w *ClipboardWatcher) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started.Load() {
		return nil
	}

	w.done = make(chan struct{})

	go w.run(w.done)

	w.started.Store(true)
	return nil
}

func (w *ClipboardWatcher) Stop() {
	w.mu.Lock()
	if !w.started.Load() {
		w.mu.Unlock()
		return
	}

	hwnd := w.hwnd
	done := w.done
	w.started.Store(false)
	w.mu.Unlock()

	if hwnd != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, wmCloseWatcher, 0, 0)
	}

	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			logger.RDAgent.Warn("Clipboard watcher stop timed out")
		}
	}
}

func (w *ClipboardWatcher) run(done chan struct{}) {
	defer close(done)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	desktop, err := winsta.BindCurrentThreadToInputDesktop("clipboard-watcher")
	if err != nil {
		logger.RDAgent.Warnf("Clipboard watcher bind desktop failed: %v", err)
		return
	}
	defer desktop.Close()

	hwnd, err := createClipboardWatcherWindow(w)
	if err != nil {
		logger.RDAgent.Warnf("Clipboard watcher window create failed: %v", err)
		return
	}

	w.mu.Lock()
	w.hwnd = hwnd
	w.mu.Unlock()

	defer func() {
		_, _, _ = procRemoveClipboardFormatListen.Call(hwnd)
		clipboardWatcherByHwnd.Delete(hwnd)
		procDestroyWindow.Call(hwnd)

		w.mu.Lock()
		if w.hwnd == hwnd {
			w.hwnd = 0
		}
		w.mu.Unlock()
	}()

	if ret, _, callErr := procAddClipboardFormatListener.Call(hwnd); ret == 0 {
		logger.RDAgent.Warnf("AddClipboardFormatListener failed: %v", callErr)
		return
	}

	logger.RDAgent.Infof("Clipboard watcher started on desktop=%s", desktop.Name())

	var m msg
	for {
		ret, _, callErr := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&m)),
			0,
			0,
			0,
		)

		if int32(ret) == -1 {
			logger.RDAgent.Warnf("Clipboard watcher GetMessage failed: %v", callErr)
			return
		}
		if ret == 0 {
			return
		}

		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func createClipboardWatcherWindow(w *ClipboardWatcher) (uintptr, error) {
	clipboardWatcherProcOnce.Do(func() {
		clipboardWatcherProc = syscall.NewCallback(clipboardWatcherWndProc)
	})

	className, err := syscall.UTF16PtrFromString("rdagent-clipboard-watcher")
	if err != nil {
		return 0, err
	}

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   clipboardWatcherProc,
		LpszClassName: className,
	}

	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	windowName, err := syscall.UTF16PtrFromString("rdagent-clipboard-watcher")
	if err != nil {
		return 0, err
	}

	hwnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW watcher failed: %w", callErr)
	}

	clipboardWatcherByHwnd.Store(hwnd, w)
	return hwnd, nil
}

func clipboardWatcherWndProc(hwnd uintptr, message uint32, wParam uintptr, lParam uintptr) uintptr {
	switch message {
	case wmClipboardUpdate:
		if raw, ok := clipboardWatcherByHwnd.Load(hwnd); ok {
			w := raw.(*ClipboardWatcher)
			if w.onChange != nil {
				go w.onChange()
			}
		}
		return 0

	case wmCloseWatcher:
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}
