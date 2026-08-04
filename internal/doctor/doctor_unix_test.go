//go:build !windows

package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

func TestRunProviderPathProblemPrecedenceRunsNoProbeAndStaysUnresolved(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*testing.T, *config.Provider)
		configure   func(*Dependencies)
		wantProblem string
	}{
		{
			name: "missing executable before unsafe config",
			mutate: func(t *testing.T, value *config.Provider) {
				parent, err := filepath.EvalSymlinks(doctorTestPrivateDirectory(t))
				if err != nil {
					t.Fatalf("resolve missing-path parent: %v", err)
				}
				value.Executable = filepath.Join(parent, "missing")
				unsafeHome := doctorTestPrivateDirectory(t)
				//nolint:gosec // Deliberately unsafe directory fixture.
				if err := os.Chmod(unsafeHome, 0o755); err != nil {
					t.Fatalf("chmod unsafe home: %v", err)
				}
				value.ConfigHome = unsafeHome
			},
			wantProblem: provider.ProblemExecutableMissing,
		},
		{
			name: "unsafe executable before unsafe config",
			mutate: func(t *testing.T, value *config.Provider) {
				value.Executable = doctorTestPrivateDirectory(t)
				unsafeHome := doctorTestPrivateDirectory(t)
				//nolint:gosec // Deliberately unsafe directory fixture.
				if err := os.Chmod(unsafeHome, 0o755); err != nil {
					t.Fatalf("chmod unsafe home: %v", err)
				}
				value.ConfigHome = unsafeHome
			},
			wantProblem: provider.ProblemExecutableUnsafe,
		},
		{
			name: "unsafe config",
			mutate: func(t *testing.T, value *config.Provider) {
				unsafeHome := doctorTestPrivateDirectory(t)
				//nolint:gosec // Deliberately unsafe directory fixture.
				if err := os.Chmod(unsafeHome, 0o755); err != nil {
					t.Fatalf("chmod unsafe home: %v", err)
				}
				value.ConfigHome = unsafeHome
			},
			wantProblem: provider.ProblemConfigHomeUnsafe,
		},
		{
			name: "exact launcher missing Node",
			mutate: func(t *testing.T, value *config.Provider) {
				fixture := newUnixNodeLauncherFixture(t, "\n")
				value.Executable = fixture.shim
			},
			configure: func(dependencies *Dependencies) {
				dependencies.LookupExecutable = func(string) (string, error) {
					return "", errors.New("node is unavailable")
				}
			},
			wantProblem: provider.ProblemExecutableUnsafe,
		},
		{
			name: "exact launcher unsafe Node",
			mutate: func(t *testing.T, value *config.Provider) {
				fixture := newUnixNodeLauncherFixture(t, "\n")
				value.Executable = fixture.shim
			},
			configure: func(dependencies *Dependencies) {
				dependencies.LookupExecutable = func(string) (string, error) {
					return doctorTestPrivateDirectory(t), nil
				}
			},
			wantProblem: provider.ProblemExecutableUnsafe,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(t, core.ProviderCodex)
			providerConfig := cfg.Providers["codex"]
			test.mutate(t, &providerConfig)
			cfg.Providers["codex"] = providerConfig
			adapter := &doctorTestAdapter{
				name: core.ProviderCodex, interval: reportTestRange(),
				health: validReadyProviderHealth(core.ProviderCodex),
			}
			dependencies, _, _ := doctorTestReadyDependencies(
				t,
				cfg,
				map[core.ProviderName]provider.Adapter{core.ProviderCodex: adapter},
				&doctorTestController{},
			)
			if test.configure != nil {
				test.configure(&dependencies)
			}
			diagnosis, err := Run(context.Background(), cfg, dependencies)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			row := diagnosis.Report().Providers()[0]
			if !slices.Equal(row.Problems, []string{test.wantProblem}) ||
				diagnosis.ResolvedProviders() != nil || adapter.probeCalls != 0 {
				t.Fatalf("row/resolved/probes = %+v/%#v/%d",
					row, diagnosis.ResolvedProviders(), adapter.probeCalls)
			}
			if err := diagnosis.RuntimeRoot.Close(); err != nil {
				t.Fatalf("close transferred root: %v", err)
			}
		})
	}
}

func TestRunResolvesUnixNodeLauncherBeforeProbe(t *testing.T) {
	fixture := newUnixNodeLauncherFixture(t, "\n")
	validatedNode, disposition := validateExecutablePath(fixture.node)
	if disposition != pathSafe {
		t.Fatalf("Node disposition=%v", disposition)
	}
	validatedLauncher := fixture.launcher
	cfg := doctorTestConfig(t, core.ProviderCodex)
	configured := cfg.Providers["codex"]
	configured.Executable = fixture.shim
	cfg.Providers["codex"] = configured

	var resolved provider.ProviderConfig
	adapter := &doctorTestAdapter{
		name: core.ProviderCodex, interval: reportTestRange(),
		probe: func(
			_ context.Context,
			value provider.ProviderConfig,
			_ provider.ProbeRunner,
		) provider.Health {
			resolved = value.Clone()
			return validReadyProviderHealth(core.ProviderCodex)
		},
	}
	dependencies, _, _ := doctorTestReadyDependencies(
		t,
		cfg,
		map[core.ProviderName]provider.Adapter{core.ProviderCodex: adapter},
		&doctorTestController{},
	)
	lookups := 0
	dependencies.LookupExecutable = func(name string) (string, error) {
		lookups++
		if name != "node" {
			t.Fatalf("lookup name=%q, want node", name)
		}
		return fixture.node, nil
	}

	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resolved.Executable != validatedNode.Resolved ||
		!slices.Equal(resolved.PrefixArgs, []string{validatedLauncher.Resolved}) ||
		!strings.Contains(resolved.SafePath, filepath.Dir(validatedNode.Resolved)) {
		t.Fatalf("resolved provider command=%+v", resolved)
	}
	if lookups != 1 || adapter.probeCalls != 1 {
		t.Fatalf("lookups/probes = %d/%d, want 1/1", lookups, adapter.probeCalls)
	}
	resolvedProvider, present := diagnosis.ResolvedProviders()[core.ProviderCodex]
	if !present || resolvedProvider.Health.Status != provider.HealthReady ||
		diagnosis.RuntimeRoot == nil {
		t.Fatalf("transferred ready provider=%+v root=%p", resolvedProvider, diagnosis.RuntimeRoot)
	}
	if err := diagnosis.RuntimeRoot.Close(); err != nil {
		t.Fatalf("close transferred root: %v", err)
	}
}
