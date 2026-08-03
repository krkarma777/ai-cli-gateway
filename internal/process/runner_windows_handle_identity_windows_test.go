//go:build windows

package process

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/windows"
)

var compareObjectHandlesForIdentityProbe = windows.NewLazySystemDLL(
	"kernelbase.dll",
).NewProc("CompareObjectHandles")

type windowsHandleIdentityOps struct {
	duplicate func(windows.Handle, windows.Handle) (windows.Handle, error)
	compare   func(windows.Handle, windows.Handle) (bool, error)
	close     func(windows.Handle) error
}

var liveWindowsHandleIdentityOps = windowsHandleIdentityOps{
	duplicate: func(
		sourceProcess windows.Handle,
		sourceHandle windows.Handle,
	) (windows.Handle, error) {
		var proof windows.Handle
		err := windows.DuplicateHandle(
			sourceProcess,
			sourceHandle,
			windows.CurrentProcess(),
			&proof,
			0,
			false,
			windows.DUPLICATE_SAME_ACCESS,
		)
		return proof, err
	},
	compare: compareWindowsObjectHandles,
	close:   windows.CloseHandle,
}

// probeWindowsHandleIdentity duplicates a candidate out of the suspended
// process. A missing raw handle is safe, and a different object at the same
// numeric slot is ordinary per-process handle reuse. Only the same kernel
// object is an inherited-handle leak.
func probeWindowsHandleIdentity(
	ops windowsHandleIdentityOps,
	sourceProcess windows.Handle,
	candidate windows.Handle,
	expected windows.Handle,
) (bool, error) {
	proof, err := ops.duplicate(sourceProcess, candidate)
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("duplicate suspended child handle: %w", err)
	}

	same, compareErr := ops.compare(proof, expected)
	closeErr := ops.close(proof)
	if compareErr != nil || closeErr != nil {
		var probeErrors []error
		if compareErr != nil {
			probeErrors = append(probeErrors, fmt.Errorf(
				"compare suspended child handle: %w",
				compareErr,
			))
		}
		if closeErr != nil {
			probeErrors = append(probeErrors, fmt.Errorf(
				"close suspended child handle proof: %w",
				closeErr,
			))
		}
		return false, errors.Join(probeErrors...)
	}
	return same, nil
}

func compareWindowsObjectHandles(
	left windows.Handle,
	right windows.Handle,
) (bool, error) {
	if err := compareObjectHandlesForIdentityProbe.Find(); err != nil {
		return false, fmt.Errorf("resolve CompareObjectHandles: %w", err)
	}
	result, _, callErr := compareObjectHandlesForIdentityProbe.Call(
		uintptr(left),
		uintptr(right),
	)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, windows.ERROR_NOT_SAME_OBJECT) {
		return false, nil
	}
	return false, fmt.Errorf("CompareObjectHandles: %w", callErr)
}

func TestLiveWindowsHandleIdentityProbeDistinguishesObjectIdentity(
	t *testing.T,
) {
	t.Parallel()

	first, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(first); err != nil {
			t.Errorf("close first event: %v", err)
		}
	})
	second, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(second); err != nil {
			t.Errorf("close second event: %v", err)
		}
	})

	tests := []struct {
		name          string
		candidate     windows.Handle
		expected      windows.Handle
		wantInherited bool
	}{
		{
			name:          "same object",
			candidate:     first,
			expected:      first,
			wantInherited: true,
		},
		{
			name:      "different object at a valid numeric slot",
			candidate: first,
			expected:  second,
		},
		{
			name:     "absent candidate",
			expected: first,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inherited, probeErr := probeWindowsHandleIdentity(
				liveWindowsHandleIdentityOps,
				windows.CurrentProcess(),
				test.candidate,
				test.expected,
			)
			if probeErr != nil {
				t.Fatal(probeErr)
			}
			if inherited != test.wantInherited {
				t.Fatalf(
					"inherited=%t want=%t",
					inherited,
					test.wantInherited,
				)
			}
		})
	}
}

func TestProbeWindowsHandleIdentityClassifiesWithoutLeakingProof(
	t *testing.T,
) {
	t.Parallel()

	duplicateFailure := errors.New("duplicate failure")
	compareFailure := errors.New("compare failure")
	closeFailure := errors.New("close failure")
	tests := []struct {
		name          string
		duplicateErr  error
		same          bool
		compareErr    error
		closeErr      error
		wantInherited bool
		wantErr       error
		wantCloses    int
	}{
		{
			name:         "candidate absent",
			duplicateErr: windows.ERROR_INVALID_HANDLE,
		},
		{
			name:         "duplicate failure fails closed",
			duplicateErr: duplicateFailure,
			wantErr:      duplicateFailure,
		},
		{
			name:       "numeric slot reused by a different object",
			wantCloses: 1,
		},
		{
			name:          "same object is inherited",
			same:          true,
			wantInherited: true,
			wantCloses:    1,
		},
		{
			name:       "comparison failure closes proof",
			compareErr: compareFailure,
			wantErr:    compareFailure,
			wantCloses: 1,
		},
		{
			name:       "close failure is reported",
			closeErr:   closeFailure,
			wantErr:    closeFailure,
			wantCloses: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const proof = windows.Handle(303)
			closes := 0
			ops := windowsHandleIdentityOps{
				duplicate: func(
					sourceProcess windows.Handle,
					candidate windows.Handle,
				) (windows.Handle, error) {
					if sourceProcess != 101 || candidate != 202 {
						t.Fatalf(
							"duplicate source=(%d,%d)",
							sourceProcess,
							candidate,
						)
					}
					return proof, test.duplicateErr
				},
				compare: func(
					left windows.Handle,
					right windows.Handle,
				) (bool, error) {
					if left != proof || right != 404 {
						t.Fatalf("compare handles=(%d,%d)", left, right)
					}
					return test.same, test.compareErr
				},
				close: func(handle windows.Handle) error {
					closes++
					if handle != proof {
						t.Fatalf("close handle=%d", handle)
					}
					return test.closeErr
				},
			}

			inherited, err := probeWindowsHandleIdentity(
				ops,
				101,
				202,
				404,
			)
			if inherited != test.wantInherited {
				t.Fatalf(
					"inherited=%t want=%t",
					inherited,
					test.wantInherited,
				)
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want cause=%v", err, test.wantErr)
			}
			if closes != test.wantCloses {
				t.Fatalf("proof closes=%d want=%d", closes, test.wantCloses)
			}
		})
	}
}
