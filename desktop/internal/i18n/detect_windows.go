//go:build windows

package i18n

import (
	"os"
	"syscall"
	"unsafe"
)

// localeNameMaxLength is LOCALE_NAME_MAX_LENGTH from winnls.h.
const localeNameMaxLength = 85

// osLocale resolves the current user's locale via kernel32!GetUserDefaultLocaleName,
// which returns a BCP-47 name like "zh-CN" or "en-US". Falls back to the LANG
// env var if the syscall is unavailable or fails.
func osLocale() string {
	mod := syscall.NewLazyDLL("kernel32.dll")
	proc := mod.NewProc("GetUserDefaultLocaleName")
	if err := proc.Find(); err != nil {
		return os.Getenv("LANG")
	}
	buf := make([]uint16, localeNameMaxLength)
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return os.Getenv("LANG")
	}
	return syscall.UTF16ToString(buf)
}
