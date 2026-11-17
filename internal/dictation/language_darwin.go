package dictation

import (
	"github.com/ebitengine/purego"
)

func init() { osPreferredLanguage = darwinPreferredLanguage }

// darwinPreferredLanguage reads the first entry of the user's preferred
// languages (System Settings > Language & Region), e.g. "pt-BR". "" on any
// failure.
func darwinPreferredLanguage() string {
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return ""
	}
	var (
		copyLanguages func() uintptr
		arrayCount    func(arr uintptr) int
		arrayAt       func(arr uintptr, i int) uintptr
		getCString    func(str uintptr, buf *byte, size int, encoding uint32) bool
		release       func(obj uintptr)
	)
	for name, fn := range map[string]any{
		"CFLocaleCopyPreferredLanguages": &copyLanguages,
		"CFArrayGetCount":                &arrayCount,
		"CFArrayGetValueAtIndex":         &arrayAt,
		"CFStringGetCString":             &getCString,
		"CFRelease":                      &release,
	} {
		addr, err := purego.Dlsym(cf, name)
		if err != nil {
			return ""
		}
		purego.RegisterFunc(fn, addr)
	}
	arr := copyLanguages()
	if arr == 0 {
		return ""
	}
	defer release(arr)
	if arrayCount(arr) < 1 {
		return ""
	}
	const utf8 = 0x08000100 // kCFStringEncodingUTF8
	buf := make([]byte, 64)
	if !getCString(arrayAt(arr, 0), &buf[0], len(buf), utf8) {
		return ""
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return ""
}
