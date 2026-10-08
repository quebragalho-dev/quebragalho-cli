package main

import (
	"os"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

func main() {
	os.Exit(sandbox.RunWindowsSandboxSetup(os.Args[1:], os.Stderr))
}
