//go:build windows

package sandbox

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// ensureWindowsSandboxRuntimeDirs recreates any missing QG runtime directory
// and re-grants the write-root capability allow ACE wherever it is absent.
//
// prepareSandboxRuntime (main process) recreates the tree on every command, but
// the recreated directories inherit their parent's DACL — the capability grant
// the setup (or an earlier apply) stamped is gone with the deleted directory.
// The same gap hits the elevated tier on first use: the setup-time profile does
// not know the per-command runtime root, so it never granted it. Without this
// step isolated commands lose TEMP writes and never recover on their own.
//
// Root is healed first so the children it creates inherit a granted DACL; each
// dir is still verified individually because inheritance alone cannot be
// assumed (a dir revived from elsewhere may block it).
func ensureWindowsSandboxRuntimeDirs(config WindowsSandboxCommandConfig) error {
	grants, err := windowsSandboxRuntimeDirGrants(config)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if err := os.MkdirAll(grant.Path, 0o700); err != nil {
			return fmt.Errorf("recreate windows sandbox runtime dir %s: %w", grant.Path, err)
		}
		granted, err := windowsDACLGrantsWriteSID(grant.Path, grant.Capability)
		if err != nil {
			return err
		}
		if granted {
			continue
		}
		snapshot, applied, err := applyWindowsACLPathGroup(windowsACLPathGroup{
			Path: grant.Path,
			Entries: []WindowsACLEntry{{
				Action:     WindowsACLAllowWrite,
				Path:       grant.Path,
				Capability: grant.Capability,
			}},
		})
		if err != nil {
			return fmt.Errorf("re-grant windows sandbox runtime dir %s: %w — run `qg sandbox setup` from an elevated (Administrator) terminal", grant.Path, err)
		}
		if !applied {
			return fmt.Errorf("re-grant windows sandbox runtime dir %s did not apply — run `qg sandbox setup` from an elevated (Administrator) terminal", grant.Path)
		}
		// The rollback is deliberately discarded: like the unelevated tier's
		// workspace grants, the ACE names a synthetic capability SID no other
		// token carries, so leaving it in place cannot widen anything else.
		_ = snapshot
	}
	return nil
}

// windowsDACLGrantsWriteSID reports whether path's DACL carries an allow ACE
// for wantSID whose mask covers the write-root allow shape (explicit or
// inherited — either one grants the write).
func windowsDACLGrantsWriteSID(path, wantSID string) (bool, error) {
	want, err := windows.StringToSid(wantSID)
	if err != nil {
		return false, fmt.Errorf("parse windows capability SID %q: %w", wantSID, err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read windows ACL for %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("read windows DACL for %s: %w", path, err)
	}
	if dacl == nil {
		return false, nil
	}
	_, allowMask, err := windowsACLAccess(WindowsACLAllowWrite)
	if err != nil {
		return false, err
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return false, fmt.Errorf("read ACE %d for %s: %w", index, path, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, windowsAccessAllowedObjectAceType,
			windowsAccessAllowedCallbackAceType, windowsAccessAllowedCallbackObjectAceType:
		default:
			continue
		}
		sid, ok := windowsAceSID(ace)
		if !ok || !sid.Equals(want) {
			continue
		}
		if ace.Mask&allowMask == allowMask {
			return true, nil
		}
	}
	return false, nil
}
