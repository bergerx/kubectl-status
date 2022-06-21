package main

import (
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth" // Initialize all known client auth plugins.

	"github.com/bergerx/kubectl-status/cmd/plugin/cli"
)

func main() {
	// Kubernetes uses UTC times, status shows times only in ".. ago" format, so
	// setting the TZ to UTC is safe.
	os.Setenv("TZ", "UTC")
	cli.InitAndExecute()
}
