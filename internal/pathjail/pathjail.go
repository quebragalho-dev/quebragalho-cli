// Package pathjail confines file operations to a directory tree.
//
// It exists because the obvious guard does not work. Checking a path with
// Lstat and then handing the same string to MkdirAll, CreateTemp, Rename or
// Remove is two independent resolutions with a gap between them, and every
// operation after the check re-resolves the ANCESTORS as well. A store that
// verified only its own directory and file was therefore writable outside its
// tree whenever any component above them was a link: the check passed, and the
// syscall that followed went somewhere else.
//
// On Windows the same guard failed for a second, independent reason. A junction
// is a reparse point but NOT a symlink, so os.Lstat reports it as ModeIrregular
// with ModeSymlink clear, and a guard testing only ModeSymlink returns "not a
// link" when pointed straight at one. Junctions also need no privilege to
// create, unlike Windows symlinks, so this is reachable by exactly the
// unprivileged user a workspace guard is meant to contain.
//
// The fix for both is to stop naming things by path. Open a handle on the root
// once and address everything below it relative to that handle, so the kernel
// resolves each component against an object that cannot be swapped underneath.
// os.Root is that handle: it refuses any component that would escape, including
// a Windows reparse point, and it is the same idea the sandbox uses for ACL
// materialization.
package pathjail

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscapes reports a target that is not inside the root it was confined to.
// Returned before anything is opened, so a caller cannot act on the path first.
var ErrEscapes = errors.New("path escapes its root")

// ErrReparse reports a link or reparse point where a real file or directory was
// required.
var ErrReparse = errors.New("refusing to write through a link")

// randomBytes fills b with cryptographic randomness. A variable rather than a
// direct crypto/rand call so a test can force the name collision that exercises
// the O_EXCL retry below — the branch that matters, and one a real random source
// will not reach on demand.
var randomBytes = rand.Read

// CreateTemp makes an O_EXCL temp file named prefix.<random>suffix inside dir,
// relative to handle, and returns it with its relative path.
//
// O_EXCL rather than a fixed name: a link planted at a predictable temp path
// between the check and the create is the same race this package exists to
// close, and O_EXCL makes the kernel refuse it rather than us.
func CreateTemp(handle *os.Root, dir, prefix, suffix string) (*os.File, string, error) {
	// prefix and suffix are NAME fragments, not path fragments. A separator or a
	// "."/".." in either would relocate the file out of dir — a prefix of
	// "../escaped", or a suffix of ".tmp/../evil" that also erases the random
	// component and with it the O_EXCL unpredictability the file relies on.
	// Reject them up front so the file lands where dir promises.
	for _, fragment := range []string{prefix, suffix} {
		if strings.ContainsAny(fragment, `/\`) || fragment == "." || fragment == ".." {
			return nil, "", fmt.Errorf("%w: temporary name fragment %q must be a single path component", ErrEscapes, fragment)
		}
	}
	for attempt := 0; attempt < 10; attempt++ {
		noise := make([]byte, 8)
		if _, err := randomBytes(noise); err != nil {
			return nil, "", fmt.Errorf("generate a temporary name: %w", err)
		}
		relative := filepath.Join(dir, prefix+"."+hex.EncodeToString(noise)+suffix)
		file, err := handle.OpenFile(relative, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, relative, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return nil, "", err
	}
	return nil, "", errors.New("could not create a temporary file with an unused name")
}
