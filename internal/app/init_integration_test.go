//go:build integration

package app

import (
	"bytes"
	"context"
	"io"
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
	result := Init(context.Background(), options, nonInteractiveInitStreams(&first), deps)
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
	result = Init(context.Background(), options, nonInteractiveInitStreams(&second), deps)
	if result != (InitResult{Outcome: InitReady}) ||
		!strings.Contains(second.String(), "already_current:") ||
		!strings.HasSuffix(second.String(), "setup_ready\n") {
		t.Fatalf("second Init() = %#v output %q", result, second.String())
	}
}

func TestInitInteractiveConfirmedWriteRunsDoctorAndUsesSelectedProviderReadiness(t *testing.T) {
	fixture := newReadyAppFixture(t)
	addNotReadyClaudeProvider(t, fixture)
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			options := request.Initial
			options.Models = []initconfig.ModelMapping{{
				ID: "codex-interactive", Provider: core.ProviderCodex, ProviderModel: "gpt-interactive",
			}}
			return initconfig.CollectResponse{Options: options}, nil
		},
		review: func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}
	deps := ProductionInitDependencies(ioDiscardForInit{})
	deps.Runtime = fixture.deps
	deps.Entropy = panicInitReader{}
	deps.IsTerminal = func(input io.Reader, output io.Writer) bool { return true }
	deps.LookupEnv = func(string) (string, bool) { return "", false }
	deps.NewPrompt = func(io.Reader, io.Writer, bool) initconfig.Prompt { return prompt }
	deps.Discover = func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
		return map[core.ProviderName]initconfig.ProviderDiscovery{}, nil
	}
	var stdout, stderr bytes.Buffer

	result := Init(context.Background(), initconfig.Options{
		ConfigPath: fixture.configPath,
		Providers:  []core.ProviderName{core.ProviderCodex},
	}, Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, deps)

	if result != (InitResult{Outcome: InitReady, Saved: true}) ||
		!strings.Contains(stdout.String(), "+ model codex-interactive\n") ||
		!strings.Contains(stdout.String(), "saved_config:") ||
		!strings.Contains(stdout.String(), "codex\tready") ||
		!strings.Contains(stdout.String(), "claude\tnot_ready") ||
		!strings.HasSuffix(stdout.String(), "setup_ready\n") || stderr.Len() != 0 ||
		fixture.closeCalls != 1 || fixture.listenCalls != 0 {
		t.Fatalf("Init() = %#v output=%q stderr=%q close/listen=%d/%d",
			result, stdout.String(), stderr.String(), fixture.closeCalls, fixture.listenCalls)
	}
}

type ioDiscardForInit struct{}

func (ioDiscardForInit) Write(payload []byte) (int, error) { return len(payload), nil }
