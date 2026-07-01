//go:build windows

package diag

import (
	"fmt"

	"golang.org/x/sys/windows"
)

var (
	user32DPI = windows.NewLazySystemDLL("user32.dll")

	procSetProcessDpiAwarenessContext = user32DPI.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32DPI.NewProc("SetProcessDPIAware")
)

const (
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)
)

func EnableDPIAwareness() error {
	if err := user32DPI.Load(); err != nil {
		return fmt.Errorf("load user32.dll: %w", err)
	}

	if err := procSetProcessDpiAwarenessContext.Find(); err == nil {
		ret, _, callErr := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2)
		if ret != 0 {
			return nil
		}

		// Если DPI awareness уже установлен манифестом или раньше в процессе,
		// Windows может вернуть ошибку. Для нас это не fatal, но логировать полезно.
		return fmt.Errorf("SetProcessDpiAwarenessContext failed: %w", callErr)
	}

	if err := procSetProcessDPIAware.Find(); err == nil {
		ret, _, callErr := procSetProcessDPIAware.Call()
		if ret != 0 {
			return nil
		}

		return fmt.Errorf("SetProcessDPIAware failed: %w", callErr)
	}

	return fmt.Errorf("no supported DPI awareness API found")
}
