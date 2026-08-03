package provider

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
)

type contractAdapter struct{}

func (contractAdapter) Name() core.ProviderName {
	return core.ProviderCodex
}

func (contractAdapter) SupportedVersion() Range {
	return Range{}
}

func (contractAdapter) Probe(context.Context, ProviderConfig, ProbeRunner) Health {
	return Health{}
}

func (contractAdapter) Build(
	core.Request,
	core.Model,
	ProviderConfig,
	process.Runtime,
) (process.CommandSpec, error) {
	return process.CommandSpec{}, nil
}

func (contractAdapter) Parse(core.Request, process.Result) (string, error) {
	return "", nil
}

type contractProbeRunner struct{}

func (contractProbeRunner) RunProbe(
	_ context.Context,
	build func(process.Runtime) (process.CommandSpec, error),
) (process.Result, error) {
	_, err := build(process.Runtime{})
	return process.Result{}, err
}

func TestAdapterContractsAreImplementableByChildPackages(_ *testing.T) {
	var _ Adapter = contractAdapter{}
	var _ ProbeRunner = contractProbeRunner{}
}

func TestProviderConfigCloneOwnsPrefixAndCredentialSlices(t *testing.T) {
	original := ProviderConfig{
		Executable:    "/trusted/provider",
		PrefixArgs:    []string{"exec", "--safe"},
		ConfigHome:    "/trusted/config",
		CredentialEnv: []string{"PROVIDER_TOKEN"},
		SafePath:      "/trusted/bin",
	}

	cloned := original.Clone()
	cloned.PrefixArgs[0] = "mutated"
	cloned.CredentialEnv[0] = "OTHER_TOKEN"

	if original.PrefixArgs[0] != "exec" {
		t.Fatalf("original prefix args changed: %q", original.PrefixArgs)
	}
	if original.CredentialEnv[0] != "PROVIDER_TOKEN" {
		t.Fatalf("original credential names changed: %q", original.CredentialEnv)
	}
}

func TestHealthCloneOwnsCapabilityAndProblemSlices(t *testing.T) {
	original := Health{
		Provider:     core.ProviderGemini,
		Status:       HealthReady,
		Version:      "0.53.0",
		Auth:         "environment",
		Capabilities: []string{"text", "json_schema"},
		Problems:     []string{"none"},
	}

	cloned := original.Clone()
	cloned.Capabilities[0] = "mutated"
	cloned.Problems[0] = "mutated"

	if original.Capabilities[0] != "text" {
		t.Fatalf("original capabilities changed: %q", original.Capabilities)
	}
	if original.Problems[0] != "none" {
		t.Fatalf("original problems changed: %q", original.Problems)
	}
}

func TestProviderProblemCodesMatchCompleteOrderedUniqueSet(t *testing.T) {
	var sourceDirCandidates []string
	if _, sourcePath, _, ok := runtime.Caller(0); ok {
		sourceDirCandidates = append(sourceDirCandidates, filepath.Dir(sourcePath))
	}
	if workingDir, err := os.Getwd(); err == nil {
		sourceDirCandidates = append(sourceDirCandidates, workingDir)
	}

	var sourceDir string
	for _, candidate := range sourceDirCandidates {
		parsed, err := parser.ParseFile(
			token.NewFileSet(),
			filepath.Join(candidate, "adapter.go"),
			nil,
			parser.PackageClauseOnly,
		)
		if err == nil && parsed.Name.Name == "provider" {
			sourceDir = candidate
			break
		}
	}
	if sourceDir == "" {
		t.Fatal("provider test could not locate a parseable adapter.go source file")
	}

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatalf("read provider source directory: %v", err)
	}

	var gotNames []string
	files := token.NewFileSet()
	// os.ReadDir sorts by filename, and parsed declarations retain source order.
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(
			files,
			filepath.Join(sourceDir, name),
			nil,
			0,
		)
		if parseErr != nil {
			t.Fatalf("parse provider source %q: %v", name, parseErr)
		}
		for _, declaration := range parsed.Decls {
			constants, isConstants := declaration.(*ast.GenDecl)
			if !isConstants || constants.Tok != token.CONST {
				continue
			}
			for _, specification := range constants.Specs {
				values := specification.(*ast.ValueSpec)
				for _, identifier := range values.Names {
					if token.IsExported(identifier.Name) &&
						strings.HasPrefix(identifier.Name, "Problem") {
						gotNames = append(gotNames, identifier.Name)
					}
				}
			}
		}
	}
	wantNames := []string{
		"ProblemExecutableMissing",
		"ProblemExecutableUnsafe",
		"ProblemVersionUnreadable",
		"ProblemVersionUnsupported",
		"ProblemCapabilityMissing",
		"ProblemConfigHomeUnsafe",
		"ProblemAuthMissing",
		"ProblemAuthUnknown",
		"ProblemCredentialMissing",
		"ProblemCredentialFileUnsafe",
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("provider problem constant names = %q, want %q", gotNames, wantNames)
	}

	got := []string{
		ProblemExecutableMissing,
		ProblemExecutableUnsafe,
		ProblemVersionUnreadable,
		ProblemVersionUnsupported,
		ProblemCapabilityMissing,
		ProblemConfigHomeUnsafe,
		ProblemAuthMissing,
		ProblemAuthUnknown,
		ProblemCredentialMissing,
		ProblemCredentialFileUnsafe,
	}
	want := []string{
		"executable_missing",
		"executable_unsafe",
		"version_unreadable",
		"version_unsupported",
		"capability_missing",
		"config_home_unsafe",
		"auth_missing",
		"auth_unknown",
		"credential_missing",
		"credential_file_unsafe",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("provider problem codes = %q, want %q", got, want)
	}

	seen := make(map[string]struct{}, len(got))
	for _, code := range got {
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("provider problem code %q is duplicated", code)
		}
		seen[code] = struct{}{}
	}
}

func TestProviderErrorCategoriesAreClosedInspectableAndSafe(t *testing.T) {
	tests := []struct {
		category ErrorCategory
		message  string
	}{
		{ProviderErrorAuthRequired, "provider authentication is required"},
		{ProviderErrorRateLimited, "provider rate limit was reached"},
		{ProviderErrorProtocol, "provider protocol error"},
		{ProviderErrorFailed, "provider failed"},
	}
	//nolint:gosec // Deliberate planted value verifies redaction.
	const plantedSecret = "stderr=sk-provider-secret prompt=/private/request"

	for _, test := range tests {
		t.Run(string(test.category), func(t *testing.T) {
			providerErr := NewProviderError(test.category)
			if got := providerErr.Category(); got != test.category {
				t.Fatalf("Category()=%q, want %q", got, test.category)
			}
			if got := providerErr.Error(); got != test.message {
				t.Fatalf("Error()=%q, want %q", got, test.message)
			}
			if strings.Contains(providerErr.Error(), plantedSecret) {
				t.Fatal("provider error exposed planted secret")
			}
		})
	}
}

func TestProviderErrorInvalidCategoriesFailClosed(t *testing.T) {
	//nolint:gosec // Deliberate planted value verifies fail-closed redaction.
	const plantedSecret = "stdout=secret-output credential=sk-secret"

	providerErr := NewProviderError(ErrorCategory(plantedSecret))
	if got := providerErr.Category(); got != ProviderErrorFailed {
		t.Fatalf("invalid category became %q, want %q", got, ProviderErrorFailed)
	}
	if got := providerErr.Error(); got != "provider failed" {
		t.Fatalf("invalid category Error()=%q", got)
	}
	if strings.Contains(providerErr.Error(), plantedSecret) {
		t.Fatal("invalid category text leaked through Error()")
	}

	var nilError *ProviderError
	if got := nilError.Category(); got != ProviderErrorFailed {
		t.Fatalf("nil Category()=%q, want %q", got, ProviderErrorFailed)
	}
	if got := nilError.Error(); got != "provider failed" {
		t.Fatalf("nil Error()=%q", got)
	}
}

func TestProviderModelValidationRemainsInCore(t *testing.T) {
	if err := core.ValidateProviderModel("gemini-2.5-pro"); err != nil {
		t.Fatalf("trusted model rejected: %v", err)
	}
	if err := core.ValidateProviderModel("--attacker-flag"); err == nil {
		t.Fatal("leading-dash provider model accepted")
	}
}
