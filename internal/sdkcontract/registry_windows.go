//go:build windows

package sdkcontract

import "time"

func startPlatformRegistry(string, time.Duration) (fixtureRegistry, error) {
	return nil, newError(categoryUnsupported)
}
