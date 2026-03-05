//go:build windows

package plugin

import "fmt"

func signalName(signal int64) string {
	return fmt.Sprintf("signal %d", signal)
}
