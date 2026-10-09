package modelregistry

// Modes returns a copy of the preset catalog, preserving declaration order so
// list output and help text stay stable.
func Modes() []Mode {
	modes := make([]Mode, len(defaultModes))
	for index, mode := range defaultModes {
		modes[index] = cloneMode(mode)
	}
	return modes
}
