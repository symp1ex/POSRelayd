package identity

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32                = windows.NewLazySystemDLL("crypt32.dll")
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

func DPAPIUnprotect(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("empty dpapi blob")
	}

	in := dataBlob{
		cbData: uint32(len(ciphertext)),
		pbData: &ciphertext[0],
	}

	var out dataBlob

	ret, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&out)),
	)

	if ret == 0 {
		return nil, fmt.Errorf("CryptUnprotectData failed: %w", err)
	}

	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))

	if out.cbData == 0 || out.pbData == nil {
		return nil, fmt.Errorf("CryptUnprotectData returned empty data")
	}

	plain := unsafe.Slice(out.pbData, out.cbData)
	result := make([]byte, len(plain))
	copy(result, plain)

	return result, nil
}
