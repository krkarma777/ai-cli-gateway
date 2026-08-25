package initconfig

import (
	"bytes"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func FuzzPlanMergePreservesUntouchedSource(f *testing.F) {
	seeds := [][]byte{
		mergeTableDocument(),
		mergeDottedDocument(),
		mergeInlineDocument(),
		mergeCommentedModelsDocument(),
		bytes.ReplaceAll(mergeTableDocument(), []byte("\n"), []byte("\r\n")),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, source []byte) {
		decoded, err := config.Decode(bytes.NewReader(source))
		if err != nil {
			t.Skip()
		}
		provider, exists := decoded.Providers["codex"]
		if !exists {
			t.Skip()
		}
		for _, model := range decoded.Models {
			if model.ID == "initconfig-fuzz-added" {
				t.Skip()
			}
		}

		desired := DesiredState{
			NewRuntimeRoot:    testAbsolutePath("fuzz-runtime"),
			SelectedProviders: []core.ProviderName{core.ProviderCodex},
			Providers: []ProviderPatch{{
				Name: core.ProviderCodex,
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
			Models: []ModelMapping{{
				ID:            "initconfig-fuzz-added",
				Provider:      core.ProviderCodex,
				ProviderModel: "fuzz-model",
			}},
			ReplaceProviders: map[core.ProviderName]struct{}{},
			ReplaceModels:    map[string]struct{}{},
		}
		index, err := buildSourceIndex(source)
		if err != nil {
			t.Fatalf("valid config could not be indexed: %v", err)
		}
		providerGroup, ok := singleSourceGroup(index, []string{"providers", "codex"})
		if !ok {
			t.Skip()
		}
		providerBytes := append([]byte(nil), source[providerGroup.Owned.Start:providerGroup.Owned.End]...)

		first, err := PlanMerge(source, true, desired)
		if err != nil {
			t.Fatalf("PlanMerge() error = %v", err)
		}
		if _, err := config.Decode(bytes.NewReader(first.Candidate)); err != nil {
			t.Fatalf("candidate decode error = %v", err)
		}
		if !bytes.Contains(first.Candidate, providerBytes) {
			t.Fatal("untouched provider source changed")
		}
		second, err := PlanMerge(first.Candidate, true, desired)
		if err != nil {
			t.Fatalf("second PlanMerge() error = %v", err)
		}
		if second.Changed || !bytes.Equal(second.Candidate, first.Candidate) {
			t.Fatal("second application was not byte-identical")
		}
	})
}
