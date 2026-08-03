//go:build !windows

package process

func validPlatformFileName(_ string) bool {
	return true
}
