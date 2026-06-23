//go:build windows

package control

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"rdagent/internal/logger"
	"rdagent/internal/winsta"
)

const (
	wmClipboardUpdate = 0x031D
	wmClose           = 0x0010
)

type watchPoint struct {
	X int32
	Y int32
}

type watchMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      watchPoint
}

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

var (
	user32watch = windows.NewLazySystemDLL("user32.dll")
	kernel32w   = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW           = user32watch.NewProc("RegisterClassExW")
	procCreateWindowExWWatcher     = user32watch.NewProc("CreateWindowExW")
	procDestroyWindowWatcher       = user32watch.NewProc("DestroyWindow")
	procDefWindowProcWWatcher      = user32watch.NewProc("DefWindowProcW")
	procGetMessageWWatcher         = user32watch.NewProc("GetMessageW")
	procTranslateMessageWatcher    = user32watch.NewProc("TranslateMessage")
	procDispatchMessageWWatcher    = user32watch.NewProc("DispatchMessageW")
	procPostMessageWWatcher        = user32watch.NewProc("PostMessageW")
	procPostQuitMessageWatcher     = user32watch.NewProc("PostQuitMessage")
	procAddClipboardFormatListener = user32watch.NewProc("AddClipboardFormatListener")
	procRemoveClipboardListener    = user32watch.NewProc("RemoveClipboardFormatListener")
	procGetModuleHandleW           = kernel32w.NewProc("GetModuleHandleW")

	clipboardWatchers sync.Map // hwnd -> *ClipboardWatcher
)

type ClipboardWatcher struct {
	mu       sync.Mutex
	hwnd     uintptr
	done     chan struct{}
	onChange func()
}

func NewClipboardWatcher(onChange func()) *ClipboardWatcher {
	return &ClipboardWatcher{onChange: onChange}
}

func (w *ClipboardWatcher) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.done != nil {
		return nil
	}

	w.done = make(chan struct{})
	go w.loop()

	return nil
}

func (w *ClipboardWatcher) Stop() {
	w.mu.Lock()
	hwnd := w.hwnd
	done := w.done
	w.hwnd = 0
	w.done = nil
	w.mu.Unlock()

	if hwnd != 0 {
		_, _, _ = procPostMessageWWatcher.Call(hwnd, wmClose, 0, 0)
	}

	if done != nil {
		<-done
	}
}

func (w *ClipboardWatcher) loop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	done := func() chan struct{} {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.done
	}()

	defer func() {
		if done != nil {
			close(done)
		}
	}()

	desktop, err := bindWatcherThreadToInputDesktop()
	if err != nil {
		logger.RDAgent.Warnf("Clipboard watcher bind input desktop failed: %v", err)
		return
	}
	defer desktop.Close()

	logger.RDAgent.Infof("Clipboard watcher bound to desktop: %s", desktop.Name())

	if err := w.runMessageLoopOnCurrentDesktop(); err != nil {
		logger.RDAgent.Warnf("Clipboard watcher stopped: %v", err)
		return
	}
}

func bindWatcherThreadToInputDesktop() (*winsta.BoundDesktop, error) {
	var lastErr error

	for attempt := 0; attempt < 20; attempt++ {
		desktop, err := winsta.BindCurrentThreadToInputDesktop("clipboard-watch")
		if err == nil {
			return desktop, nil
		}

		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}

	return nil, fmt.Errorf("bind input desktop failed after retries: %w", lastErr)
}

func (w *ClipboardWatcher) runMessageLoopOnCurrentDesktop() error {
	className, err := syscall.UTF16PtrFromString("RDAgentClipboardWatcher")
	if err != nil {
		return err
	}

	windowName, err := syscall.UTF16PtrFromString("RDAgentClipboardWatcher")
	if err != nil {
		return err
	}

	hInst, _, _ := procGetModuleHandleW.Call(0)

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   syscall.NewCallback(clipboardWatcherWndProc),
		HInstance:     hInst,
		LpszClassName: className,
	}

	if ret, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		// Если класс уже зарегистрирован — это не критично.
		logger.RDAgent.Debugf("RegisterClassExW returned 0: %v", callErr)
	}

	hwnd, _, callErr := procCreateWindowExWWatcher.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0,
		0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW watcher failed: %w", callErr)
	}

	w.mu.Lock()
	w.hwnd = hwnd
	w.mu.Unlock()

	clipboardWatchers.Store(hwnd, w)
	defer clipboardWatchers.Delete(hwnd)

	defer func() {
		w.mu.Lock()
		if w.hwnd == hwnd {
			w.hwnd = 0
		}
		w.mu.Unlock()
	}()

	if ret, _, callErr := procAddClipboardFormatListener.Call(hwnd); ret == 0 {
		_, _, _ = procDestroyWindowWatcher.Call(hwnd)
		return fmt.Errorf("AddClipboardFormatListener failed: %w", callErr)
	}

	logger.RDAgent.Infof("Clipboard watcher window created: hwnd=%d", hwnd)

	defer procRemoveClipboardListener.Call(hwnd)
	defer procDestroyWindowWatcher.Call(hwnd)

	var msg watchMsg

	for {
		ret, _, callErr := procGetMessageWWatcher.Call(
			uintptr(unsafe.Pointer(&msg)),
			0,
			0,
			0,
		)

		if int32(ret) == -1 {
			return fmt.Errorf("GetMessageW failed: %w", callErr)
		}

		if int32(ret) == 0 {
			return nil
		}

		_, _, _ = procTranslateMessageWatcher.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageWWatcher.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func clipboardWatcherWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClipboardUpdate:
		if raw, ok := clipboardWatchers.Load(hwnd); ok {
			w := raw.(*ClipboardWatcher)

			logger.RDAgent.Debug("WM_CLIPBOARDUPDATE received")

			if w.onChange != nil {
				go w.onChange()
			}
		}
		return 0

	case wmClose:
		_, _, _ = procPostQuitMessageWatcher.Call(0)
		return 0

	default:
		ret, _, _ := procDefWindowProcWWatcher.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	}
}
