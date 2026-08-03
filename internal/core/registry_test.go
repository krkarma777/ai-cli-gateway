package core

import (
	"strings"
	"testing"
)

func TestRegistrySortsAndResolves(t *testing.T) {
	input := []Model{
		{ID: "z-model", Provider: ProviderCodex, ProviderModel: "z", Created: 2},
		{ID: "a-model", Provider: ProviderClaude, ProviderModel: "a", Created: 1},
	}
	registry, err := NewRegistry(input)
	if err != nil {
		t.Fatal(err)
	}

	models := registry.Models()
	if len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "z-model" {
		t.Fatalf("Models()=%v", models)
	}
	model, ok := registry.Resolve("z-model")
	if !ok || model != input[0] {
		t.Fatalf("Resolve(z-model)=(%v, %v)", model, ok)
	}
}

func TestRegistryDefensivelyCopiesModels(t *testing.T) {
	input := []Model{
		{ID: "b", Provider: ProviderCodex, ProviderModel: "trusted-b"},
		{ID: "a", Provider: ProviderClaude, ProviderModel: "trusted-a"},
	}
	registry, err := NewRegistry(input)
	if err != nil {
		t.Fatal(err)
	}

	input[0] = Model{ID: "attacker", ProviderModel: "-unsafe"}
	first := registry.Models()
	first[0] = Model{ID: "mutated", ProviderModel: "-unsafe"}
	second := registry.Models()

	if len(second) != 2 || second[0].ID != "a" || second[1].ID != "b" {
		t.Fatalf("Models() after mutation=%v", second)
	}
	resolved, ok := registry.Resolve("b")
	if !ok || resolved.ProviderModel != "trusted-b" {
		t.Fatalf("Resolve(b)=(%v, %v)", resolved, ok)
	}
	if _, ok := registry.Resolve("attacker"); ok {
		t.Fatal("Resolve(attacker) succeeded after caller mutation")
	}
}

func TestRegistryAcceptsValidAliases(t *testing.T) {
	aliases := []string{
		"a",
		"Model-1",
		"a.b_c:d-e",
		"9",
		"a" + strings.Repeat("b", 127),
	}

	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			if _, err := NewRegistry([]Model{{
				ID:            alias,
				Provider:      ProviderGemini,
				ProviderModel: "safe/model v1:latest",
			}}); err != nil {
				t.Fatalf("NewRegistry() error=%v", err)
			}
		})
	}
}

func TestRegistryRejectsUnsafeAlias(t *testing.T) {
	aliases := []string{
		"",
		"-model",
		".model",
		"_model",
		":model",
		"../model",
		"a/b",
		`a\b`,
		"a b",
		"a\tb",
		"a\nb",
		"é",
		"a" + strings.Repeat("b", 128),
	}

	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			if _, err := NewRegistry([]Model{{
				ID:            alias,
				Provider:      ProviderCodex,
				ProviderModel: "safe",
			}}); err == nil {
				t.Fatalf("NewRegistry() accepted alias %q", alias)
			}
		})
	}
}

func TestRegistryRejectsDuplicateAliases(t *testing.T) {
	if _, err := NewRegistry([]Model{
		{ID: "duplicate", Provider: ProviderCodex, ProviderModel: "one"},
		{ID: "duplicate", Provider: ProviderClaude, ProviderModel: "two"},
	}); err == nil {
		t.Fatal("NewRegistry() accepted duplicate aliases")
	}
}

func TestRegistryRejectsUnknownProvider(t *testing.T) {
	for _, provider := range []ProviderName{"", "other"} {
		if _, err := NewRegistry([]Model{{
			ID:            "model",
			Provider:      provider,
			ProviderModel: "safe",
		}}); err == nil {
			t.Fatalf("NewRegistry() accepted provider %q", provider)
		}
	}
}

func TestValidateProviderModelAcceptsPrintableUTF8(t *testing.T) {
	for _, value := range []string{
		"model",
		"models/gpt-5.2:latest",
		`publisher\model`,
		"model name (preview)@2026-07-30",
		"モデル-α",
		strings.Repeat("é", 128),
	} {
		if err := ValidateProviderModel(value); err != nil {
			t.Errorf("ValidateProviderModel(%q)=%v", value, err)
		}
	}
}

func TestValidateProviderModelRejectsUnsafeValues(t *testing.T) {
	values := []string{
		"",
		"-model",
		"\x00model",
		"model\x00",
		"model\nnext",
		"model\tnext",
		"model\x7f",
		string([]byte{0xff, 0xfe}),
		strings.Repeat("a", 257),
		strings.Repeat("é", 129),
	}

	for _, value := range values {
		if err := ValidateProviderModel(value); err == nil {
			t.Errorf("ValidateProviderModel(%q)=nil", value)
		}
	}
}

func TestRegistryRejectsUnsafeProviderModel(t *testing.T) {
	if _, err := NewRegistry([]Model{{
		ID:            "model",
		Provider:      ProviderCodex,
		ProviderModel: "-unsafe",
	}}); err == nil {
		t.Fatal("NewRegistry() accepted unsafe provider model")
	}
}

func TestRegistryUnknownModelLookup(t *testing.T) {
	registry, err := NewRegistry([]Model{{
		ID:            "known",
		Provider:      ProviderCodex,
		ProviderModel: "trusted",
	}})
	if err != nil {
		t.Fatal(err)
	}

	model, ok := registry.Resolve("unknown")
	if ok || model != (Model{}) {
		t.Fatalf("Resolve(unknown)=(%v, %v), want zero, false", model, ok)
	}
}
