//go:build plan9

package mcp

// Plan 9 has no BSD-style errno constants (it uses string errors), so the
// errno-based classification is unavailable here: every predicate returns
// false and reconnect.go keeps classifying via its string markers, exactly
// as before. This variant only keeps the package building on Plan 9.

func isRefusedDialErrno(error) bool { return false }

func isResetErrno(error) bool { return false }

func isClosedPipeErrno(error) bool { return false }
