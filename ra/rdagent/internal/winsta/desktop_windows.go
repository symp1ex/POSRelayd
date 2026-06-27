//go:build windows

package winsta

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procOpenInputDesktop         = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop         = user32.NewProc("SetThreadDesktop")
	procCloseDesktop             = user32.NewProc("CloseDesktop")
	procGetUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
)

const (
	desktopReadObjects   = 0x0001
	desktopCreateWindow  = 0x0002
	desktopCreateMenu    = 0x0004
	desktopHookControl   = 0x0008
	desktopJournalRecord = 0x0010
	desktopJournalPlaybk = 0x0020
	desktopEnumerate     = 0x0040
	desktopWriteObjects  = 0x0080
	desktopSwitchDesktop = 0x0100

	dfAllowOtherAccountHook = 0x0001

	uoiName = 2
)

const desktopAllAccess = desktopReadObjects |
	desktopCreateWindow |
	desktopCreateMenu |
	desktopHookControl |
	desktopJournalRecord |
	desktopJournalPlaybk |
	desktopEnumerate |
	desktopWriteObjects |
	desktopSwitchDesktop

type BoundDesktop struct {
	handle uintptr
	name   string
}

func BindCurrentThreadToInputDesktop(reason string) (*BoundDesktop, error) {
	runtime.LockOSThread()

	hDesk, _, err := procOpenInputDesktop.Call(
		dfAllowOtherAccountHook,
		0,
		desktopAllAccess,
	)
	if hDesk == 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("OpenInputDesktop failed: %w", err)
	}

	name := desktopName(hDesk)

	ret, _, setErr := procSetThreadDesktop.Call(hDesk)
	if ret == 0 {
		procCloseDesktop.Call(hDesk)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("SetThreadDesktop name=%s failed: %w", name, setErr)
	}

	return &BoundDesktop{
		handle: hDesk,
		name:   name,
	}, nil
}

func RebindCurrentThreadToInputDesktop(reason string, previous *BoundDesktop) (*BoundDesktop, error) {
	_ = reason

	hDesk, _, err := procOpenInputDesktop.Call(
		dfAllowOtherAccountHook,
		0,
		desktopAllAccess,
	)
	if hDesk == 0 {
		return nil, fmt.Errorf("OpenInputDesktop failed: %w", err)
	}

	name := desktopName(hDesk)

	ret, _, setErr := procSetThreadDesktop.Call(hDesk)
	if ret == 0 {
		procCloseDesktop.Call(hDesk)
		return nil, fmt.Errorf("SetThreadDesktop name=%s failed: %w", name, setErr)
	}

	if previous != nil {
		previous.CloseHandleOnly()
	}

	return &BoundDesktop{
		handle: hDesk,
		name:   name,
	}, nil
}

func (d *BoundDesktop) Close() {
	if d == nil {
		return
	}

	d.CloseHandleOnly()

	runtime.UnlockOSThread()
}

// CloseHandleOnly закрывает HDESK, но не делает runtime.UnlockOSThread.
// Нужен для rebind внутри уже pinned worker-thread.
func (d *BoundDesktop) CloseHandleOnly() {
	if d == nil {
		return
	}

	if d.handle != 0 {
		procCloseDesktop.Call(d.handle)
		d.handle = 0
	}
}

func (d *BoundDesktop) Name() string {
	if d == nil {
		return ""
	}
	return d.name
}

func (d *BoundDesktop) FullName() string {
	if d == nil || d.name == "" {
		return "WinSta0\\Default"
	}

	return "WinSta0\\" + d.name
}

func desktopName(hDesk uintptr) string {
	var needed uint32

	procGetUserObjectInformation.Call(
		hDesk,
		uoiName,
		0,
		0,
		uintptr(unsafe.Pointer(&needed)),
	)

	if needed == 0 {
		return "unknown"
	}

	buf := make([]uint16, needed/2)

	ret, _, _ := procGetUserObjectInformation.Call(
		hDesk,
		uoiName,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(needed),
		uintptr(unsafe.Pointer(&needed)),
	)

	if ret == 0 {
		return "unknown"
	}

	return syscall.UTF16ToString(buf)
}

func CurrentInputDesktopName() (string, error) {
	hDesk, _, err := procOpenInputDesktop.Call(
		dfAllowOtherAccountHook,
		0,
		desktopReadObjects|desktopEnumerate,
	)
	if hDesk == 0 {
		return "", fmt.Errorf("OpenInputDesktop for name failed: %w", err)
	}
	defer procCloseDesktop.Call(hDesk)

	return desktopName(hDesk), nil
}
