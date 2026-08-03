//go:build !darwin && !windows

package process

const retainedZombieEPERMPolicy = 0

func retainedZombieSignalErrorMeansAbsent(error) bool {
	return false
}

func retainedZombieProbeErrorMeansAbsent(error, bool) bool {
	return false
}
