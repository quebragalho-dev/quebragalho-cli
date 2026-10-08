package main

import (
	"os"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

func main() {
	os.Exit(sandbox.RunWindowsSandboxCommandRunner(os.Args[1:], os.Stderr))
}
