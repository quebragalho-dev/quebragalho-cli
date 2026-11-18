//go:build windows

package sandbox

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Everyone must be in the restricted-SID list in BOTH token modes. Real
// programs need it (managed PowerShell/.NET fail to initialize without it, and
// the fully restricted token cannot open cmd.exe without it); the #865 hole it
// reopens for arbitrary Everyone-writable paths is compensated DACL-side by
// capability-SID deny ACEs on every plan-managed sensitive path.
func TestWindowsRestrictedTokenCarriesEveryoneInBothModes(t *testing.T) {
	capability := randomWindowsCapabilitySID()
	for _, writeRestricted := range []bool{true, false} {
		token, err := createWindowsRestrictedTokenForCapabilitySIDs([]string{capability}, writeRestricted)
		if err != nil {
			t.Fatalf("writeRestricted=%v: createWindowsRestrictedTokenForCapabilitySIDs: %v", writeRestricted, err)
		}
		sids := restrictedTokenSIDs(t, token)
		_ = token.Close()

		worldSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
		if err != nil {
			t.Fatalf("CreateWellKnownSid: %v", err)
		}
		if !sidListContains(sids, worldSID) {
			t.Errorf("writeRestricted=%v: restricted-SID list lacks Everyone (S-1-1-0); PowerShell/.NET and fully-restricted launch depend on it", writeRestricted)
		}
		capabilitySID, err := windows.StringToSid(capability)
		if err != nil {
			t.Fatalf("StringToSid capability: %v", err)
		}
		if !sidListContains(sids, capabilitySID) {
			t.Errorf("writeRestricted=%v: restricted-SID list lacks the workspace capability SID", writeRestricted)
		}
	}
}

func restrictedTokenSIDs(t *testing.T, token windows.Token) []*windows.SID {
	t.Helper()
	var size uint32
	err := windows.GetTokenInformation(token, windows.TokenRestrictedSids, nil, 0, &size)
	if err == nil || err != windows.ERROR_INSUFFICIENT_BUFFER {
		t.Fatalf("size TokenRestrictedSids: %v", err)
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenRestrictedSids, &buf[0], size, &size); err != nil {
		t.Fatalf("get TokenRestrictedSids: %v", err)
	}
	groups := (*windows.Tokengroups)(unsafe.Pointer(&buf[0]))
	out := make([]*windows.SID, 0)
	for _, group := range groups.AllGroups() {
		out = append(out, group.Sid)
	}
	runtime.KeepAlive(buf)
	return out
}

func sidListContains(sids []*windows.SID, want *windows.SID) bool {
	for _, sid := range sids {
		if sid.Equals(want) {
			return true
		}
	}
	return false
}
