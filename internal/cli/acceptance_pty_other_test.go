//go:build !darwin && !linux

package cli

import (
	"errors"
	"os"
)

func openAcceptancePTY() (*os.File, *os.File, error) {
	return nil, nil, errors.New("pseudo-terminals are opened on darwin and linux only")
}
