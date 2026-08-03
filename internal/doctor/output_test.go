package doctor

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

func TestValidateReportRejectsConstructionMembershipAliasAndRangeCorruption(t *testing.T) {
	base := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderClaude),
		validReadyProvider(core.ProviderCodex),
		validReadyProvider(core.ProviderGemini),
	}, []string{"model-a", "model-z"})

	tests := []struct {
		name   string
		mutate func(*Report)
	}{
		{"unconstructed", func(report *Report) { report.constructed = false }},
		{"core phase", func(report *Report) { report.phase = reportPhaseCore }},
		{"provider phase", func(report *Report) { report.phase = reportPhaseProviders }},
		{"unknown phase", func(report *Report) { report.phase = reportPhase(255) }},
		{"actual provider missing", func(report *Report) { report.providers = report.providers[1:] }},
		{"actual provider reordered", func(report *Report) {
			report.providers[0], report.providers[1] = report.providers[1], report.providers[0]
		}},
		{"actual provider duplicate", func(report *Report) { report.providers[1].Name = report.providers[0].Name }},
		{"actual provider unknown", func(report *Report) { report.providers[0].Name = "planted-provider" }},
		{"expected provider missing", func(report *Report) { report.expectedProviders = report.expectedProviders[1:] }},
		{"expected provider reordered", func(report *Report) {
			report.expectedProviders[0], report.expectedProviders[1] = report.expectedProviders[1], report.expectedProviders[0]
		}},
		{"expected provider duplicate", func(report *Report) { report.expectedProviders[1] = report.expectedProviders[0] }},
		{"expected provider unknown", func(report *Report) { report.expectedProviders[0] = "planted-provider" }},
		{"actual model missing", func(report *Report) { report.models = report.models[1:] }},
		{"actual model reordered", func(report *Report) { report.models[0], report.models[1] = report.models[1], report.models[0] }},
		{"actual model duplicate", func(report *Report) { report.models[1] = report.models[0] }},
		{"actual model invalid", func(report *Report) { report.models[0] = "planted/model" }},
		{"expected model missing", func(report *Report) { report.expectedModels = report.expectedModels[1:] }},
		{"expected model reordered", func(report *Report) {
			report.expectedModels[0], report.expectedModels[1] = report.expectedModels[1], report.expectedModels[0]
		}},
		{"expected model duplicate", func(report *Report) { report.expectedModels[1] = report.expectedModels[0] }},
		{"expected model invalid", func(report *Report) { report.expectedModels[0] = "planted/model" }},
		{"range map nil", func(report *Report) { report.expectedRanges = nil }},
		{"range missing", func(report *Report) { delete(report.expectedRanges, core.ProviderClaude) }},
		{"range extra", func(report *Report) { report.expectedRanges["planted-provider"] = reportTestRange() }},
		{"range empty", func(report *Report) { report.expectedRanges[core.ProviderCodex] = provider.Range{} }},
		{"range equal", func(report *Report) {
			report.expectedRanges[core.ProviderCodex] = provider.Range{
				MinInclusive: provider.Version{Major: 1},
				MaxExclusive: provider.Version{Major: 1},
			}
		}},
		{"range reversed", func(report *Report) {
			report.expectedRanges[core.ProviderCodex] = provider.Range{
				MinInclusive: provider.Version{Major: 2},
				MaxExclusive: provider.Version{Major: 1},
			}
		}},
		{"range valid but reclassifies ready version", func(report *Report) {
			report.expectedRanges[core.ProviderCodex] = provider.Range{
				MinInclusive: provider.Version{Major: 1, Minor: 3},
				MaxExclusive: provider.Version{Major: 2},
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := base.clone()
			test.mutate(&report)
			assertReportInvalid(t, report)
		})
	}
}

func TestWritersRejectLiteralZeroAndPartialReports(t *testing.T) {
	assertReportInvalid(t, Report{})

	providers := []core.ProviderName{core.ProviderCodex}
	builder := newReportBuilder(providers, []string{"model-a"}, reportTestRanges(providers))
	assertReportInvalid(t, builder.report.clone())
	if err := builder.setCore(validCoreChecks()); err != nil {
		t.Fatalf("setCore() error = %v", err)
	}
	assertReportInvalid(t, builder.report.clone())
	if err := builder.setProviders([]Provider{validReadyProvider(core.ProviderCodex)}); err != nil {
		t.Fatalf("setProviders() error = %v", err)
	}
	assertReportInvalid(t, builder.report.clone())
}

func TestValidateReportRejectsJointlyForgedMembershipAndAliases(t *testing.T) {
	base := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderCodex),
	}, []string{"model-a"})

	t.Run("zero provider membership", func(t *testing.T) {
		report := base.clone()
		report.providers = nil
		report.expectedProviders = nil
		report.expectedRanges = map[core.ProviderName]provider.Range{}
		assertReportInvalid(t, report)
	})
	t.Run("joint unknown provider", func(t *testing.T) {
		report := base.clone()
		report.providers[0].Name = "planted-provider"
		report.expectedProviders[0] = "planted-provider"
		report.expectedRanges = map[core.ProviderName]provider.Range{
			"planted-provider": reportTestRange(),
		}
		assertReportInvalid(t, report)
	})
	t.Run("zero aliases", func(t *testing.T) {
		report := base.clone()
		report.models = nil
		report.expectedModels = nil
		assertReportInvalid(t, report)
	})
	for _, alias := range []string{
		"", "-model", ".model", "model/unsafe", "model unsafe", "model\nunsafe",
		"모델", strings.Repeat("a", 129),
	} {
		t.Run("joint invalid alias "+alias, func(t *testing.T) {
			report := base.clone()
			report.models[0] = alias
			report.expectedModels[0] = alias
			assertReportInvalid(t, report)
		})
	}
}

func TestValidateReportAcceptsAliasBoundaries(t *testing.T) {
	for _, alias := range []string{"a", "Model-1._:x", strings.Repeat("a", 128)} {
		t.Run(alias, func(t *testing.T) {
			report := newCompleteReport(t, validCoreChecks(), []Provider{
				validReadyProvider(core.ProviderCodex),
			}, []string{alias})
			assertReportValid(t, report)
		})
	}
}

func TestValidateReportEnforcesCoreRowsAndLifecycle(t *testing.T) {
	base := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderCodex),
	}, []string{"model-a"})

	validCases := []struct {
		name     string
		checks   []Check
		provider Provider
	}{
		{"all pass", validCoreChecks(), validReadyProvider(core.ProviderCodex)},
		{"pre-root failure", corePreRootFailure(), coreSkippedProvider(core.ProviderCodex)},
		{"root unsafe", coreRootFailure("runtime_unsafe", "runtime root is unsafe"), coreSkippedProvider(core.ProviderCodex)},
		{"root locked", coreRootFailure("runtime_locked", "runtime root is already locked"), coreSkippedProvider(core.ProviderCodex)},
		{"janitor failure cleanup pass", coreJanitorFailure(false), coreSkippedProvider(core.ProviderCodex)},
		{"janitor failure cleanup fail", coreJanitorFailure(true), coreSkippedProvider(core.ProviderCodex)},
		{"containment failure cleanup pass", coreContainmentFailure(false), coreSkippedProvider(core.ProviderCodex)},
		{"containment failure cleanup fail", coreContainmentFailure(true), coreSkippedProvider(core.ProviderCodex)},
		{"cleanup-only failure", coreCleanupFailure(), validReadyProvider(core.ProviderCodex)},
	}
	for index, failure := range []Check{
		{Name: "listener", Status: "fail", Code: "listener_unsafe", Message: "listener is unsafe"},
		{Name: "gateway_auth", Status: "fail", Code: "gateway_key_missing", Message: "gateway authentication is unavailable"},
		{Name: "scheduler", Status: "fail", Code: "scheduler_invalid", Message: "provider scheduler configuration is invalid"},
	} {
		checks := validCoreChecks()
		checks[index] = failure
		for later := 3; later < len(checks); later++ {
			checks[later].Status = checkStatusSkipped
		}
		validCases = append(validCases, struct {
			name     string
			checks   []Check
			provider Provider
		}{"exact " + failure.Name + " failure", checks, coreSkippedProvider(core.ProviderCodex)})
	}
	for _, test := range validCases {
		t.Run("valid "+test.name, func(t *testing.T) {
			report := base.clone()
			report.core = test.checks
			report.providers[0] = test.provider
			assertReportValid(t, report)
		})
	}

	invalidCases := []struct {
		name   string
		mutate func([]Check) []Check
	}{
		{"missing row", func(checks []Check) []Check { return checks[:6] }},
		{"extra row", func(checks []Check) []Check { return append(checks, Check{Name: "extra", Status: "pass"}) }},
		{"reordered rows", func(checks []Check) []Check { checks[0], checks[1] = checks[1], checks[0]; return checks }},
		{"duplicate row", func(checks []Check) []Check { checks[1].Name = checks[0].Name; return checks }},
		{"unknown status", func(checks []Check) []Check { checks[0].Status = "planted-status"; return checks }},
		{"pass with code", func(checks []Check) []Check { checks[0].Code = "listener_unsafe"; return checks }},
		{"pass with message", func(checks []Check) []Check { checks[0].Message = "listener is unsafe"; return checks }},
		{"skipped initial", func(checks []Check) []Check { checks[0].Status = "skipped"; return checks }},
		{"pre-root fail but root pass", func(_ []Check) []Check {
			checks := corePreRootFailure()
			checks[3] = Check{Name: "runtime_root", Status: "pass"}
			return checks
		}},
		{"root fail but janitor pass", func(_ []Check) []Check {
			checks := coreRootFailure("runtime_unsafe", "runtime root is unsafe")
			checks[4] = Check{Name: "runtime_janitor", Status: "pass"}
			return checks
		}},
		{"root pass but janitor skipped", func(checks []Check) []Check { checks[4].Status = "skipped"; return checks }},
		{"janitor fail but containment pass", func(_ []Check) []Check {
			checks := coreJanitorFailure(false)
			checks[5] = Check{Name: "containment", Status: "pass"}
			return checks
		}},
		{"root acquired but cleanup skipped", func(checks []Check) []Check { checks[6].Status = "skipped"; return checks }},
	}
	for _, test := range invalidCases {
		t.Run("invalid "+test.name, func(t *testing.T) {
			report := base.clone()
			report.core = test.mutate(slices.Clone(report.core))
			assertReportInvalid(t, report)
		})
	}

	wrongFailures := []Check{
		{Name: "listener", Status: "fail", Code: "scheduler_invalid", Message: "provider scheduler configuration is invalid"},
		{Name: "gateway_auth", Status: "fail", Code: "gateway_key_missing", Message: "planted-message"},
		{Name: "scheduler", Status: "fail", Code: "", Message: ""},
		{Name: "runtime_root", Status: "fail", Code: "runtime_cleanup_failed", Message: "runtime cleanup failed"},
		{Name: "runtime_janitor", Status: "fail", Code: "containment_failed", Message: "process containment self-test failed"},
		{Name: "containment", Status: "fail", Code: "runtime_cleanup_failed", Message: "runtime cleanup failed"},
		{Name: "probe_cleanup", Status: "fail", Code: "runtime_locked", Message: "runtime root is already locked"},
	}
	for index, failure := range wrongFailures {
		t.Run("wrong failure pair "+failure.Name, func(t *testing.T) {
			report := base.clone()
			report.core[index] = failure
			assertReportInvalid(t, report)
		})
	}
}

func TestValidateReportAcceptsProviderProofCrossProductAndRecomputesStatus(t *testing.T) {
	providers := []core.ProviderName{core.ProviderClaude, core.ProviderCodex, core.ProviderGemini}
	versions := []proofVersion{proofVersionSupported, proofVersionUnsupported, proofVersionUnreadable}
	capabilityStates := []bool{true, false}
	authStates := []proofAuth{proofAuthReady, proofAuthMissing, proofAuthUnknown}

	for _, name := range providers {
		for _, versionState := range versions {
			for _, completeCapabilities := range capabilityStates {
				for _, authState := range authStates {
					row := proofProvider(name, versionState, completeCapabilities, authState)
					testName := string(name) + "/" + string(versionState) + "/" +
						boolName(completeCapabilities) + "/" + string(authState)
					t.Run(testName, func(t *testing.T) {
						report := newCompleteReport(t, validCoreChecks(), []Provider{row}, []string{"model-a"})
						assertReportValid(t, report)
						for _, wrong := range []provider.HealthStatus{
							provider.HealthReady, provider.HealthNotReady, provider.HealthUnknown,
						} {
							if wrong == row.Status {
								continue
							}
							forged := report.clone()
							forged.providers[0].Status = wrong
							assertReportInvalid(t, forged)
						}
					})
				}
			}
		}
	}
}

func TestValidateReportAcceptsOnlyExactPreprobeAndCoreSkippedShapes(t *testing.T) {
	allowed := map[core.ProviderName][]string{
		core.ProviderCodex: {
			provider.ProblemExecutableMissing,
			provider.ProblemExecutableUnsafe,
			provider.ProblemConfigHomeUnsafe,
		},
		core.ProviderClaude: {
			provider.ProblemExecutableMissing,
			provider.ProblemExecutableUnsafe,
			provider.ProblemConfigHomeUnsafe,
			provider.ProblemCredentialMissing,
		},
		core.ProviderGemini: {
			provider.ProblemExecutableMissing,
			provider.ProblemExecutableUnsafe,
			provider.ProblemConfigHomeUnsafe,
			provider.ProblemCredentialMissing,
			provider.ProblemCredentialFileUnsafe,
		},
	}
	for name, problems := range allowed {
		for _, problem := range problems {
			t.Run(string(name)+"/"+problem, func(t *testing.T) {
				row := preprobeProvider(name, problem)
				report := newCompleteReport(t, validCoreChecks(), []Provider{row}, []string{"model-a"})
				assertReportValid(t, report)

				mutations := []func(*Provider){
					func(value *Provider) { value.Status = provider.HealthReady },
					func(value *Provider) { value.Auth = "planted-auth" },
					func(value *Provider) { value.Version = "1.2.3" },
					func(value *Provider) { value.Capabilities = []string{"stdin_prompt"} },
					func(value *Provider) { value.Problems = append(value.Problems, provider.ProblemVersionUnreadable) },
				}
				for _, mutate := range mutations {
					forged := report.clone()
					mutate(&forged.providers[0])
					assertReportInvalid(t, forged)
				}
			})
		}
	}

	for _, name := range []core.ProviderName{core.ProviderClaude, core.ProviderCodex, core.ProviderGemini} {
		t.Run(string(name)+" core skipped", func(t *testing.T) {
			report := newCompleteReport(t, corePreRootFailure(), []Provider{
				coreSkippedProvider(name),
			}, []string{"model-a"})
			assertReportValid(t, report)
			mutations := []func(*Provider){
				func(value *Provider) { value.Status = provider.HealthNotReady },
				func(value *Provider) { value.Auth = "missing" },
				func(value *Provider) { value.Version = "1.2.3" },
				func(value *Provider) { value.Capabilities = []string{"stdin_prompt"} },
				func(value *Provider) { value.Problems = []string{provider.ProblemVersionUnreadable} },
			}
			for _, mutate := range mutations {
				forged := report.clone()
				mutate(&forged.providers[0])
				assertReportInvalid(t, forged)
			}
		})
	}
}

func TestValidateReportRejectsMalformedProviderFieldsAndRelationships(t *testing.T) {
	base := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderCodex),
	}, []string{"model-a"})

	tests := []struct {
		name   string
		mutate func(*Provider)
	}{
		{"unknown status", func(row *Provider) { row.Status = "planted-status" }},
		{"noncanonical version", func(row *Provider) { row.Version = "01.2.3" }},
		{"decorated version", func(row *Provider) { row.Version = "codex 1.2.3" }},
		{"multiple versions", func(row *Provider) { row.Version = "1.2.3 1.2.4" }},
		{"invalid UTF-8 version", func(row *Provider) { row.Version = string([]byte{0xff}) }},
		{"out of range without problem", func(row *Provider) { row.Version = "2.0.0"; row.Status = provider.HealthNotReady }},
		{"in range unsupported", func(row *Provider) {
			row.Status = provider.HealthNotReady
			row.Problems = []string{provider.ProblemVersionUnsupported}
		}},
		{"partial capabilities", func(row *Provider) { row.Status = provider.HealthNotReady; row.Capabilities = row.Capabilities[:1] }},
		{"empty capabilities no problem", func(row *Provider) { row.Status = provider.HealthNotReady; row.Capabilities = nil }},
		{"duplicate capabilities", func(row *Provider) {
			row.Capabilities = append(row.Capabilities, row.Capabilities[len(row.Capabilities)-1])
		}},
		{"unsorted capabilities", func(row *Provider) {
			row.Capabilities[0], row.Capabilities[1] = row.Capabilities[1], row.Capabilities[0]
		}},
		{"unknown capability", func(row *Provider) { row.Capabilities[0] = "planted-capability" }},
		{"cross-provider capabilities", func(row *Provider) { row.Capabilities = readyCapabilities(core.ProviderClaude) }},
		{"cross-provider auth", func(row *Provider) { row.Auth = "configured" }},
		{"unknown auth", func(row *Provider) { row.Auth = "planted-auth" }},
		{"unknown problem", func(row *Provider) { row.Status = provider.HealthNotReady; row.Problems = []string{"planted-problem"} }},
		{"duplicate problems", func(row *Provider) {
			row.Status = provider.HealthNotReady
			row.Auth = "unknown"
			row.Problems = []string{provider.ProblemAuthUnknown, provider.ProblemAuthUnknown}
		}},
		{"unsorted problems", func(row *Provider) {
			row.Status = provider.HealthNotReady
			row.Auth = "unknown"
			row.Capabilities = nil
			row.Version = ""
			row.Problems = []string{provider.ProblemVersionUnreadable, provider.ProblemCapabilityMissing, provider.ProblemAuthUnknown}
		}},
		{"path problem mixed with proof", func(row *Provider) {
			row.Status = provider.HealthNotReady
			row.Problems = []string{provider.ProblemExecutableUnsafe}
		}},
		{"ready proof labelled not ready", func(row *Provider) { row.Status = provider.HealthNotReady }},
		{"ready proof labelled unknown", func(row *Provider) { row.Status = provider.HealthUnknown }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := base.clone()
			test.mutate(&report.providers[0])
			assertReportInvalid(t, report)
		})
	}

	for _, name := range []core.ProviderName{core.ProviderClaude, core.ProviderCodex, core.ProviderGemini} {
		t.Run(string(name)+" malformed fallback", func(t *testing.T) {
			row := Provider{
				Name:   name,
				Status: provider.HealthNotReady,
				Auth:   "unknown",
				Problems: []string{
					provider.ProblemAuthUnknown,
					provider.ProblemCapabilityMissing,
					provider.ProblemVersionUnreadable,
				},
			}
			report := newCompleteReport(t, validCoreChecks(), []Provider{row}, []string{"model-a"})
			assertReportValid(t, report)
		})
	}

	disallowedPreprobe := []Provider{
		preprobeProvider(core.ProviderCodex, provider.ProblemCredentialMissing),
		preprobeProvider(core.ProviderClaude, provider.ProblemCredentialFileUnsafe),
		preprobeProvider(core.ProviderGemini, provider.ProblemAuthMissing),
	}
	for _, row := range disallowedPreprobe {
		t.Run(string(row.Name)+" disallowed problem "+row.Problems[0], func(t *testing.T) {
			report := base.clone()
			report.providers[0] = row
			report.providers[0].Name = core.ProviderCodex
			if row.Name != core.ProviderCodex {
				report.expectedProviders[0] = row.Name
				report.providers[0].Name = row.Name
				report.expectedRanges = reportTestRanges([]core.ProviderName{row.Name})
			}
			assertReportInvalid(t, report)
		})
	}
}

func TestValidateReportEnforcesCoreSkippedSuffixAfterCleanupFailure(t *testing.T) {
	providers := []Provider{
		validReadyProvider(core.ProviderClaude),
		validReadyProvider(core.ProviderCodex),
		validReadyProvider(core.ProviderGemini),
	}
	base := newCompleteReport(t, coreCleanupFailure(), providers, []string{"model-a"})

	for cut := 1; cut <= len(providers); cut++ {
		t.Run("canonical prefix", func(t *testing.T) {
			report := base.clone()
			for index := cut; index < len(report.providers); index++ {
				report.providers[index] = coreSkippedProvider(report.providers[index].Name)
			}
			assertReportValid(t, report)
		})
	}
	t.Run("earlier preprobe then current proof then skipped", func(t *testing.T) {
		report := base.clone()
		report.providers[0] = preprobeProvider(
			core.ProviderClaude,
			provider.ProblemCredentialMissing,
		)
		report.providers[2] = coreSkippedProvider(core.ProviderGemini)
		assertReportValid(t, report)
	})

	t.Run("all skipped after otherwise passing core", func(t *testing.T) {
		report := base.clone()
		for index := range report.providers {
			report.providers[index] = coreSkippedProvider(report.providers[index].Name)
		}
		assertReportInvalid(t, report)
	})
	t.Run("preprobe-only prefix before skipped suffix", func(t *testing.T) {
		report := base.clone()
		report.providers[0] = preprobeProvider(
			core.ProviderClaude,
			provider.ProblemCredentialMissing,
		)
		report.providers[1] = coreSkippedProvider(core.ProviderCodex)
		report.providers[2] = coreSkippedProvider(core.ProviderGemini)
		assertReportInvalid(t, report)
	})
	t.Run("proof then preprobe before skipped suffix", func(t *testing.T) {
		report := base.clone()
		report.providers[1] = preprobeProvider(
			core.ProviderCodex,
			provider.ProblemExecutableMissing,
		)
		report.providers[2] = coreSkippedProvider(core.ProviderGemini)
		assertReportInvalid(t, report)
	})
	t.Run("canonical after skipped", func(t *testing.T) {
		report := base.clone()
		report.providers[0] = coreSkippedProvider(report.providers[0].Name)
		assertReportInvalid(t, report)
	})
	t.Run("skipped with cleanup pass", func(t *testing.T) {
		report := base.clone()
		report.core = validCoreChecks()
		report.providers[2] = coreSkippedProvider(report.providers[2].Name)
		assertReportInvalid(t, report)
	})
	t.Run("non-skipped with pre-provider core failure", func(t *testing.T) {
		report := base.clone()
		report.core = coreContainmentFailure(false)
		assertReportInvalid(t, report)
	})
}

func TestReportReadinessFailsClosedAndCountsCleanupEvidence(t *testing.T) {
	valid := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderClaude),
		proofProvider(core.ProviderCodex, proofVersionUnreadable, false, proofAuthUnknown),
		validReadyProvider(core.ProviderGemini),
	}, []string{"model-a"})
	if !valid.CoreReady() || valid.ReadyCount() != 2 {
		t.Fatalf("valid readiness = %v/%d, want true/2", valid.CoreReady(), valid.ReadyCount())
	}

	cleanupFailure := valid.clone()
	cleanupFailure.core = coreCleanupFailure()
	if cleanupFailure.CoreReady() || cleanupFailure.ReadyCount() != 2 {
		t.Fatalf("cleanup evidence readiness = %v/%d, want false/2", cleanupFailure.CoreReady(), cleanupFailure.ReadyCount())
	}

	corrupted := valid.clone()
	corrupted.models[0] = "planted/model"
	if corrupted.CoreReady() || corrupted.ReadyCount() != 0 {
		t.Fatalf("corrupt readiness = %v/%d, want false/0", corrupted.CoreReady(), corrupted.ReadyCount())
	}
}

func TestWritersEmitPinnedDeterministicJSONAndText(t *testing.T) {
	report := newCompleteReport(t, validCoreChecks(), []Provider{
		preprobeProvider(core.ProviderClaude, provider.ProblemCredentialMissing),
		validReadyProvider(core.ProviderCodex),
		{
			Name:   core.ProviderGemini,
			Status: provider.HealthNotReady,
			Auth:   "unknown",
			Problems: []string{
				provider.ProblemAuthUnknown,
				provider.ProblemCapabilityMissing,
				provider.ProblemVersionUnreadable,
			},
		},
	}, []string{"model-z", "model-a"})

	wantJSON := "{\"core\":[" +
		"{\"name\":\"listener\",\"status\":\"pass\"}," +
		"{\"name\":\"gateway_auth\",\"status\":\"pass\"}," +
		"{\"name\":\"scheduler\",\"status\":\"pass\"}," +
		"{\"name\":\"runtime_root\",\"status\":\"pass\"}," +
		"{\"name\":\"runtime_janitor\",\"status\":\"pass\"}," +
		"{\"name\":\"containment\",\"status\":\"pass\"}," +
		"{\"name\":\"probe_cleanup\",\"status\":\"pass\"}]," +
		"\"providers\":[" +
		"{\"name\":\"claude\",\"status\":\"not_ready\",\"auth\":\"missing\",\"capabilities\":[],\"problems\":[\"credential_missing\"]}," +
		"{\"name\":\"codex\",\"status\":\"ready\",\"version\":\"1.2.3\",\"auth\":\"authenticated\",\"capabilities\":[\"ephemeral\",\"feature_hardening\",\"never_approve\",\"read_only\",\"schema_file\",\"stdin_prompt\"]}," +
		"{\"name\":\"gemini\",\"status\":\"not_ready\",\"auth\":\"unknown\",\"capabilities\":[],\"problems\":[\"auth_unknown\",\"capability_missing\",\"version_unreadable\"]}]," +
		"\"models\":[\"model-a\",\"model-z\"]}\n"
	wantText := "core:\n" +
		"listener\tpass\t-\t-\n" +
		"gateway_auth\tpass\t-\t-\n" +
		"scheduler\tpass\t-\t-\n" +
		"runtime_root\tpass\t-\t-\n" +
		"runtime_janitor\tpass\t-\t-\n" +
		"containment\tpass\t-\t-\n" +
		"probe_cleanup\tpass\t-\t-\n" +
		"providers:\n" +
		"claude\tnot_ready\t-\tmissing\t-\tcredential_missing\n" +
		"codex\tready\t1.2.3\tauthenticated\tephemeral,feature_hardening,never_approve,read_only,schema_file,stdin_prompt\t-\n" +
		"gemini\tnot_ready\t-\tunknown\t-\tauth_unknown,capability_missing,version_unreadable\n" +
		"models:\n" +
		"model-a\n" +
		"model-z\n"

	for iteration := 0; iteration < 2; iteration++ {
		var jsonOutput bytes.Buffer
		if err := WriteJSON(&jsonOutput, report); err != nil {
			t.Fatalf("WriteJSON() error = %v", err)
		}
		if got := jsonOutput.String(); got != wantJSON {
			t.Fatalf("WriteJSON() = %q, want %q", got, wantJSON)
		}
		if !strings.HasSuffix(jsonOutput.String(), "\n") || strings.HasSuffix(jsonOutput.String(), "\n\n") {
			t.Fatalf("JSON final newline is not exact: %q", jsonOutput.String())
		}

		var textOutput bytes.Buffer
		if err := WriteText(&textOutput, report); err != nil {
			t.Fatalf("WriteText() error = %v", err)
		}
		if got := textOutput.String(); got != wantText {
			t.Fatalf("WriteText() = %q, want %q", got, wantText)
		}
		if !strings.HasSuffix(textOutput.String(), "\n") || strings.HasSuffix(textOutput.String(), "\n\n") {
			t.Fatalf("text final newline is not exact: %q", textOutput.String())
		}
	}
}

func TestWritersValidateBeforeWritingAndCollapseWriterFailures(t *testing.T) {
	report := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderCodex),
	}, []string{"model-a"})
	planted := errors.New("planted-writer-secret")

	for _, write := range []struct {
		name string
		fn   func(io.Writer, Report) error
	}{
		{"json", WriteJSON},
		{"text", WriteText},
	} {
		t.Run(write.name+" writer error", func(t *testing.T) {
			writer := &recordingWriter{err: planted}
			err := write.fn(writer, report)
			assertExactError(t, err, ErrReportWrite)
			if writer.calls != 1 || strings.Contains(err.Error(), planted.Error()) || errors.Is(err, planted) {
				t.Fatalf("writer failure calls/error = %d/%v", writer.calls, err)
			}
		})
		t.Run(write.name+" short writer", func(t *testing.T) {
			writer := &recordingWriter{short: true}
			assertExactError(t, write.fn(writer, report), ErrReportWrite)
			if writer.calls != 1 {
				t.Fatalf("writer calls = %d, want 1", writer.calls)
			}
		})
		t.Run(write.name+" nil writer", func(t *testing.T) {
			assertExactError(t, write.fn(nil, report), ErrReportWrite)
		})
		t.Run(write.name+" invalid before writer", func(t *testing.T) {
			invalid := report.clone()
			invalid.phase = reportPhaseCore
			writer := &recordingWriter{err: planted}
			assertExactError(t, write.fn(writer, invalid), ErrInvalidReport)
			if writer.calls != 0 {
				t.Fatalf("invalid report reached writer %d times", writer.calls)
			}
		})
	}
}

func TestWritersRejectSecretBearingForgedReportsWithoutWriting(t *testing.T) {
	const secret = "planted-secret-14c-901"
	base := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderCodex),
	}, []string{"model-a"})

	mutations := []struct {
		name   string
		mutate func(*Report)
	}{
		{"core name", func(report *Report) { report.core[0].Name = secret }},
		{"core status", func(report *Report) { report.core[0].Status = secret }},
		{"core code", func(report *Report) { report.core[0].Code = secret }},
		{"core message", func(report *Report) { report.core[0].Message = secret }},
		{"provider name", func(report *Report) { report.providers[0].Name = core.ProviderName(secret) }},
		{"provider status", func(report *Report) { report.providers[0].Status = provider.HealthStatus(secret) }},
		{"provider version", func(report *Report) { report.providers[0].Version = secret }},
		{"provider auth", func(report *Report) { report.providers[0].Auth = secret }},
		{"provider capability", func(report *Report) { report.providers[0].Capabilities[0] = secret }},
		{"provider problem", func(report *Report) { report.providers[0].Problems = []string{secret} }},
		{"model", func(report *Report) { report.models[0] = secret + "/unsafe" }},
		{"expected model", func(report *Report) { report.expectedModels[0] = secret + "/unsafe" }},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			report := base.clone()
			mutation.mutate(&report)
			for _, write := range []struct {
				name string
				fn   func(io.Writer, Report) error
			}{
				{"json", WriteJSON},
				{"text", WriteText},
			} {
				t.Run(write.name, func(t *testing.T) {
					var output bytes.Buffer
					err := write.fn(&output, report)
					assertExactError(t, err, ErrInvalidReport)
					if output.Len() != 0 || strings.Contains(output.String(), secret) || strings.Contains(err.Error(), secret) {
						t.Fatalf("secret-bearing rejection output/error = %q/%v", output.String(), err)
					}
				})
			}
		})
	}
}

type proofVersion string

const (
	proofVersionSupported   proofVersion = "supported"
	proofVersionUnsupported proofVersion = "unsupported"
	proofVersionUnreadable  proofVersion = "unreadable"
)

type proofAuth string

const (
	proofAuthReady   proofAuth = "ready"
	proofAuthMissing proofAuth = "missing"
	proofAuthUnknown proofAuth = "unknown"
)

func proofProvider(
	name core.ProviderName,
	versionState proofVersion,
	completeCapabilities bool,
	authState proofAuth,
) Provider {
	row := Provider{Name: name, Status: provider.HealthNotReady}
	switch versionState {
	case proofVersionSupported:
		row.Version = "1.2.3"
	case proofVersionUnsupported:
		row.Version = "2.0.0"
		row.Problems = append(row.Problems, provider.ProblemVersionUnsupported)
	case proofVersionUnreadable:
		row.Problems = append(row.Problems, provider.ProblemVersionUnreadable)
	default:
		panic("unknown proof version")
	}

	if completeCapabilities {
		row.Capabilities = readyCapabilities(name)
		if row.Capabilities == nil {
			panic("unknown provider")
		}
	} else {
		row.Problems = append(row.Problems, provider.ProblemCapabilityMissing)
	}

	switch authState {
	case proofAuthReady:
		if name == core.ProviderGemini {
			row.Auth = "configured"
		} else {
			row.Auth = "authenticated"
		}
	case proofAuthMissing:
		row.Auth = "missing"
		if name == core.ProviderGemini {
			row.Problems = append(row.Problems, provider.ProblemCredentialMissing)
		} else {
			row.Problems = append(row.Problems, provider.ProblemAuthMissing)
		}
	case proofAuthUnknown:
		row.Auth = "unknown"
		row.Problems = append(row.Problems, provider.ProblemAuthUnknown)
	default:
		panic("unknown proof auth")
	}
	slices.Sort(row.Problems)

	allReady := versionState == proofVersionSupported && completeCapabilities && authState == proofAuthReady
	unknown := versionState == proofVersionSupported && completeCapabilities && authState == proofAuthUnknown &&
		name != core.ProviderGemini
	switch {
	case allReady:
		row.Status = provider.HealthReady
	case unknown:
		row.Status = provider.HealthUnknown
	default:
		row.Status = provider.HealthNotReady
	}
	return row
}

func preprobeProvider(name core.ProviderName, problem string) Provider {
	auth := "unknown"
	if problem == provider.ProblemCredentialMissing {
		auth = "missing"
	}
	return Provider{
		Name:     name,
		Status:   provider.HealthNotReady,
		Auth:     auth,
		Problems: []string{problem},
	}
}

func coreSkippedProvider(name core.ProviderName) Provider {
	return Provider{
		Name:   name,
		Status: provider.HealthUnknown,
		Auth:   "unknown",
	}
}

func corePreRootFailure() []Check {
	checks := validCoreChecks()
	checks[0] = Check{
		Name:    "listener",
		Status:  "fail",
		Code:    "listener_unsafe",
		Message: "listener is unsafe",
	}
	for index := 3; index < len(checks); index++ {
		checks[index].Status = "skipped"
	}
	return checks
}

func coreRootFailure(code, message string) []Check {
	checks := validCoreChecks()
	checks[3] = Check{Name: "runtime_root", Status: "fail", Code: code, Message: message}
	for index := 4; index < len(checks); index++ {
		checks[index].Status = "skipped"
	}
	return checks
}

func coreJanitorFailure(cleanupFailed bool) []Check {
	checks := validCoreChecks()
	checks[4] = cleanupFailureCheck("runtime_janitor")
	checks[5].Status = "skipped"
	if cleanupFailed {
		checks[6] = cleanupFailureCheck("probe_cleanup")
	}
	return checks
}

func coreContainmentFailure(cleanupFailed bool) []Check {
	checks := validCoreChecks()
	checks[5] = Check{
		Name:    "containment",
		Status:  "fail",
		Code:    "containment_failed",
		Message: "process containment self-test failed",
	}
	if cleanupFailed {
		checks[6] = cleanupFailureCheck("probe_cleanup")
	}
	return checks
}

func coreCleanupFailure() []Check {
	checks := validCoreChecks()
	checks[6] = cleanupFailureCheck("probe_cleanup")
	return checks
}

func cleanupFailureCheck(name string) Check {
	return Check{
		Name:    name,
		Status:  "fail",
		Code:    "runtime_cleanup_failed",
		Message: "runtime cleanup failed",
	}
}

func boolName(value bool) string {
	if value {
		return "complete"
	}
	return "missing"
}

func assertReportValid(t *testing.T, report Report) {
	t.Helper()
	if err := validateReport(report); err != nil {
		t.Fatalf("validateReport() error = %v", err)
	}
}

func assertReportInvalid(t *testing.T, report Report) {
	t.Helper()
	assertExactError(t, validateReport(report), ErrInvalidReport)
	if report.CoreReady() || report.ReadyCount() != 0 {
		t.Fatalf("invalid report readiness = %v/%d, want false/0", report.CoreReady(), report.ReadyCount())
	}
	for _, write := range []func(io.Writer, Report) error{WriteJSON, WriteText} {
		var output bytes.Buffer
		assertExactError(t, write(&output, report), ErrInvalidReport)
		if output.Len() != 0 {
			t.Fatalf("invalid report wrote %q", output.String())
		}
	}
}

type recordingWriter struct {
	calls int
	err   error
	short bool
}

func (w *recordingWriter) Write(payload []byte) (int, error) {
	w.calls++
	if w.err != nil {
		return 0, w.err
	}
	if w.short && len(payload) > 0 {
		return len(payload) - 1, nil
	}
	return len(payload), nil
}
