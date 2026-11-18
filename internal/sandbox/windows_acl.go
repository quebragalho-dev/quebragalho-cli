package sandbox

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type WindowsACLAction string

const (
	WindowsACLAllowWrite WindowsACLAction = "allow-write"
	// WindowsACLAllowRead grants a capability SID read access on a path. It is
	// load-bearing only for the fully restricted token (DenyRead profiles),
	// whose restricted-SID check applies to reads; under WRITE_RESTRICTED reads
	// already pass via the user identity, so the extra ACE changes nothing.
	WindowsACLAllowRead WindowsACLAction = "allow-read"
	WindowsACLDenyRead  WindowsACLAction = "deny-read"
	WindowsACLDenyWrite WindowsACLAction = "deny-write"
	// WindowsACLRevokeCapability removes any existing ACE (allow or deny) for
	// Capability at Path, without itself granting or denying anything (applied
	// via SetEntriesInAclW's SET_ACCESS mode with a qg mask, not
	// REVOKE_ACCESS — see windowsACLAccess for why). It is consumed by
	// applyWindowsACLPlan for migration cleanup and tests; no current
	// plan-generation path emits this action, preserving legacy-process confinement.
	WindowsACLRevokeCapability WindowsACLAction = "revoke-capability"
)

type WindowsACLEntry struct {
	Action     WindowsACLAction `json:"action"`
	Path       string           `json:"path"`
	Capability string           `json:"capability"`
	// NoInherit forces the applied ACE to carry no inheritance flags, even
	// when the target is a directory. Without it, applyWindowsACLPlan makes
	// every directory ACE inheritable (SUB_CONTAINERS_AND_OBJECTS_INHERIT),
	// and SetNamedSecurityInfo automatically propagates any inheritable ACE
	// down onto the target's EXISTING descendants (not just new ones it
	// creates going forward), which is why direct-only denies must set this
	// flag rather than rely on inheritance.
	NoInherit bool `json:"noInherit,omitempty"`
	// Anchor is the write root Path was DERIVED from, for the paths this
	// package constructs rather than the operator naming. The apply requires
	// the object it finally opens to still live under it, so a reparse point
	// planted on the derived tail cannot walk an elevated ACL write out of the
	// sandbox. Empty for an operator-named path, which has no owned tail.
	Anchor      string `json:"anchor,omitempty"`
	Materialize bool   `json:"materialize,omitempty"`
}

type WindowsACLPlan struct {
	Entries []WindowsACLEntry `json:"entries"`
}

func BuildWindowsACLPlan(config WindowsSandboxCommandConfig) (WindowsACLPlan, error) {
	if config.PermissionProfile.FileSystem.Kind != FileSystemRestricted {
		return WindowsACLPlan{}, errors.New("windows ACL setup requires a restricted filesystem permission profile")
	}
	writeCapabilities, err := windowsWriteRootCapabilities(config)
	if err != nil {
		return WindowsACLPlan{}, err
	}
	var entries []WindowsACLEntry
	for _, capability := range writeCapabilities {
		entries = append(entries, []WindowsACLEntry{
			{
				Action:     WindowsACLAllowWrite,
				Path:       capability.Root,
				Capability: capability.SID,
			},
			// The fully restricted token (DenyRead profiles) checks reads
			// against the restricted list too, so the workspace needs an
			// explicit read grant or nothing inside it opens. Inert under
			// WRITE_RESTRICTED, where reads pass via the user identity.
			{
				Action:     WindowsACLAllowRead,
				Path:       capability.Root,
				Capability: capability.SID,
			},
		}...)
		for _, path := range capability.MaterializedWriteDenyPaths {
			// Protected metadata (.qg, .agents) denies materialize: a deny ACE
			// cannot attach to a path that does not exist, and the apply skips
			// missing non-materialized targets, so without this an isolated
			// command creates the directory itself and writes inside it.
			// Operator-named subpaths below deliberately keep the old
			// behaviour: creating arbitrary operator paths during (possibly
			// elevated) setup would be host mutation, not confinement.
			entries = append(entries, WindowsACLEntry{
				Action:      WindowsACLDenyWrite,
				Path:        path,
				Capability:  capability.SID,
				Anchor:      capability.Root,
				Materialize: true,
			})
		}
		for _, path := range capability.ProtectedWriteDenyPaths {
			// ANCHORED ONLY WHERE THE ANCHOR IS TRUE. Most of these are derived
			// from the root and sit under it, and holding those inside it is the
			// point. But ReadOnlySubpaths is a profile field an operator can set to
			// any path, and one deliberately placed outside the root is a
			// configuration that works today; anchoring it would turn that into a
			// containment refusal. A path that is not under the root gets no
			// anchor and keeps the final-component guard it always had.
			anchor := ""
			if pathWithinRoot(capability.Root, path) {
				anchor = capability.Root
			}
			entries = append(entries, WindowsACLEntry{
				Action:     WindowsACLDenyWrite,
				Path:       path,
				Capability: capability.SID,
				Anchor:     anchor,
			})
		}
	}
	writeSIDs := windowsWriteCapabilitySIDs(writeCapabilities)
	for _, path := range config.PermissionProfile.FileSystem.DenyWrite {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		for _, sid := range writeSIDs {
			entries = append(entries, WindowsACLEntry{
				Action:     WindowsACLDenyWrite,
				Path:       path,
				Capability: sid,
			})
		}
	}
	readDenySIDs, err := windowsReadDenyCapabilitySIDs(config, writeSIDs)
	if err != nil {
		return WindowsACLPlan{}, err
	}
	for _, path := range planWindowsDenyReadPaths(config.PermissionProfile.FileSystem.DenyRead) {
		for _, sid := range readDenySIDs {
			entries = append(entries, WindowsACLEntry{
				Action:      WindowsACLDenyRead,
				Path:        path,
				Capability:  sid,
				Materialize: true,
			})
		}
	}
	// Credential stores (~/.ssh, tokens, ...) are denied best-effort: deny
	// what exists, never plant what does not. Materializing an absent store
	// during (possibly elevated) setup would create a host directory the user
	// no longer owns, breaking their own tools — so unlike operator-named
	// DenyRead, these entries skip missing targets instead of creating them.
	// The write deny holds under both token modes; the read deny is enforced
	// by the fully restricted token (DenyRead profiles) and inert under
	// WRITE_RESTRICTED, where reads bypass the restricted-SID check (#612).
	for _, path := range planWindowsCredentialDenyPaths(config.PermissionProfile.FileSystem.DenyReadIfExists) {
		for _, sid := range readDenySIDs {
			entries = append(entries,
				WindowsACLEntry{Action: WindowsACLDenyWrite, Path: path, Capability: sid},
				WindowsACLEntry{Action: WindowsACLDenyRead, Path: path, Capability: sid},
			)
		}
	}
	// Carveouts stay readable inside a denied credential root (QG plugin,
	// specialist and command trees run through the sandbox). The allow sits on
	// the child object itself, so it wins over the inherited parent deny. Like
	// the denies above, carveouts name the resolved object: an unresolved
	// spelling under a symlinked parent would make the apply refuse a
	// reparse-point target, and the plan content then tracks existence — a
	// carveout created later changes the hash and the on-demand apply picks it
	// up, instead of the hash staying stable while coverage silently lags.
	for _, path := range planWindowsCredentialDenyPaths(config.PermissionProfile.FileSystem.DenyReadCarveouts) {
		for _, sid := range readDenySIDs {
			entries = append(entries, WindowsACLEntry{
				Action:     WindowsACLAllowRead,
				Path:       path,
				Capability: sid,
			})
		}
	}

	return WindowsACLPlan{Entries: dedupeWindowsACLEntries(entries)}, nil
}

// windowsSandboxOwnedMetadataName reports whether a protected metadata directory
// is owned by the sandbox itself (.qg, .agents) rather than merely guarded.
// Only owned names materialize their deny target: creating an arbitrary guarded
// name (for example an empty .git) during setup would change tool behaviour on
// the host, while an empty .qg or .agents directory is indistinguishable from
// an absent one to every consumer.
func windowsSandboxOwnedMetadataName(name string) bool {
	for _, owned := range sandboxFullyProtectedMetadataNames {
		if strings.EqualFold(strings.TrimSpace(name), owned) {
			return true
		}
	}
	return false
}

type windowsWriteRootCapability struct {
	Root                    string
	SID                     string
	ProtectedWriteDenyPaths []string
	// MaterializedWriteDenyPaths are denies derived from ProtectedMetadataNames
	// (.qg, .agents). Unlike operator-named ReadOnlySubpaths (which keep
	// ProtectedWriteDenyPaths), these materialize their target so the deny
	// holds whether or not the directory existed before setup.
	MaterializedWriteDenyPaths []string
}

func windowsWriteRootCapabilities(config WindowsSandboxCommandConfig) ([]windowsWriteRootCapability, error) {
	out := make([]windowsWriteRootCapability, 0, len(config.PermissionProfile.FileSystem.WriteRoots))
	for _, root := range config.PermissionProfile.FileSystem.WriteRoots {
		rootPath := strings.TrimSpace(root.Root)
		if rootPath == "" {
			continue
		}
		sid, err := windowsCapabilitySIDForWriteRoot(config, rootPath)
		if err != nil {
			return nil, err
		}
		protected := make([]string, 0, len(root.ReadOnlySubpaths))
		for _, subpath := range root.ReadOnlySubpaths {
			if trimmed := strings.TrimSpace(subpath); trimmed != "" {
				protected = append(protected, trimmed)
			}
		}
		materialized := make([]string, 0, len(root.ProtectedMetadataNames))
		for _, name := range root.ProtectedMetadataNames {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				if windowsSandboxOwnedMetadataName(trimmed) {
					materialized = append(materialized, filepath.Join(rootPath, trimmed))
				} else {
					protected = append(protected, filepath.Join(rootPath, trimmed))
				}
			}
		}
		out = append(out, windowsWriteRootCapability{
			Root:                       rootPath,
			SID:                        sid,
			ProtectedWriteDenyPaths:    protected,
			MaterializedWriteDenyPaths: materialized,
		})
	}
	return out, nil
}

func windowsCapabilitySIDForWriteRoot(config WindowsSandboxCommandConfig, root string) (string, error) {
	if windowsRootMatchesWorkspace(root, config.WorkspaceRoots) {
		return WindowsWorkspaceCapabilitySID(config.SandboxHome, root)
	}
	return WindowsWritableRootCapabilitySID(config.SandboxHome, root)
}

func windowsWriteCapabilitySIDs(capabilities []windowsWriteRootCapability) []string {
	out := make([]string, 0, len(capabilities))
	seen := map[string]struct{}{}
	for _, capability := range capabilities {
		if capability.SID == "" {
			continue
		}
		if _, ok := seen[capability.SID]; ok {
			continue
		}
		seen[capability.SID] = struct{}{}
		out = append(out, capability.SID)
	}
	return out
}

func windowsReadDenyCapabilitySIDs(config WindowsSandboxCommandConfig, writeSIDs []string) ([]string, error) {
	if len(writeSIDs) > 0 {
		return writeSIDs, nil
	}
	if len(config.PermissionProfile.FileSystem.DenyRead) == 0 {
		return nil, nil
	}
	caps, err := LoadOrCreateWindowsCapabilitySIDs(config.SandboxHome)
	if err != nil {
		return nil, err
	}
	return []string{caps.ReadOnly}, nil
}

// planWindowsCredentialDenyPaths resolves best-effort credential deny paths to
// the entries the Windows ACL plan can actually stamp.
//
// Only the resolved spelling is emitted: the profile path itself may be a
// symlink, and the apply refuses reparse-point targets outright, so emitting
// the link spelling would fail closed on every such host. Denying the resolved
// object covers access through the link too, because the target's DACL governs
// both. Missing paths are skipped (never materialized): the apply denies
// whatever exists when it runs, so a store created later is picked up by the
// next on-demand apply.
func planWindowsCredentialDenyPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		resolved = filepath.Clean(resolved)
		key := windowsCapabilityPathKey(resolved)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, resolved)
	}
	return out
}

func planWindowsDenyReadPaths(paths []string) []string {
	out := make([]string, 0, len(paths)*2)
	seen := map[string]struct{}{}
	push := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		key := windowsCapabilityPathKey(path)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, path)
	}
	for _, path := range paths {
		push(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			push(resolved)
		}
	}
	return out
}

func dedupeWindowsACLEntries(entries []WindowsACLEntry) []WindowsACLEntry {
	out := make([]WindowsACLEntry, 0, len(entries))
	seen := map[string]int{}
	for _, entry := range entries {
		if entry.Action == "" || strings.TrimSpace(entry.Path) == "" || strings.TrimSpace(entry.Capability) == "" {
			continue
		}
		// NoInherit is part of the identity: a direct-only deny and an
		// inheritable one on the same path/SID are different ACL shapes, and
		// collapsing them could silently promote a deliberately non-inherited
		// shared-path deny into an inheritable one (or vice versa).
		key := string(entry.Action) + "\x00" + windowsCapabilityPathKey(entry.Path) + "\x00" + strings.ToLower(entry.Capability) + "\x00" + fmt.Sprintf("%t", entry.NoInherit)
		if index, ok := seen[key]; ok {
			// The same deny can reach the plan twice (operator-named and
			// metadata-derived). Keep the stronger shape: a duplicate must
			// never disarm materialization the metadata deny relies on.
			out[index].Materialize = out[index].Materialize || entry.Materialize
			continue
		}
		seen[key] = len(out)
		out = append(out, entry)
	}
	return out
}
