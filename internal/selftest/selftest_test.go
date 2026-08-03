package selftest

import (
	"bytes"
	"testing"
)

func TestMainIgnoresPublicArguments(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	handled, code := Main([]string{"--help"}, &stdout, &stderr)
	if handled {
		t.Fatalf("public arguments handled with code %d", code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.Bytes(), stderr.Bytes())
	}
}

func TestMainRejectsMalformedInternalArguments(t *testing.T) {
	tests := [][]string{
		{"__process-selftest"},
		{"__process-selftest", "unknown"},
		{"__process-selftest", "parent", "extra"},
	}
	for _, args := range tests {
		t.Run(args[len(args)-1], func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			handled, code := Main(args, &stdout, &stderr)
			if !handled {
				t.Fatal("internal prefix was not handled")
			}
			if code == 0 {
				t.Fatal("malformed internal arguments succeeded")
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout=%q", stdout.Bytes())
			}
		})
	}
}
