//go:build windows

package control

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32cb                       = windows.NewLazySystemDLL("user32.dll")
	kernel32cb                     = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard              = user32cb.NewProc("OpenClipboard")
	procCloseClipboard             = user32cb.NewProc("CloseClipboard")
	procEmptyClipboard             = user32cb.NewProc("EmptyClipboard")
	procSetClipboardData           = user32cb.NewProc("SetClipboardData")
	procGetClipboardData           = user32cb.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = user32cb.NewProc("IsClipboardFormatAvailable")
	procCreateWindowExW            = user32cb.NewProc("CreateWindowExW")
	procDestroyWindow              = user32cb.NewProc("DestroyWindow")

	procGlobalAlloc  = kernel32cb.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32cb.NewProc("GlobalLock")
	procGlobalUnlock = kernel32cb.NewProc("GlobalUnlock")
	procGlobalFree   = kernel32cb.NewProc("GlobalFree")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

type Clipboard struct {
	mu           sync.Mutex
	lastRevision string
	lastOrigin   string
	lastSeq      uint64

	lastHostSetRevision string
	suppressUntil       time.Time
}

func createClipboardOwnerWindow() (uintptr, error) {
	className, err := syscall.UTF16PtrFromString("STATIC")
	if err != nil {
		return 0, err
	}

	windowName, err := syscall.UTF16PtrFromString("rdagent-clipboard-owner")
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
		return 0, fmt.Errorf("CreateWindowExW failed: %w", callErr)
	}

	return hwnd, nil
}

func openClipboardWithRetry(owner uintptr) error {
	var lastErr error

	for attempt := 0; attempt < 20; attempt++ {
		if ret, _, err := procOpenClipboard.Call(owner); ret != 0 {
			return nil
		} else {
			lastErr = err
		}

		time.Sleep(10 * time.Millisecond)
	}

	return fmt.Errorf("OpenClipboard failed after retries: %w", lastErr)
}

func NewClipboard() *Clipboard {
	return &Clipboard{}
}

func (c *Clipboard) SetText(text, origin string, seq uint64, revision string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if revision == "" {
		revision = Revision(text)
	}

	if c.lastOrigin == origin && c.lastSeq >= seq {
		return false, nil
	}

	if c.lastRevision == revision {
		c.lastOrigin = origin
		c.lastSeq = seq
		return false, nil
	}

	if err := setClipboardText(text); err != nil {
		return false, err
	}

	c.lastRevision = revision
	c.lastOrigin = origin
	c.lastSeq = seq
	c.lastHostSetRevision = revision
	c.suppressUntil = time.Now().Add(350 * time.Millisecond)

	return true, nil
}

func (c *Clipboard) GetText() (string, string, error) {
	text, err := getClipboardText()
	if err != nil {
		return "", "", err
	}

	revision := Revision(text)

	c.mu.Lock()
	c.lastRevision = revision
	c.mu.Unlock()

	return text, revision, nil
}

func (c *Clipboard) GetTextIfChanged() (string, string, bool, error) {
	text, err := getClipboardText()
	if err != nil {
		return "", "", false, err
	}

	revision := Revision(text)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastRevision == revision {
		return "", revision, false, nil
	}

	c.lastRevision = revision
	c.lastOrigin = ""
	c.lastSeq = 0

	return text, revision, true, nil
}

func (c *Clipboard) GetTextIfUserChanged() (string, string, bool, error) {
	text, err := getClipboardText()
	if err != nil {
		return "", "", false, err
	}

	revision := Revision(text)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastRevision == revision {
		return "", revision, false, nil
	}

	if c.lastHostSetRevision == revision {
		c.lastRevision = revision
		return "", revision, false, nil
	}

	if time.Now().Before(c.suppressUntil) {
		c.lastRevision = revision
		return "", revision, false, nil
	}

	c.lastRevision = revision
	c.lastOrigin = ""
	c.lastSeq = 0

	return text, revision, true, nil
}

func Revision(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func setClipboardText(text string) error {
	desktop, err := bindInputDesktop("clipboard-set")
	if err != nil {
		return fmt.Errorf("clipboard set bind desktop: %w", err)
	}
	defer desktop.Close()

	owner, err := createClipboardOwnerWindow()
	if err != nil {
		return fmt.Errorf("create clipboard owner window desktop=%s: %w", desktop.Name(), err)
	}
	defer procDestroyWindow.Call(owner)

	if err := openClipboardWithRetry(owner); err != nil {
		return err
	}
	defer procCloseClipboard.Call()

	if ret, _, err := procEmptyClipboard.Call(); ret == 0 {
		return fmt.Errorf("EmptyClipboard desktop=%s failed: %w", desktop.Name(), err)
	}

	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}

	size := uintptr(len(utf16) * 2)

	hMem, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc failed: %w", err)
	}

	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("GlobalLock failed: %w", err)
	}

	copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16)), utf16)
	procGlobalUnlock.Call(hMem)

	if ret, _, err := procSetClipboardData.Call(cfUnicodeText, hMem); ret == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("SetClipboardData desktop=%s failed: %w", desktop.Name(), err)
	}

	return nil
}

func getClipboardText() (string, error) {
	desktop, err := bindInputDesktop("clipboard-get")
	if err != nil {
		return "", fmt.Errorf("clipboard get bind desktop: %w", err)
	}
	defer desktop.Close()

	if ret, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); ret == 0 {
		return "", nil
	}

	if err := openClipboardWithRetry(0); err != nil {
		return "", err
	}

	defer procCloseClipboard.Call()

	h, _, err := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", fmt.Errorf("GetClipboardData desktop=%s failed: %w", desktop.Name(), err)
	}

	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock desktop=%s failed: %w", desktop.Name(), err)
	}
	defer procGlobalUnlock.Call(h)

	var chars []uint16
	for p := ptr; ; p += 2 {
		ch := *(*uint16)(unsafe.Pointer(p))
		if ch == 0 {
			break
		}
		chars = append(chars, ch)
	}

	return strings.TrimRight(syscall.UTF16ToString(chars), "\x00"), nil
}
