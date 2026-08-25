package initconfig

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestPlanMergeFreshProducesValidatedConfigAndKeyPlan(t *testing.T) {
	t.Parallel()

	desired := freshMergeDesired()
	plan, err := PlanMerge(nil, false, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	if !plan.Changed {
		t.Fatal("Changed = false, want true")
	}
	if plan.KeyAction != KeyActionEnsure ||
		plan.KeyPath != desired.Gateway.APIKeyFile ||
		plan.KeyAllowExisting {
		t.Fatalf("key plan = %d, %q, %t", plan.KeyAction, plan.KeyPath, plan.KeyAllowExisting)
	}
	decoded, err := config.Decode(bytes.NewReader(plan.Candidate))
	if err != nil {
		t.Fatalf("candidate decode error = %v\n%s", err, plan.Candidate)
	}
	if !reflect.DeepEqual(decoded, plan.Config) {
		t.Fatalf("Config = %#v, decoded %#v", plan.Config, decoded)
	}
	if len(plan.Collisions) != 0 {
		t.Fatalf("Collisions = %#v", plan.Collisions)
	}
}

func TestPlanMergeProviderReplacementRequiresExactAuthorityAndReturnsPreview(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")

	preview, err := PlanMerge(source, true, desired)
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("PlanMerge() error = %v, want ErrCollision", err)
	}
	if !preview.Changed || len(preview.Candidate) == 0 || len(preview.Collisions) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	collision := preview.Collisions[0]
	if collision.Target != DiffProvider || collision.Name != "codex" ||
		len(collision.Fields) != 1 || collision.Fields[0].Name != "config_home" {
		t.Fatalf("collision = %#v", collision)
	}
	if _, err := config.Decode(bytes.NewReader(preview.Candidate)); err != nil {
		t.Fatalf("preview candidate invalid: %v\n%s", err, preview.Candidate)
	}

	desired.ReplaceProviders[core.ProviderCodex] = struct{}{}
	approved, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("approved PlanMerge() error = %v", err)
	}
	if len(approved.Collisions) != 1 {
		t.Fatalf("approved collisions = %#v", approved.Collisions)
	}
	if !bytes.Equal(approved.Candidate, preview.Candidate) {
		t.Fatal("authorization changed the semantic preview candidate")
	}
}

func TestPlanMergeProviderTablePreservesUnrelatedBytesCommentsAndTuning(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Providers[0].Command.Value.Executable = testAbsolutePath("changed", "codex")
	desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
	desired.ReplaceProviders[core.ProviderCodex] = struct{}{}

	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	for _, untouched := range [][]byte{
		[]byte("# top-level sentinel\n[server]\n# gateway auth\napi_key_env = 'AI_CLI_GATEWAY_API_KEY'\nlisten = '127.0.0.1:8080'\n\n"),
		[]byte("[runtime]\nroot = " + string(mustTOMLValue(t, testAbsolutePath("runtime"))) + "\n\n"),
		[]byte("[[models]]\nid = 'codex-existing'\nprovider = 'codex'\nprovider_model = 'gpt-existing'\ncreated = 77\n# trailing model sentinel\n"),
	} {
		if !bytes.Contains(plan.Candidate, untouched) {
			t.Fatalf("candidate lost untouched bytes %q:\n%s", untouched, plan.Candidate)
		}
	}
	for _, comment := range []string{"# executable comment", "# provider home", "# tuning comment"} {
		if !bytes.Contains(plan.Candidate, []byte(comment)) {
			t.Fatalf("candidate lost %q:\n%s", comment, plan.Candidate)
		}
	}
	if got := plan.Config.Providers["codex"].Concurrency; got != 7 {
		t.Fatalf("Concurrency = %d, want 7", got)
	}
	if !bytes.Contains(plan.Candidate, []byte("concurrency = 7 # keep tuning\n")) {
		t.Fatalf("candidate lost inline tuning comment:\n%s", plan.Candidate)
	}
}

func TestPlanMergeProviderRepresentationsRemainValidAndPreserveSiblings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    []byte
		untouched []byte
		want      string
	}{
		{
			name:      "dotted",
			source:    mergeDottedDocument(),
			untouched: []byte("runtime.root = " + string(mustTOMLValue(t, testAbsolutePath("runtime"))) + "\n"),
			want: "providers.codex.config_home = " +
				string(mustTOMLValue(t, testAbsolutePath("changed", "codex-home"))),
		},
		{
			name:      "inline",
			source:    mergeInlineDocument(),
			untouched: []byte("server = { api_key_env = 'AI_CLI_GATEWAY_API_KEY', listen = '127.0.0.1:8080' }\n"),
			want: "config_home = " +
				string(mustTOMLValue(t, testAbsolutePath("changed", "codex-home"))),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			existing := mustDecodeMergeConfig(t, test.source)
			desired := desiredFromExisting(existing, core.ProviderCodex)
			desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
			desired.ReplaceProviders[core.ProviderCodex] = struct{}{}
			plan, err := PlanMerge(test.source, true, desired)
			if err != nil {
				t.Fatalf("PlanMerge() error = %v", err)
			}
			if !bytes.Contains(plan.Candidate, test.untouched) {
				t.Fatalf("candidate lost sibling bytes %q:\n%s", test.untouched, plan.Candidate)
			}
			if !bytes.Contains(plan.Candidate, []byte(test.want)) {
				t.Fatalf("candidate missing %q:\n%s", test.want, plan.Candidate)
			}
			if _, err := config.Decode(bytes.NewReader(plan.Candidate)); err != nil {
				t.Fatalf("candidate decode error = %v\n%s", err, plan.Candidate)
			}
		})
	}
}

func TestPlanMergeAddsProviderAndModelWithoutReorderingExistingSource(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	claude := config.Provider{
		Executable:    testAbsolutePath("bin", "claude"),
		ConfigHome:    testAbsolutePath("homes", "claude"),
		CredentialEnv: []string{"ANTHROPIC_API_KEY"},
	}
	desired := DesiredState{
		NewRuntimeRoot:    testAbsolutePath("new-runtime"),
		SelectedProviders: []core.ProviderName{core.ProviderClaude},
		Providers: []ProviderPatch{{
			Name: core.ProviderClaude,
			Command: Optional[ProviderCommand]{Set: true, Value: ProviderCommand{
				Executable: claude.Executable,
			}},
			ConfigHome:    Optional[string]{Set: true, Value: claude.ConfigHome},
			CredentialEnv: Optional[[]string]{Set: true, Value: claude.CredentialEnv},
		}},
		Models: []ModelMapping{{
			ID:            "claude-local",
			Provider:      core.ProviderClaude,
			ProviderModel: "sonnet",
		}},
		ReplaceProviders: map[core.ProviderName]struct{}{},
		ReplaceModels:    map[string]struct{}{},
	}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	if !bytes.HasPrefix(plan.Candidate, source) {
		t.Fatalf("existing source prefix changed while adding:\n%s", plan.Candidate)
	}
	if _, ok := plan.Config.Providers["claude"]; !ok {
		t.Fatalf("Providers = %#v", plan.Config.Providers)
	}
	if got := plan.Config.Models[len(plan.Config.Models)-1].ID; got != "claude-local" {
		t.Fatalf("last model = %q", got)
	}
}

func TestPlanMergeGatewayAuthPreservesOtherServerFields(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Gateway = GatewayAuthPatch{
		Set:         true,
		APIKeyFile:  testAbsolutePath("config", "gateway.key"),
		KeyExplicit: true,
	}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	if bytes.Contains(plan.Candidate, []byte("api_key_env")) ||
		!bytes.Contains(plan.Candidate, []byte("api_key_file = ")) ||
		!bytes.Contains(plan.Candidate, []byte("listen = '127.0.0.1:8080'")) ||
		!bytes.Contains(plan.Candidate, []byte("# gateway auth")) {
		t.Fatalf("gateway merge lost server state:\n%s", plan.Candidate)
	}
	if plan.KeyAction != KeyActionEnsure || !plan.KeyAllowExisting {
		t.Fatalf("key plan = %d, allow %t", plan.KeyAction, plan.KeyAllowExisting)
	}
}

func TestPlanMergeGatewayFileReplacesExplicitEmptyEnvironmentSource(t *testing.T) {
	t.Parallel()

	for name, document := range map[string][]byte{
		"table":  mergeTableDocument(),
		"dotted": mergeDottedDocument(),
	} {
		document := document
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := bytes.Replace(
				document,
				[]byte("'AI_CLI_GATEWAY_API_KEY'"),
				[]byte("''"),
				1,
			)
			existing := mustDecodeMergeConfig(t, source)
			desired := desiredFromExisting(existing, core.ProviderCodex)
			desired.Gateway = GatewayAuthPatch{
				Set:        true,
				APIKeyFile: testAbsolutePath("config", "gateway.key"),
			}
			plan, err := PlanMerge(source, true, desired)
			if err != nil {
				t.Fatalf("PlanMerge() error = %v", err)
			}
			if bytes.Contains(plan.Candidate, []byte("api_key_env")) ||
				!bytes.Contains(plan.Candidate, []byte("api_key_file")) {
				t.Fatalf("candidate retained disabled environment source:\n%s", plan.Candidate)
			}
		})
	}
}

func TestPlanMergeGatewayAuthAddsMissingServerAndUpdatesInlineServer(t *testing.T) {
	t.Parallel()

	t.Run("missing server", func(t *testing.T) {
		t.Parallel()
		source := mergeDocumentWithoutServer()
		existing := mustDecodeMergeConfig(t, source)
		desired := desiredFromExisting(existing, core.ProviderCodex)
		desired.Gateway = GatewayAuthPatch{
			Set:        true,
			APIKeyFile: testAbsolutePath("config", "gateway.key"),
		}
		plan, err := PlanMerge(source, true, desired)
		if err != nil {
			t.Fatalf("PlanMerge() error = %v", err)
		}
		if !bytes.HasPrefix(plan.Candidate, source) ||
			!bytes.Contains(plan.Candidate, []byte("[server]\napi_key_file = ")) {
			t.Fatalf("candidate =\n%s", plan.Candidate)
		}
	})

	t.Run("inline server", func(t *testing.T) {
		t.Parallel()
		source := mergeInlineDocument()
		existing := mustDecodeMergeConfig(t, source)
		desired := desiredFromExisting(existing, core.ProviderCodex)
		desired.Gateway = GatewayAuthPatch{Set: true}
		plan, err := PlanMerge(source, true, desired)
		if err != nil {
			t.Fatalf("PlanMerge() error = %v", err)
		}
		if bytes.Contains(plan.Candidate, []byte("api_key_env")) ||
			!bytes.Contains(plan.Candidate, []byte("listen = '127.0.0.1:8080'")) {
			t.Fatalf("candidate =\n%s", plan.Candidate)
		}
	})
}

func TestPlanMergeGatewayKeyActionMatrix(t *testing.T) {
	t.Parallel()

	keyOne := testAbsolutePath("keys", "one.key")
	keyTwo := testAbsolutePath("keys", "two.key")
	tests := []struct {
		name          string
		mutateSource  func(*config.Config, []byte) []byte
		gateway       GatewayAuthPatch
		wantAction    KeyAction
		wantPath      string
		allowExisting bool
	}{
		{
			name:       "environment remains no key",
			wantAction: KeyActionNone,
		},
		{
			name: "unchanged file is inspected",
			mutateSource: func(_ *config.Config, source []byte) []byte {
				return bytes.Replace(source,
					[]byte("api_key_env = 'AI_CLI_GATEWAY_API_KEY'"),
					[]byte("api_key_file = "+string(mustTOMLValue(t, keyOne))), 1)
			},
			wantAction: KeyActionInspect,
			wantPath:   keyOne,
		},
		{
			name: "changed explicit file is ensured with reuse authority",
			mutateSource: func(_ *config.Config, source []byte) []byte {
				return bytes.Replace(source,
					[]byte("api_key_env = 'AI_CLI_GATEWAY_API_KEY'"),
					[]byte("api_key_file = "+string(mustTOMLValue(t, keyOne))), 1)
			},
			gateway: GatewayAuthPatch{
				Set:         true,
				APIKeyFile:  keyTwo,
				KeyExplicit: true,
			},
			wantAction:    KeyActionEnsure,
			wantPath:      keyTwo,
			allowExisting: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := mergeTableDocument()
			if test.mutateSource != nil {
				source = test.mutateSource(nil, source)
			}
			existing := mustDecodeMergeConfig(t, source)
			desired := desiredFromExisting(existing, core.ProviderCodex)
			desired.Gateway = test.gateway
			plan, err := PlanMerge(source, true, desired)
			if err != nil {
				t.Fatalf("PlanMerge() error = %v", err)
			}
			if plan.KeyAction != test.wantAction || plan.KeyPath != test.wantPath ||
				plan.KeyAllowExisting != test.allowExisting {
				t.Fatalf("key plan = %d, %q, %t", plan.KeyAction, plan.KeyPath, plan.KeyAllowExisting)
			}
		})
	}
}

func TestPlanMergeNoOpReturnsOriginalBytesWithoutMutation(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	if plan.Changed || !bytes.Equal(plan.Candidate, source) {
		t.Fatalf("no-op plan changed source:\n%s", plan.Candidate)
	}
	plan.Candidate[0] = 'X'
	if source[0] == 'X' {
		t.Fatal("Candidate aliases source")
	}
}

func TestPlanMergeRejectsInvalidSourceAndExistenceShapes(t *testing.T) {
	t.Parallel()

	desired := freshMergeDesired()
	tests := []struct {
		source []byte
		exists bool
	}{
		{source: []byte("PLANTED_INVALID_TOML"), exists: true},
		{source: nil, exists: true},
		{source: []byte("nonempty"), exists: false},
	}
	for _, test := range tests {
		plan, err := PlanMerge(test.source, test.exists, desired)
		if !errors.Is(err, ErrPlan) {
			t.Fatalf("PlanMerge(%q, %t) error = %v, want ErrPlan", test.source, test.exists, err)
		}
		if len(plan.Candidate) != 0 {
			t.Fatalf("invalid source returned candidate %q", plan.Candidate)
		}
	}
}

func freshMergeDesired() DesiredState {
	desired := validDesiredState()
	desired.Gateway = GatewayAuthPatch{
		Set:        true,
		APIKeyFile: testAbsolutePath("config", "gateway.key"),
	}
	return desired
}

func desiredFromExisting(
	existing config.Config,
	providerName core.ProviderName,
) DesiredState {
	provider := existing.Providers[string(providerName)]
	return DesiredState{
		NewRuntimeRoot:    testAbsolutePath("new-runtime"),
		SelectedProviders: []core.ProviderName{providerName},
		Providers: []ProviderPatch{{
			Name: providerName,
			Command: Optional[ProviderCommand]{Set: true, Value: ProviderCommand{
				Executable: provider.Executable,
				PrefixArgs: append([]string(nil), provider.PrefixArgs...),
			}},
			ConfigHome: Optional[string]{Set: true, Value: provider.ConfigHome},
			CredentialEnv: Optional[[]string]{
				Set:   true,
				Value: append([]string(nil), provider.CredentialEnv...),
			},
		}},
		ReplaceProviders: map[core.ProviderName]struct{}{},
		ReplaceModels:    map[string]struct{}{},
	}
}

func mergeTableDocument() []byte {
	return []byte("# top-level sentinel\n" +
		"[server]\n" +
		"# gateway auth\n" +
		"api_key_env = 'AI_CLI_GATEWAY_API_KEY'\n" +
		"listen = '127.0.0.1:8080'\n\n" +
		"[runtime]\n" +
		"root = " + string(mustEncodedValue(testAbsolutePath("runtime"))) + "\n\n" +
		"[providers.codex]\n" +
		"# executable comment\n" +
		"executable = " + string(mustEncodedValue(testAbsolutePath("bin", "codex"))) + "\n" +
		"# provider home\n" +
		"config_home = " + string(mustEncodedValue(testAbsolutePath("homes", "codex"))) + " # provider home inline\n" +
		"# tuning comment\n" +
		"concurrency = 7 # keep tuning\n\n" +
		"[[models]]\n" +
		"id = 'codex-existing'\n" +
		"provider = 'codex'\n" +
		"provider_model = 'gpt-existing'\n" +
		"created = 77\n" +
		"# trailing model sentinel\n")
}

func mergeDottedDocument() []byte {
	return []byte("server.api_key_env = 'AI_CLI_GATEWAY_API_KEY'\n" +
		"server.listen = '127.0.0.1:8080'\n" +
		"runtime.root = " + string(mustEncodedValue(testAbsolutePath("runtime"))) + "\n" +
		"providers.codex.executable = " + string(mustEncodedValue(testAbsolutePath("bin", "codex"))) + "\n" +
		"# dotted home comment\n" +
		"providers.codex.config_home = " + string(mustEncodedValue(testAbsolutePath("homes", "codex"))) + "\n" +
		"models = [{ id = 'codex-existing', provider = 'codex', provider_model = 'gpt-existing', created = 77 }]\n")
}

func mergeInlineDocument() []byte {
	return []byte("server = { api_key_env = 'AI_CLI_GATEWAY_API_KEY', listen = '127.0.0.1:8080' }\n" +
		"runtime = { root = " + string(mustEncodedValue(testAbsolutePath("runtime"))) + " }\n" +
		"providers = { codex = { executable = " + string(mustEncodedValue(testAbsolutePath("bin", "codex"))) +
		", config_home = " + string(mustEncodedValue(testAbsolutePath("homes", "codex"))) + ", concurrency = 7 } }\n" +
		"models = [{ id = 'codex-existing', provider = 'codex', provider_model = 'gpt-existing', created = 77 }]\n")
}

func mergeDocumentWithoutServer() []byte {
	return []byte("[runtime]\n" +
		"root = " + string(mustEncodedValue(testAbsolutePath("runtime"))) + "\n\n" +
		"[providers.codex]\n" +
		"executable = " + string(mustEncodedValue(testAbsolutePath("bin", "codex"))) + "\n" +
		"config_home = " + string(mustEncodedValue(testAbsolutePath("homes", "codex"))) + "\n\n" +
		"[[models]]\n" +
		"id = 'codex-existing'\n" +
		"provider = 'codex'\n" +
		"provider_model = 'gpt-existing'\n")
}

func mustDecodeMergeConfig(t *testing.T, source []byte) config.Config {
	t.Helper()
	decoded, err := config.Decode(bytes.NewReader(source))
	if err != nil {
		t.Fatalf("config.Decode() error = %v\n%s", err, source)
	}
	return decoded
}

func mustTOMLValue(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := encodeTOMLValue(value)
	if err != nil {
		t.Fatalf("encodeTOMLValue() error = %v", err)
	}
	return encoded
}

func mustEncodedValue(value any) []byte {
	encoded, err := encodeTOMLValue(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestPlanMergeNeverCopiesRawSourceSecretsIntoCollisionMetadata(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	planted := "PLANTED_COMMENT_SECRET"
	source = bytes.Replace(source, []byte("# top-level sentinel"), []byte("# "+planted), 1)
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
	plan, err := PlanMerge(source, true, desired)
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	for _, collision := range plan.Collisions {
		if strings.Contains(collision.Name, planted) {
			t.Fatalf("collision name leaked source: %#v", collision)
		}
		for _, field := range collision.Fields {
			if strings.Contains(field.Name+field.Before+field.After, planted) {
				t.Fatalf("collision field leaked source: %#v", field)
			}
		}
	}
}

func TestPlanMergeModelAdditionsAreDeterministicAndIdempotent(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Models = []ModelMapping{
		{ID: "z-new", Provider: core.ProviderCodex, ProviderModel: "gpt-z"},
		{ID: "a-new", Provider: core.ProviderCodex, ProviderModel: "gpt-a"},
	}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	if !bytes.HasPrefix(plan.Candidate, source) {
		t.Fatalf("existing source was reordered:\n%s", plan.Candidate)
	}
	aPosition := bytes.Index(plan.Candidate, []byte("id = 'a-new'"))
	zPosition := bytes.Index(plan.Candidate, []byte("id = 'z-new'"))
	if aPosition < len(source) || zPosition <= aPosition {
		t.Fatalf("new model positions = %d, %d\n%s", aPosition, zPosition, plan.Candidate)
	}
	requireIdempotentPlan(t, plan, desired)
}

func TestPlanMergeModelIdenticalMappingIsExactNoOp(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Models = []ModelMapping{{
		ID:            "codex-existing",
		Provider:      core.ProviderCodex,
		ProviderModel: "gpt-existing",
	}}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	if plan.Changed || !bytes.Equal(plan.Candidate, source) {
		t.Fatalf("identical model changed source:\n%s", plan.Candidate)
	}
}

func TestPlanMergeModelReplacementRequiresAliasAuthorityAndPreservesCreated(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Models = []ModelMapping{{
		ID:            "codex-existing",
		Provider:      core.ProviderCodex,
		ProviderModel: "gpt-replaced",
	}}

	preview, err := PlanMerge(source, true, desired)
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("PlanMerge() error = %v, want ErrCollision", err)
	}
	if len(preview.Collisions) != 1 ||
		preview.Collisions[0].Target != DiffModel ||
		preview.Collisions[0].Name != "codex-existing" {
		t.Fatalf("Collisions = %#v", preview.Collisions)
	}
	if got := preview.Config.Models[0].Created; got != 77 {
		t.Fatalf("preview Created = %d, want 77", got)
	}

	desired.ReplaceModels["codex-existing"] = struct{}{}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("approved PlanMerge() error = %v", err)
	}
	if got := plan.Config.Models[0].Created; got != 77 {
		t.Fatalf("Created = %d, want 77", got)
	}
	for _, comment := range []string{"# trailing model sentinel"} {
		if !bytes.Contains(plan.Candidate, []byte(comment)) {
			t.Fatalf("candidate lost %q:\n%s", comment, plan.Candidate)
		}
	}
	requireIdempotentPlan(t, plan, desired)
}

func TestPlanMergeModelProviderAndArgumentReplaceTogether(t *testing.T) {
	t.Parallel()

	source := mergeTwoProviderDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderClaude)
	desired.Models = []ModelMapping{{
		ID:            "shared-existing",
		Provider:      core.ProviderClaude,
		ProviderModel: "sonnet",
	}}
	desired.ReplaceModels["shared-existing"] = struct{}{}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	model := plan.Config.Models[0]
	if model.Provider != "claude" || model.ProviderModel != "sonnet" || model.Created != 321 {
		t.Fatalf("model = %#v", model)
	}
	if len(plan.Collisions) != 1 || len(plan.Collisions[0].Fields) != 2 {
		t.Fatalf("Collisions = %#v", plan.Collisions)
	}
	requireIdempotentPlan(t, plan, desired)
}

func TestPlanMergeModelInlineArraySupportsReplacementAndAddition(t *testing.T) {
	t.Parallel()

	source := mergeInlineDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Models = []ModelMapping{
		{ID: "codex-existing", Provider: core.ProviderCodex, ProviderModel: "gpt-replaced"},
		{ID: "codex-new", Provider: core.ProviderCodex, ProviderModel: "gpt-new"},
	}
	desired.ReplaceModels["codex-existing"] = struct{}{}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	if !bytes.Contains(plan.Candidate, []byte("models = [")) ||
		bytes.Contains(plan.Candidate, []byte("[[models]]")) ||
		!bytes.Contains(plan.Candidate, []byte("id = 'codex-new'")) {
		t.Fatalf("inline model representation was not preserved:\n%s", plan.Candidate)
	}
	if got := plan.Config.Models[0].Created; got != 77 {
		t.Fatalf("replaced inline Created = %d, want 77", got)
	}
	requireIdempotentPlan(t, plan, desired)
}

func TestPlanMergeModelCommentsAndOmittedAliasesRemainByteIdentical(t *testing.T) {
	t.Parallel()

	source := mergeCommentedModelsDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Models = []ModelMapping{{
		ID:            "one",
		Provider:      core.ProviderCodex,
		ProviderModel: "gpt-one-replaced",
	}}
	desired.ReplaceModels["one"] = struct{}{}
	plan, err := PlanMerge(source, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	untouchedSecond := []byte("[[models]]\n# second id comment\nid = 'two'\nprovider = 'codex'\nprovider_model = 'gpt-two'\ncreated = 22\n")
	if !bytes.Contains(plan.Candidate, untouchedSecond) {
		t.Fatalf("omitted model bytes changed:\n%s", plan.Candidate)
	}
	for _, comment := range []string{
		"# first id comment",
		"# first provider inline",
		"# between model blocks",
	} {
		if !bytes.Contains(plan.Candidate, []byte(comment)) {
			t.Fatalf("candidate lost %q:\n%s", comment, plan.Candidate)
		}
	}
	requireIdempotentPlan(t, plan, desired)
}

func TestPlanMergeRejectsDuplicateRequestedModelAliases(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Models = []ModelMapping{
		{ID: "duplicate", Provider: core.ProviderCodex, ProviderModel: "one"},
		{ID: "duplicate", Provider: core.ProviderCodex, ProviderModel: "two"},
	}
	plan, err := PlanMerge(source, true, desired)
	if !errors.Is(err, ErrPlan) || len(plan.Candidate) != 0 {
		t.Fatalf("PlanMerge() = %#v, %v, want empty ErrPlan", plan, err)
	}
}

func requireIdempotentPlan(t *testing.T, first MergePlan, desired DesiredState) {
	t.Helper()
	second, err := PlanMerge(first.Candidate, true, desired)
	if err != nil {
		t.Fatalf("second PlanMerge() error = %v", err)
	}
	if second.Changed || !bytes.Equal(second.Candidate, first.Candidate) {
		t.Fatalf("second application changed candidate:\nfirst:\n%s\nsecond:\n%s", first.Candidate, second.Candidate)
	}
}

func mergeTwoProviderDocument() []byte {
	return []byte("[server]\n" +
		"api_key_env = 'AI_CLI_GATEWAY_API_KEY'\n\n" +
		"[runtime]\n" +
		"root = " + string(mustEncodedValue(testAbsolutePath("runtime"))) + "\n\n" +
		"[providers.codex]\n" +
		"executable = " + string(mustEncodedValue(testAbsolutePath("bin", "codex"))) + "\n" +
		"config_home = " + string(mustEncodedValue(testAbsolutePath("homes", "codex"))) + "\n\n" +
		"[providers.claude]\n" +
		"executable = " + string(mustEncodedValue(testAbsolutePath("bin", "claude"))) + "\n" +
		"config_home = " + string(mustEncodedValue(testAbsolutePath("homes", "claude"))) + "\n" +
		"credential_env = ['ANTHROPIC_API_KEY']\n\n" +
		"[[models]]\n" +
		"id = 'shared-existing'\n" +
		"provider = 'codex'\n" +
		"provider_model = 'gpt-existing'\n" +
		"created = 321\n")
}

func mergeCommentedModelsDocument() []byte {
	return []byte("[runtime]\n" +
		"root = " + string(mustEncodedValue(testAbsolutePath("runtime"))) + "\n\n" +
		"[providers.codex]\n" +
		"executable = " + string(mustEncodedValue(testAbsolutePath("bin", "codex"))) + "\n" +
		"config_home = " + string(mustEncodedValue(testAbsolutePath("homes", "codex"))) + "\n\n" +
		"[[models]]\n" +
		"# first id comment\n" +
		"id = 'one'\n" +
		"provider = 'codex' # first provider inline\n" +
		"provider_model = 'gpt-one'\n" +
		"created = 11\n" +
		"# between model blocks\n" +
		"[[models]]\n" +
		"# second id comment\n" +
		"id = 'two'\n" +
		"provider = 'codex'\n" +
		"provider_model = 'gpt-two'\n" +
		"created = 22\n")
}
