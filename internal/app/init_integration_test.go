//go:build integration

package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/initconfig"
)

func TestInitFreshCommitRunsDoctorAndSecondRunIsNoop(t *testing.T) {
	fixture := newReadyAppFixture(t)
	raw, err := os.ReadFile(fixture.configPath) // #nosec G304 -- exact test-owned fixture path.
	if err != nil {
		t.Fatalf("ReadFile fixture: %v", err)
	}
	existing, err := config.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Decode fixture: %v", err)
	}
	providerConfig := existing.Providers[string(core.ProviderCodex)]
	configPath := filepath.Join(filepath.Dir(fixture.configPath), "initialized.toml")
	options := initconfig.Options{
		ConfigPath: configPath, NonInteractive: true,
		Providers: []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]initconfig.ProviderInput{
			core.ProviderCodex: {
				Executable: initconfig.StringValue{Set: true, Value: providerConfig.Executable},
				ConfigHome: initconfig.StringValue{Set: true, Value: providerConfig.ConfigHome},
			},
		},
		Models: []initconfig.ModelMapping{{
			ID: "codex-test", Provider: core.ProviderCodex, ProviderModel: "gpt-test",
		}},
	}
	deps := ProductionInitDependencies(ioDiscardForInit{})
	deps.Runtime = fixture.deps
	deps.Entropy = bytes.NewReader(make([]byte, 32))
	deps.DefaultInitRuntimeRoot = func() (string, error) {
		return filepath.Join(filepath.Dir(configPath), "init-runtime"), nil
	}
	var first bytes.Buffer
	result := Init(context.Background(), options, &first, deps)
	if result != (InitResult{Outcome: InitReady, Saved: true}) ||
		!strings.Contains(first.String(), "saved_config:") ||
		!strings.Contains(first.String(), "gateway_key_file:") ||
		!strings.Contains(first.String(), "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}") ||
		!strings.Contains(first.String(), "core:\n") ||
		!strings.HasSuffix(first.String(), "setup_ready\n") ||
		fixture.httpIDCalls != 0 || fixture.listenCalls != 0 {
		t.Fatalf("first Init() = %#v output %q", result, first.String())
	}

	deps.Entropy = panicInitReader{}
	deps.DefaultInitRuntimeRoot = func() (string, error) {
		panic("existing config resolved the default runtime root")
	}
	var second bytes.Buffer
	result = Init(context.Background(), options, &second, deps)
	if result != (InitResult{Outcome: InitReady}) ||
		!strings.Contains(second.String(), "already_current:") ||
		!strings.HasSuffix(second.String(), "setup_ready\n") {
		t.Fatalf("second Init() = %#v output %q", result, second.String())
	}
}

type ioDiscardForInit struct{}

func (ioDiscardForInit) Write(payload []byte) (int, error) { return len(payload), nil }
