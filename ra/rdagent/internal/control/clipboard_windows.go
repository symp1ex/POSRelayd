//go:build windows

package control

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"syscall"
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

	procGlobalAlloc  = kernel32cb.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32cb.NewProc("GlobalLock")
	procGlobalUnlock = kernel32cb.NewProc("GlobalUnlock")
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

func Revision(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func setClipboardText(text string) error {
	if ret, _, err := procOpenClipboard.Call(0); ret == 0 {
		return fmt.Errorf("OpenClipboard failed: %w", err)
	}
	defer procCloseClipboard.Call()

	if ret, _, err := procEmptyClipboard.Call(); ret == 0 {
		return fmt.Errorf("EmptyClipboard failed: %w", err)
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
		return fmt.Errorf("GlobalLock failed: %w", err)
	}

	copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16)), utf16)
	procGlobalUnlock.Call(hMem)

	if ret, _, err := procSetClipboardData.Call(cfUnicodeText, hMem); ret == 0 {
		return fmt.Errorf("SetClipboardData failed: %w", err)
	}

	return nil
}

func getClipboardText() (string, error) {
	if ret, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); ret == 0 {
		return "", nil
	}

	if ret, _, err := procOpenClipboard.Call(0); ret == 0 {
		return "", fmt.Errorf("OpenClipboard failed: %w", err)
	}
	defer procCloseClipboard.Call()

	h, _, err := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", fmt.Errorf("GetClipboardData failed: %w", err)
	}

	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock failed: %w", err)
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
