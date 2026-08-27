package configstore

import (
	"bytes"
	"testing"
)

func TestStageGatewayKeyCommitMatrix(t *testing.T) {
	t.Parallel()

	valid := append(bytes.Repeat([]byte{'a'}, 64), '\n')
	for _, test := range []struct {
		name    string
		plan    KeyPlan
		state   KeyState
		payload []byte
		want    keyCommitMode
		ok      bool
	}{
		{"none", KeyPlan{}, KeyStateNone, nil, keyCommitNoop, true},
		{"none with payload", KeyPlan{}, KeyStateNone, valid, keyCommitNoop, false},
		{"ensure missing", KeyPlan{Intent: KeyIntentEnsure}, KeyStateMissing, valid, keyCommitCreate, true},
		{"ensure missing empty", KeyPlan{Intent: KeyIntentEnsure}, KeyStateMissing, nil, keyCommitNoop, false},
		{"ensure missing 64 bytes", KeyPlan{Intent: KeyIntentEnsure}, KeyStateMissing, valid[:64], keyCommitNoop, false},
		{"ensure missing CRLF", KeyPlan{Intent: KeyIntentEnsure}, KeyStateMissing, append(valid[:64:64], '\r', '\n'), keyCommitNoop, false},
		{"ensure missing uppercase", KeyPlan{Intent: KeyIntentEnsure}, KeyStateMissing, append(bytes.Repeat([]byte{'A'}, 64), '\n'), keyCommitNoop, false},
		{"inspect missing", KeyPlan{Intent: KeyIntentInspect}, KeyStateMissing, nil, keyCommitNoop, false},
		{"inspect reusable", KeyPlan{Intent: KeyIntentInspect}, KeyStateReusable, nil, keyCommitReuse, true},
		{"inspect reusable payload", KeyPlan{Intent: KeyIntentInspect}, KeyStateReusable, valid, keyCommitNoop, false},
		{"ensure reusable authorized", KeyPlan{Intent: KeyIntentEnsure, AllowExisting: true}, KeyStateReusable, nil, keyCommitReuse, true},
		{"ensure reusable unauthorized", KeyPlan{Intent: KeyIntentEnsure}, KeyStateReusable, nil, keyCommitNoop, false},
		{"ensure reusable payload", KeyPlan{Intent: KeyIntentEnsure, AllowExisting: true}, KeyStateReusable, valid, keyCommitNoop, false},
		{"needs confirmation", KeyPlan{Intent: KeyIntentEnsure}, KeyStateNeedsConfirmation, nil, keyCommitNoop, false},
		{"unknown state", KeyPlan{Intent: KeyIntentEnsure}, KeyState(99), nil, keyCommitNoop, false},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateKeyCommitMatrix(test.plan, test.state, test.payload)
			if test.ok && (err != nil || got != test.want) {
				t.Fatalf("validateKeyCommitMatrix() = %d, %v; want %d, nil", got, err, test.want)
			}
			if !test.ok && err == nil {
				t.Fatalf("validateKeyCommitMatrix() = %d, nil; want error", got)
			}
		})
	}
}
