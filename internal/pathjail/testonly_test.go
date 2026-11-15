package pathjail

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Open confines dir to root and returns a handle on root plus dir expressed
// relative to it. The caller closes the handle. Test-only: production opens
// its temp files through CreateTemp; no production path confines an
// arbitrary directory through Open.
//
// root is resolved normally, because it is the boundary the caller chose and
// trusts (a workspace the operator opened, say, which may legitimately sit under
// a link). Everything BELOW it is resolved against the returned handle and
// cannot escape.
func Open(root, dir string) (*os.Root, string, error) {
	// Only a genuinely EMPTY value is treated as missing; every other spelling is
	// used verbatim. Testing TrimSpace(...) == "" instead would retarget the very
	// paths this guards: " " is a legal directory name on unix, so trimming would
	// reject it as "no root" or silently swap dir for root, which is the same
	// retargeting bug as trimming "root " down to "root".
	if root == "" {
		return nil, "", fmt.Errorf("%w: no root to confine %q to", ErrEscapes, dir)
	}
	if dir == "" {
		dir = root
	}
	// Rel before Open, so an escaping target is refused without opening
	// anything. filepath.Rel is lexical, which is the right test here: it says
	// what the caller ASKED for, and the handle enforces what actually happens.
	relative, err := filepath.Rel(root, dir)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %q against %q: %v", ErrEscapes, dir, root, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, "", fmt.Errorf("%w: %q is outside %q", ErrEscapes, dir, root)
	}
	// The root is created if it is missing, by ordinary pathname, because it is
	// the boundary the caller declared rather than anything being confined. The
	// stores this replaced called MkdirAll on the whole tree, so this creates
	// strictly less by pathname than before, and everything BELOW root is opened
	// relative to the handle.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, "", fmt.Errorf("create %s: %w", root, err)
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", root, err)
	}
	return handle, relative, nil
}

// RefuseReparse fails when relative names a link or reparse point. Test-only:
// production relies on the os.Root handle itself refusing reparse traversal.
//
// Absent is fine: a name that does not exist yet is what a create is for.
//
// ModeIrregular is tested alongside ModeSymlink deliberately: that is what a
// Windows junction reports, and a guard checking only ModeSymlink lets one
// straight through.
func RefuseReparse(handle *os.Root, relative string) error {
	// A trailing separator makes os.Root.Lstat resolve the terminal component on
	// Windows (doInRootAlwaysResolveTerminalSlash), so a junction named with a
	// trailing slash would be stat'd as its target and wave through as "not a
	// link". Strip it so the final component itself is the thing inspected.
	//
	// os.IsPathSeparator rather than a literal `/\` set, because backslash is a
	// separator only on Windows: on unix it is an ordinary filename character, and
	// trimming it would inspect "linked" when asked about a symlink genuinely
	// named `linked\` — reporting "not a link" for one that is.
	if trimmed := trimTrailingSeparators(relative); trimmed != "" {
		relative = trimmed
	}
	info, err := handle.Lstat(relative)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", relative, err)
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("%w: %s", ErrReparse, relative)
	}
	return nil
}

// trimTrailingSeparators drops the path separators at the end of a name, using
// the ACTIVE platform's definition of one: `/` and `\` on Windows, `/` only on
// unix, where a backslash is a legal character in a filename.
func trimTrailingSeparators(path string) string {
	end := len(path)
	for end > 0 && os.IsPathSeparator(path[end-1]) {
		end--
	}
	return path[:end]
}
