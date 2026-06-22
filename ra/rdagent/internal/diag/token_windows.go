// internal/diag/token_windows.go
package diag

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	TokenUser         = 1
	TokenElevation    = 20
	TokenIntegrityLvl = 25
	TokenSessionID    = 12

	SECURITY_MANDATORY_LOW_RID    = 0x1000
	SECURITY_MANDATORY_MEDIUM_RID = 0x2000
	SECURITY_MANDATORY_HIGH_RID   = 0x3000
	SECURITY_MANDATORY_SYSTEM_RID = 0x4000
)

type tokenElevation struct {
	TokenIsElevated uint32
}

type sidAndAttributes struct {
	Sid        *windows.SID
	Attributes uint32
}

var (
	advapi32                      = windows.NewLazySystemDLL("advapi32.dll")
	procGetTokenInformation       = advapi32.NewProc("GetTokenInformation")
	procConvertSidToStringSidW    = advapi32.NewProc("ConvertSidToStringSidW")
	procGetLengthSid              = advapi32.NewProc("GetLengthSid")
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procWTSGetActiveConsoleSessID = kernel32.NewProc("WTSGetActiveConsoleSessionId")
)

func tokenInfoClass(token windows.Token, class uint32) ([]byte, error) {
	var needed uint32

	r1, _, e1 := procGetTokenInformation.Call(
		uintptr(token),
		uintptr(class),
		0,
		0,
		uintptr(unsafe.Pointer(&needed)),
	)

	if r1 == 0 && needed == 0 {
		return nil, e1
	}

	buf := make([]byte, needed)

	r1, _, e1 = procGetTokenInformation.Call(
		uintptr(token),
		uintptr(class),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&needed)),
	)

	if r1 == 0 {
		return nil, e1
	}

	return buf, nil
}

func sidToString(sid *windows.SID) string {
	var str *uint16

	r1, _, _ := procConvertSidToStringSidW.Call(
		uintptr(unsafe.Pointer(sid)),
		uintptr(unsafe.Pointer(&str)),
	)

	if r1 == 0 || str == nil {
		return ""
	}

	defer windows.LocalFree(windows.Handle(unsafe.Pointer(str)))

	return windows.UTF16PtrToString(str)
}

func integrityName(rid uint32) string {
	switch {
	case rid >= SECURITY_MANDATORY_SYSTEM_RID:
		return "System"
	case rid >= SECURITY_MANDATORY_HIGH_RID:
		return "High"
	case rid >= SECURITY_MANDATORY_MEDIUM_RID:
		return "Medium"
	case rid >= SECURITY_MANDATORY_LOW_RID:
		return "Low"
	default:
		return fmt.Sprintf("Unknown(0x%x)", rid)
	}
}

func currentIntegrityLevel(token windows.Token) (string, uint32, error) {
	buf, err := tokenInfoClass(token, TokenIntegrityLvl)
	if err != nil {
		return "", 0, err
	}

	saa := (*sidAndAttributes)(unsafe.Pointer(&buf[0]))
	sid := saa.Sid

	subAuthCount := *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(sid)) + 1))
	if subAuthCount == 0 {
		return "", 0, fmt.Errorf("integrity SID has no subauthorities")
	}

	// SID layout: Revision(1), SubAuthCount(1), IdentifierAuthority(6), SubAuthority[N] uint32
	lastSubAuthOffset := uintptr(8 + int(subAuthCount-1)*4)
	rid := *(*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(sid)) + lastSubAuthOffset))

	return integrityName(rid), rid, nil
}

func CurrentTokenReport() string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var token windows.Token

	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_QUERY,
		&token,
	)
	if err != nil {
		return fmt.Sprintf("token: OpenProcessToken failed: %v", err)
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	userSID := ""
	if err == nil && user != nil && user.User.Sid != nil {
		userSID = user.User.Sid.String()
	}

	var sessionID uint32
	if buf, err := tokenInfoClass(token, TokenSessionID); err == nil && len(buf) >= 4 {
		sessionID = *(*uint32)(unsafe.Pointer(&buf[0]))
	}

	isElevated := uint32(0)
	if buf, err := tokenInfoClass(token, TokenElevation); err == nil && len(buf) >= 4 {
		elev := (*tokenElevation)(unsafe.Pointer(&buf[0]))
		isElevated = elev.TokenIsElevated
	}

	integrity, integrityRID, integrityErr := currentIntegrityLevel(token)
	if integrityErr != nil {
		integrity = "error: " + integrityErr.Error()
	}

	activeSession, _, _ := procWTSGetActiveConsoleSessID.Call()

	return fmt.Sprintf(
		"token diagnostics: pid=%d user_sid=%s token_session_id=%d active_console_session_id=%d elevated=%t integrity=%s integrity_rid=0x%x",
		windows.GetCurrentProcessId(),
		userSID,
		sessionID,
		uint32(activeSession),
		isElevated != 0,
		integrity,
		integrityRID,
	)
}
