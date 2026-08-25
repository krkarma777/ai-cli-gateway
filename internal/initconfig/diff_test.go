package initconfig

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestSemanticDiffWritesExactSortedColorlessOutput(t *testing.T) {
	t.Parallel()

	diff := SemanticDiff{Entries: []DiffEntry{
		{
			Kind:   DiffAdded,
			Target: DiffModel,
			Name:   "z-model",
			Fields: []DiffField{
				{Name: "provider_model", After: "gpt-z"},
				{Name: "provider", After: "codex"},
			},
		},
		{
			Kind:   DiffUnchanged,
			Target: DiffProvider,
			Name:   "codex",
			Fields: []DiffField{{
				Name: "executable", Before: "/bin/codex", After: "/bin/codex",
			}},
		},
		{
			Kind:   DiffReplaced,
			Target: DiffGatewayAuth,
			Name:   "gateway",
			Fields: []DiffField{
				{Name: "key_file", Before: "", After: "/config/gateway.key"},
				{Name: "mode", Before: "environment", After: "file"},
			},
		},
		{
			Kind:   DiffReplaced,
			Target: DiffModel,
			Name:   "a-model",
			Fields: []DiffField{
				{Name: "provider_model", Before: "old", After: "new"},
				{Name: "provider", Before: "codex", After: "claude"},
			},
		},
	}}
	original := cloneSemanticDiffForTest(diff)
	var output bytes.Buffer
	written, err := diff.WriteTo(&output)
	if err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	want := "~ gateway-auth gateway\n" +
		"  mode: environment -> file\n" +
		"  key_file: /config/gateway.key\n" +
		"unchanged provider codex\n" +
		"  executable: /bin/codex\n" +
		"~ model a-model\n" +
		"  provider: codex -> claude\n" +
		"  provider_model: old -> new\n" +
		"+ model z-model\n" +
		"  provider: codex\n" +
		"  provider_model: gpt-z\n"
	if output.String() != want {
		t.Fatalf("WriteTo() = %q, want %q", output.String(), want)
	}
	if written != int64(len(want)) {
		t.Fatalf("WriteTo() wrote %d bytes, want %d", written, len(want))
	}
	if !reflect.DeepEqual(diff, original) {
		t.Fatalf("WriteTo() mutated receiver: %#v, want %#v", diff, original)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("WriteTo() emitted ANSI: %q", output.String())
	}
}

func TestSemanticDiffRejectsUnsafeOrOpenValuesBeforeWriting(t *testing.T) {
	t.Parallel()

	const planted = "PLANTED_DIFF_SECRET"
	tests := map[string]SemanticDiff{
		"unknown kind": {Entries: []DiffEntry{{
			Kind: DiffKind(99), Target: DiffProvider, Name: "codex",
		}}},
		"unknown target": {Entries: []DiffEntry{{
			Kind: DiffAdded, Target: DiffTarget(99), Name: "codex",
		}}},
		"unknown provider": {Entries: []DiffEntry{{
			Kind: DiffAdded, Target: DiffProvider, Name: planted,
		}}},
		"invalid model alias": {Entries: []DiffEntry{{
			Kind: DiffAdded, Target: DiffModel, Name: "-invalid",
		}}},
		"unknown field": {Entries: []DiffEntry{{
			Kind: DiffAdded, Target: DiffProvider, Name: "codex",
			Fields: []DiffField{{Name: planted, After: "value"}},
		}}},
		"duplicate field": {Entries: []DiffEntry{{
			Kind: DiffAdded, Target: DiffProvider, Name: "codex",
			Fields: []DiffField{
				{Name: "config_home", After: "/one"},
				{Name: "config_home", After: "/two"},
			},
		}}},
		"duplicate entry": {Entries: []DiffEntry{
			{Kind: DiffAdded, Target: DiffProvider, Name: "codex"},
			{Kind: DiffAdded, Target: DiffProvider, Name: "codex"},
		}},
		"ANSI value": {Entries: []DiffEntry{{
			Kind: DiffReplaced, Target: DiffProvider, Name: "codex",
			Fields: []DiffField{{Name: "config_home", After: "\x1b[31m" + planted}},
		}}},
		"newline value": {Entries: []DiffEntry{{
			Kind: DiffReplaced, Target: DiffModel, Name: "safe-alias",
			Fields: []DiffField{{Name: "provider_model", After: "safe\n" + planted}},
		}}},
		"invalid gateway mode": {Entries: []DiffEntry{{
			Kind: DiffReplaced, Target: DiffGatewayAuth, Name: "gateway",
			Fields: []DiffField{{Name: "mode", After: planted}},
		}}},
	}
	for name, diff := range tests {
		diff := diff
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if _, err := diff.WriteTo(&output); !errors.Is(err, ErrPlan) {
				t.Fatalf("WriteTo() error = %v, want ErrPlan", err)
			}
			if output.Len() != 0 || strings.Contains(output.String(), planted) {
				t.Fatalf("WriteTo() leaked partial output %q", output.String())
			}
		})
	}
}

func TestSemanticDiffNilAndFailedWritersReturnFixedError(t *testing.T) {
	t.Parallel()

	diff := SemanticDiff{Entries: []DiffEntry{{
		Kind: DiffAdded, Target: DiffProvider, Name: "codex",
	}}}
	if _, err := diff.WriteTo(nil); !errors.Is(err, ErrPlan) {
		t.Fatalf("WriteTo(nil) error = %v, want ErrPlan", err)
	}
	var typedNil *bytes.Buffer
	if _, err := diff.WriteTo(typedNil); !errors.Is(err, ErrPlan) {
		t.Fatalf("WriteTo(typed nil) error = %v, want ErrPlan", err)
	}

	const planted = "PLANTED_WRITER_ERROR"
	for _, writer := range []io.Writer{
		failingDiffWriter{err: errors.New(planted)},
		shortDiffWriter{},
	} {
		_, err := diff.WriteTo(writer)
		if !errors.Is(err, ErrPlan) || strings.Contains(err.Error(), planted) {
			t.Fatalf("WriteTo(failed writer) error = %v, want fixed ErrPlan", err)
		}
	}
}

func TestSemanticDiffFromMergeContainsNoRawSourceOrCredentialValues(t *testing.T) {
	t.Parallel()

	const (
		commentSecret    = "PLANTED_SOURCE_COMMENT_SECRET"
		sensitiveFixture = "PLANTED_RAW_CREDENTIAL_VALUE"
	)
	source := bytes.Replace(
		mergeTableDocument(),
		[]byte("# top-level sentinel"),
		[]byte("# "+commentSecret+" "+sensitiveFixture),
		1,
	)
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, "codex")
	desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
	desired.ReplaceProviders["codex"] = struct{}{}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	var output bytes.Buffer
	if _, err := plan.Diff.WriteTo(&output); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	if strings.Contains(output.String(), commentSecret) ||
		strings.Contains(output.String(), sensitiveFixture) {
		t.Fatalf("semantic diff leaked secret: %q", output.String())
	}
}

func TestSemanticDiffGatewaySourceSwitchUsesSeparateClosedFields(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, "codex")
	desired.Gateway = GatewayAuthPatch{
		Set:        true,
		APIKeyFile: testAbsolutePath("config", "gateway.key"),
	}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	var output bytes.Buffer
	if _, err := plan.Diff.WriteTo(&output); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	want := "~ gateway-auth gateway\n" +
		"  mode: environment -> file\n" +
		"  key_file: " + testAbsolutePath("config", "gateway.key") + "\n" +
		"  key_env: AI_CLI_GATEWAY_API_KEY -> (none)\n" +
		"unchanged provider codex\n"
	if output.String() != want {
		t.Fatalf("WriteTo() = %q, want %q", output.String(), want)
	}
}

type failingDiffWriter struct {
	err error
}

func (writer failingDiffWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type shortDiffWriter struct{}

func (shortDiffWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return len(value) - 1, nil
}

func cloneSemanticDiffForTest(diff SemanticDiff) SemanticDiff {
	cloned := SemanticDiff{Entries: append([]DiffEntry(nil), diff.Entries...)}
	for index := range cloned.Entries {
		cloned.Entries[index].Fields = append([]DiffField(nil), diff.Entries[index].Fields...)
	}
	return cloned
}
