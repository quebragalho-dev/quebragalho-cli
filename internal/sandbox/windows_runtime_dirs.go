package sandbox

// windowsRuntimeDirGrant is one QG-owned runtime directory plus the capability
// SID that must hold an allow-write ACE on it. The runtime tree (the sandbox's
// TEMP/TMP point inside it) is recreated by prepareSandboxRuntime on every
// command, but a 30-day cleanup between commands deletes the grant with the
// directory — and the elevated tier never granted it in the first place, since
// the setup-time profile does not know the per-command runtime root. The
// runner re-grants exactly these dirs before token creation; nothing else on
// the host is in scope.
type windowsRuntimeDirGrant struct {
	Path       string
	Capability string
}

func windowsSandboxRuntimeDirGrants(config WindowsSandboxCommandConfig) ([]windowsRuntimeDirGrant, error) {
	runtimeState := config.PermissionProfile.Runtime
	if runtimeState == nil || len(trimNonEmptyStrings([]string{runtimeState.Root})) == 0 {
		return nil, nil
	}
	sid, err := windowsCapabilitySIDForWriteRoot(config, runtimeState.Root)
	if err != nil {
		return nil, err
	}
	dirs := []string{runtimeState.Root, runtimeState.Cache, runtimeState.Data, runtimeState.Temp}
	out := make([]windowsRuntimeDirGrant, 0, len(dirs))
	seen := map[string]struct{}{}
	for _, dir := range dirs {
		if trimmed := trimNonEmptyStrings([]string{dir}); len(trimmed) == 0 {
			continue
		}
		key := windowsCapabilityPathKey(dir)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, windowsRuntimeDirGrant{Path: dir, Capability: sid})
	}
	return out, nil
}
