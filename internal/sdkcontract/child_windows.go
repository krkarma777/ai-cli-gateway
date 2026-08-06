//go:build windows

package sdkcontract

import (
	"context"
	"io"
	"io/fs"
	"time"
)

func platformSupported() bool                            { return false }
func createOwnedRoot(string, string) (ownedRoot, error)  { return nil, newError(categoryUnsupported) }
func validateSecureAncestors(string) error               { return newError(categoryUnsupported) }
func privateDirectory(fs.FileInfo) bool                  { return false }
func writePrivateFile(string, []byte, fs.FileMode) error { return newError(categoryUnsupported) }
func validateExecutableIdentity(string) (pathIdentity, error) {
	return pathIdentity{}, newError(categoryUnsupported)
}
func validateJavaScriptIdentity(string) (pathIdentity, error) {
	return pathIdentity{}, newError(categoryUnsupported)
}
func revalidateExecutableIdentity(pathIdentity) error { return newError(categoryUnsupported) }
func revalidateJavaScriptIdentity(pathIdentity) error { return newError(categoryUnsupported) }
func startPlatformChild(string, string, []string, []string, io.Writer) (child, error) {
	return nil, newError(categoryUnsupported)
}
func runGroupCommand(context.Context, string, string, []string, []string, time.Duration, int, int) (groupCommandResult, error) {
	return groupCommandResult{}, newError(categoryUnsupported)
}
