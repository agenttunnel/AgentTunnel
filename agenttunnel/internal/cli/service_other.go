//go:build !darwin && !linux && !windows

package cli

import (
	"fmt"
	"runtime"
)

// serviceNote is printed after install; empty where there is no service.
const serviceNote = ""

// newPlatformService reports that this OS has no service manager wired up.
// The error wraps errServiceManagerUnavailable so install exits with the
// unavailable code instead of failing as a refused command.
func newPlatformService(_ string, _ serviceRunner) (serviceManager, error) {
	return nil, fmt.Errorf("%w: no service manager for %s", errServiceManagerUnavailable, runtime.GOOS)
}
