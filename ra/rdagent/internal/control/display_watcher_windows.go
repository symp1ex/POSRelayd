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

	"rdagent/internal/logger"
	"rdagent/internal/winsta"
)

const (
	wmDisplayChange       = 0x007E
	wmCloseDisplayWatcher = 0x8002
)

type DisplayWatcher struct {
	onChange func()

	started atomic.Bool

	mu   sync.Mutex
	hwnd uintptr
	done chan struct{}
}

var (
	displayWatcherProcOnce sync.Once
	displayWatcherProc     uintptr
	displayWatcherByHwnd   sync.Map
)

func NewDisplayWatcher(onChange func()) *DisplayWatcher {
	return &DisplayWatcher{
		onChange: onChange,
	}
}

func (w *DisplayWatcher) Start() error {
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

func (w *DisplayWatcher) Stop() {
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
		_, _, _ = procPostMessageW.Call(hwnd, wmCloseDisplayWatcher, 0, 0)
	}

	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			logger.RDAgent.Warn("Display watcher stop timed out")
		}
	}
}

func (w *DisplayWatcher) run(done chan struct{}) {
	defer close(done)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	desktop, err := winsta.BindCurrentThreadToInputDesktop("display-watcher")
	if err != nil {
		logger.RDAgent.Warnf("Display watcher bind desktop failed: %v", err)
		return
	}
	defer desktop.Close()

	hwnd, err := createDisplayWatcherWindow(w)
	if err != nil {
		logger.RDAgent.Warnf("Display watcher window create failed: %v", err)
		return
	}

	w.mu.Lock()
	w.hwnd = hwnd
	w.mu.Unlock()

	defer func() {
		displayWatcherByHwnd.Delete(hwnd)
		procDestroyWindow.Call(hwnd)

		w.mu.Lock()
		if w.hwnd == hwnd {
			w.hwnd = 0
		}
		w.mu.Unlock()
	}()

	RefreshInputGeometry()

	logger.RDAgent.Infof("Display watcher started on desktop=%s", desktop.Name())

	var m msg
	for {
		ret, _, callErr := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&m)),
			0,
			0,
			0,
		)

		if int32(ret) == -1 {
			logger.RDAgent.Warnf("Display watcher GetMessage failed: %v", callErr)
			return
		}
		if ret == 0 {
			return
		}

		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func createDisplayWatcherWindow(w *DisplayWatcher) (uintptr, error) {
	displayWatcherProcOnce.Do(func() {
		displayWatcherProc = syscall.NewCallback(displayWatcherWndProc)
	})

	className, err := syscall.UTF16PtrFromString("rdagent-display-watcher")
	if err != nil {
		return 0, err
	}

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   displayWatcherProc,
		LpszClassName: className,
	}

	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	windowName, err := syscall.UTF16PtrFromString("rdagent-display-watcher")
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
		return 0, fmt.Errorf("CreateWindowExW display watcher failed: %w", callErr)
	}

	displayWatcherByHwnd.Store(hwnd, w)
	return hwnd, nil
}

func displayWatcherWndProc(hwnd uintptr, message uint32, wParam uintptr, lParam uintptr) uintptr {
	switch message {
	case wmDisplayChange:
		if raw, ok := displayWatcherByHwnd.Load(hwnd); ok {
			w := raw.(*DisplayWatcher)
			if w.onChange != nil {
				w.onChange()
			}
		}
		return 0

	case wmCloseDisplayWatcher:
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}
