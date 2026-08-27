package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

func TestDiagnoseCentralizesLoadAssemblyAndTransfersRuntimeRoot(t *testing.T) {
	fixture := newReadyAppFixture(t)
	observation := &appConfigSourceObservation{}

	diagnosis, err := diagnoseWithStartup(
		context.Background(),
		fixture.configPath,
		fixture.deps,
		observation.dependencies(t, fixture.configPath),
	)
	if err != nil {
		t.Fatalf("diagnoseWithStartup() error = %v", err)
	}
	if !diagnosis.Report().CoreReady() || diagnosis.Report().ReadyCount() != 1 ||
		diagnosis.RuntimeRoot == nil {
		t.Fatalf("diagnosis report/root = %#v/%p", diagnosis.Report(), diagnosis.RuntimeRoot)
	}
	observation.assertLifecycle(t, 1, 0, 1)
	if fixture.executableCalls != 1 || fixture.openCalls != 1 || fixture.closeCalls != 0 {
		t.Fatalf("executable/open/root-close calls = %d/%d/%d", fixture.executableCalls, fixture.openCalls, fixture.closeCalls)
	}
	if err := fixture.deps.CloseRoot(diagnosis.RuntimeRoot); err != nil {
		t.Fatalf("CloseRoot: %v", err)
	}
	if fixture.closeCalls != 1 {
		t.Fatalf("CloseRoot calls = %d, want 1", fixture.closeCalls)
	}
}

func TestDiagnoseMapsInvalidConfigAndDependenciesToFixedErrors(t *testing.T) {
	t.Parallel()

	t.Run("invalid config before dependencies", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "PLANTED_DIAGNOSE_PATH.toml")
		diagnosis, err := diagnose(context.Background(), path, Dependencies{})
		if diagnosis.Report().Core() != nil || !errors.Is(err, ErrConfigInvalid) ||
			strings.Contains(err.Error(), path) {
			t.Fatalf("diagnose() = %#v, %v", diagnosis, err)
		}
	})

	t.Run("invalid dependencies close source", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		observation := &appConfigSourceObservation{}
		diagnosis, err := diagnoseWithStartup(
			context.Background(), fixture.configPath, Dependencies{},
			observation.dependencies(t, fixture.configPath),
		)
		if diagnosis.Report().Core() != nil || !errors.Is(err, ErrStartup) {
			t.Fatalf("diagnoseWithStartup() = %#v, %v", diagnosis, err)
		}
		observation.assertLifecycle(t, 1, 0, 1)
	})
}

func TestSelectedProvidersReadyRequiresEveryUniqueSelectedProvider(t *testing.T) {
	fixture := newReadyAppFixture(t)
	addNotReadyClaudeProvider(t, fixture)
	diagnosis, err := diagnose(context.Background(), fixture.configPath, fixture.deps)
	if err != nil {
		t.Fatalf("diagnose() error = %v", err)
	}
	defer func() {
		if diagnosis.RuntimeRoot != nil {
			_ = fixture.deps.CloseRoot(diagnosis.RuntimeRoot)
		}
	}()
	report := diagnosis.Report()
	for _, test := range []struct {
		name     string
		selected []core.ProviderName
		want     bool
	}{
		{"selected ready and unselected not ready ignored", []core.ProviderName{core.ProviderCodex}, true},
		{"selected not ready", []core.ProviderName{core.ProviderClaude}, false},
		{"all selected includes not ready", []core.ProviderName{core.ProviderCodex, core.ProviderClaude}, false},
		{"selected missing", []core.ProviderName{core.ProviderGemini}, false},
		{"unselected ready cannot rescue missing", []core.ProviderName{core.ProviderGemini, core.ProviderClaude}, false},
		{"duplicate selected", []core.ProviderName{core.ProviderCodex, core.ProviderCodex}, false},
		{"empty selected", nil, false},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := selectedProvidersReady(report, test.selected); got != test.want {
				t.Fatalf("selectedProvidersReady() = %t, want %t", got, test.want)
			}
		})
	}
	if selectedProvidersReady(doctor.Report{}, []core.ProviderName{core.ProviderCodex}) {
		t.Fatal("selectedProvidersReady() accepted an invalid report")
	}
}

func TestSelectedProvidersReadyRejectsCoreNotReady(t *testing.T) {
	fixture := newReadyAppFixture(t)
	addFixtureGatewayKey(t, fixture.configPath, "DIAGNOSE_MISSING_GATEWAY_KEY")
	fixture.deps.LookupEnv = func(string) (string, bool) { return "", false }
	diagnosis, err := diagnose(context.Background(), fixture.configPath, fixture.deps)
	if err != nil {
		t.Fatalf("diagnose() error = %v", err)
	}
	if diagnosis.RuntimeRoot != nil {
		t.Fatal("core-not-ready diagnosis unexpectedly transferred a root")
	}
	if selectedProvidersReady(diagnosis.Report(), []core.ProviderName{core.ProviderCodex}) {
		t.Fatal("selectedProvidersReady() accepted core-not-ready report")
	}
}

func addNotReadyClaudeProvider(t *testing.T, fixture *readyAppFixture) {
	t.Helper()
	raw, err := os.ReadFile(fixture.configPath) // #nosec G304 -- exact test-owned path.
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	cfg, err := config.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Decode config: %v", err)
	}
	codex := cfg.Providers[string(core.ProviderCodex)]
	providerBlock := "\n[providers.claude]\n" +
		"executable = " + quotedAppTOML(codex.Executable) + "\n" +
		"config_home = " + quotedAppTOML(codex.ConfigHome) + "\n" +
		"concurrency = 1\nqueue_size = 2\nqueue_bytes = 4096\n" +
		"queue_timeout = \"1s\"\nexecution_timeout = \"1s\"\n"
	marker := []byte("\n[[models]]")
	position := bytes.Index(raw, marker)
	if position < 0 {
		t.Fatal("fixture config has no model marker")
	}
	updated := append([]byte(nil), raw[:position]...)
	updated = append(updated, providerBlock...)
	updated = append(updated, raw[position:]...)
	updated = append(updated,
		[]byte("\n[[models]]\nid = \"claude-test\"\nprovider = \"claude\"\nprovider_model = \"claude-test\"\ncreated = 8\n")...,
	)
	// fixture.configPath is an exact path below the test-owned private fixture.
	//nolint:gosec
	if err := os.WriteFile(fixture.configPath, updated, 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	fixture.deps.Adapters[core.ProviderClaude] = &appTestAdapter{
		name: core.ProviderClaude,
		health: provider.Health{
			Provider: core.ProviderClaude,
			Status:   provider.HealthNotReady,
			Version:  "1.2.3",
			Auth:     "missing",
			Problems: []string{provider.ProblemAuthMissing},
		},
	}
}

func quotedAppTOML(value string) string {
	return strconv.Quote(value)
}
