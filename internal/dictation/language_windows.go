//go:build windows

package dictation

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procGetUserDefaultLocaleName wraps kernel32's GetUserDefaultLocaleName (not
// exposed by x/sys/windows itself): it returns names like "pt-BR".
var procGetUserDefaultLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

// userLocaleName reports the user's default locale name. A variable (not a
// direct call) so tests can inject values without depending on the machine.
var userLocaleName func() (string, error) = kernelUserLocaleName

func kernelUserLocaleName() (string, error) {
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	r1, _, e1 := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r1 == 0 {
		if e1 != nil {
			return "", e1
		}
		return "", errors.New("GetUserDefaultLocaleName failed")
	}
	return windows.UTF16ToString(buf), nil
}

// SystemLanguage reports the OS language as an ISO-639-1 code, from the user's
// default locale name. "" when it cannot be determined.
func SystemLanguage() string {
	name, err := userLocaleName()
	if err != nil {
		return ""
	}
	return LanguageFromLocale(name)
}
