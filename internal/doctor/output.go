package doctor

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

const (
	checkStatusPass    = "pass"
	checkStatusFail    = "fail"
	checkStatusSkipped = "skipped"

	maxReportProviders = 3
	maxReportModels    = 1_024
)

type coreReportState struct {
	providersMustSkip bool
	cleanupFailed     bool
}

type reportDTO struct {
	Core      []checkDTO    `json:"core"`
	Providers []providerDTO `json:"providers"`
	Models    []string      `json:"models"`
}

type checkDTO struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type providerDTO struct {
	Name         core.ProviderName     `json:"name"`
	Status       provider.HealthStatus `json:"status"`
	Version      string                `json:"version,omitempty"`
	Auth         string                `json:"auth"`
	Capabilities []string              `json:"capabilities"`
	Problems     []string              `json:"problems,omitempty"`
}

// WriteJSON validates and writes one compact closed JSON report.
func WriteJSON(writer io.Writer, report Report) error {
	if err := validateReport(report); err != nil {
		return err
	}
	dto := makeReportDTO(report)
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(dto); err != nil {
		return ErrReportWrite
	}
	return writeReportPayload(writer, payload.Bytes())
}

// WriteText validates and writes the fixed three-section tab-separated report.
func WriteText(writer io.Writer, report Report) error {
	if err := validateReport(report); err != nil {
		return err
	}
	dto := makeReportDTO(report)
	payload := make([]byte, 0, 512)
	payload = append(payload, "core:\n"...)
	for _, check := range dto.Core {
		payload = appendTextColumn(payload, check.Name)
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, check.Status)
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, textValue(check.Code))
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, textValue(check.Message))
		payload = append(payload, '\n')
	}
	payload = append(payload, "providers:\n"...)
	for _, result := range dto.Providers {
		payload = appendTextColumn(payload, string(result.Name))
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, string(result.Status))
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, textValue(result.Version))
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, result.Auth)
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, textList(result.Capabilities))
		payload = append(payload, '\t')
		payload = appendTextColumn(payload, textList(result.Problems))
		payload = append(payload, '\n')
	}
	payload = append(payload, "models:\n"...)
	for _, model := range dto.Models {
		payload = appendTextColumn(payload, model)
		payload = append(payload, '\n')
	}
	return writeReportPayload(writer, payload)
}

func validateReport(report Report) error {
	if !report.constructed || report.phase != reportPhaseComplete {
		return ErrInvalidReport
	}
	coreState, valid := validateCoreReport(report.core)
	if !valid ||
		!validateProviderProvenance(report) ||
		!validateModelProvenance(report) ||
		!validateProviderRows(report.providers, report.expectedRanges, coreState) {
		return ErrInvalidReport
	}
	return nil
}

func validateCoreReport(checks []Check) (coreReportState, bool) {
	names := [...]string{
		"listener",
		"gateway_auth",
		"scheduler",
		"runtime_root",
		"runtime_janitor",
		"containment",
		"probe_cleanup",
	}
	if len(checks) != len(names) {
		return coreReportState{}, false
	}
	for index, name := range names {
		check := checks[index]
		if check.Name != name || !validCheckResult(check) {
			return coreReportState{}, false
		}
	}
	for index := 0; index < 3; index++ {
		if checks[index].Status == checkStatusSkipped {
			return coreReportState{}, false
		}
	}
	preRootReady := checks[0].Status == checkStatusPass &&
		checks[1].Status == checkStatusPass &&
		checks[2].Status == checkStatusPass
	if !preRootReady {
		return coreReportState{providersMustSkip: true},
			allChecksHaveStatus(checks[3:], checkStatusSkipped)
	}

	if checks[3].Status == checkStatusSkipped {
		return coreReportState{}, false
	}
	if checks[3].Status == checkStatusFail {
		return coreReportState{providersMustSkip: true},
			allChecksHaveStatus(checks[4:], checkStatusSkipped)
	}
	if checks[4].Status == checkStatusSkipped || checks[6].Status == checkStatusSkipped {
		return coreReportState{}, false
	}
	if checks[4].Status == checkStatusFail {
		return coreReportState{providersMustSkip: true},
			checks[5].Status == checkStatusSkipped
	}
	if checks[5].Status == checkStatusSkipped {
		return coreReportState{}, false
	}
	if checks[5].Status == checkStatusFail {
		return coreReportState{providersMustSkip: true}, true
	}
	return coreReportState{
		cleanupFailed: checks[6].Status == checkStatusFail,
	}, true
}

func validCheckResult(check Check) bool {
	switch check.Status {
	case checkStatusPass, checkStatusSkipped:
		return check.Code == "" && check.Message == ""
	case checkStatusFail:
		return validCheckFailure(check)
	default:
		return false
	}
}

func validCheckFailure(check Check) bool {
	switch check.Name {
	case "listener":
		return check.Code == "listener_unsafe" && check.Message == "listener is unsafe"
	case "gateway_auth":
		return check.Code == "gateway_key_missing" &&
			check.Message == "gateway authentication is unavailable"
	case "scheduler":
		return check.Code == "scheduler_invalid" &&
			check.Message == "provider scheduler configuration is invalid"
	case "runtime_root":
		return (check.Code == "runtime_unsafe" && check.Message == "runtime root is unsafe") ||
			(check.Code == "runtime_locked" && check.Message == "runtime root is already locked")
	case "runtime_janitor", "probe_cleanup":
		return check.Code == "runtime_cleanup_failed" && check.Message == "runtime cleanup failed"
	case "containment":
		return check.Code == "containment_failed" &&
			check.Message == "process containment self-test failed"
	default:
		return false
	}
}

func allChecksHaveStatus(checks []Check, status string) bool {
	for _, check := range checks {
		if check.Status != status {
			return false
		}
	}
	return true
}

func validateProviderProvenance(report Report) bool {
	if len(report.expectedProviders) == 0 ||
		len(report.expectedProviders) > maxReportProviders ||
		len(report.providers) != len(report.expectedProviders) ||
		len(report.expectedRanges) != len(report.expectedProviders) {
		return false
	}
	for index, expected := range report.expectedProviders {
		if !knownReportProvider(expected) ||
			(index > 0 && report.expectedProviders[index-1] >= expected) ||
			report.providers[index].Name != expected {
			return false
		}
		interval, present := report.expectedRanges[expected]
		if !present || !interval.Contains(interval.MinInclusive) {
			return false
		}
	}
	for index, result := range report.providers {
		if !knownReportProvider(result.Name) ||
			(index > 0 && report.providers[index-1].Name >= result.Name) {
			return false
		}
	}
	return true
}

func validateModelProvenance(report Report) bool {
	if len(report.expectedModels) == 0 ||
		len(report.expectedModels) > maxReportModels ||
		len(report.models) != len(report.expectedModels) {
		return false
	}
	for index, expected := range report.expectedModels {
		if !validModelAlias(expected) || report.models[index] != expected ||
			(index > 0 && report.expectedModels[index-1] >= expected) {
			return false
		}
	}
	for index, actual := range report.models {
		if !validModelAlias(actual) || (index > 0 && report.models[index-1] >= actual) {
			return false
		}
	}
	return true
}

func validModelAlias(alias string) bool {
	if len(alias) == 0 || len(alias) > 128 || !asciiAlphaNumeric(alias[0]) {
		return false
	}
	for index := 1; index < len(alias); index++ {
		if !asciiAlphaNumeric(alias[index]) {
			switch alias[index] {
			case '.', '_', ':', '-':
			default:
				return false
			}
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9'
}

func knownReportProvider(name core.ProviderName) bool {
	switch name {
	case core.ProviderClaude, core.ProviderCodex, core.ProviderGemini:
		return true
	default:
		return false
	}
}

func validateProviderRows(
	rows []Provider,
	ranges map[core.ProviderName]provider.Range,
	coreState coreReportState,
) bool {
	if coreState.providersMustSkip {
		for _, row := range rows {
			if !coreSkippedProviderRow(row) {
				return false
			}
		}
		return true
	}

	seenSkipped := false
	canonicalRows := 0
	lastRowWasProof := false
	for _, row := range rows {
		if coreSkippedProviderRow(row) {
			if !coreState.cleanupFailed || !lastRowWasProof {
				return false
			}
			seenSkipped = true
			continue
		}
		valid, proof := validateProviderRow(row, ranges[row.Name])
		if seenSkipped || !valid {
			return false
		}
		canonicalRows++
		lastRowWasProof = proof
	}
	return !coreState.cleanupFailed || canonicalRows > 0
}

func validateProviderRow(row Provider, interval provider.Range) (valid, proof bool) {
	if !strictlySortedUnique(row.Capabilities) ||
		!strictlySortedUnique(row.Problems) ||
		!validProviderAuthValue(row.Name, row.Auth) ||
		!allProviderProblemsAllowed(row.Name, row.Problems) {
		return false, false
	}
	if preprobeProviderRow(row) {
		return true, false
	}
	if providerProofRow(row, interval) {
		return true, true
	}
	return false, false
}

func coreSkippedProviderRow(row Provider) bool {
	return row.Status == provider.HealthUnknown &&
		row.Version == "" &&
		row.Auth == "unknown" &&
		len(row.Capabilities) == 0 &&
		len(row.Problems) == 0
}

func preprobeProviderRow(row Provider) bool {
	if row.Status != provider.HealthNotReady ||
		row.Version != "" ||
		len(row.Capabilities) != 0 ||
		len(row.Problems) != 1 {
		return false
	}
	problem := row.Problems[0]
	if !validPreprobeProblem(row.Name, problem) {
		return false
	}
	if problem == provider.ProblemCredentialMissing {
		return row.Auth == "missing"
	}
	return row.Auth == "unknown"
}

func validPreprobeProblem(name core.ProviderName, problem string) bool {
	switch problem {
	case provider.ProblemExecutableMissing,
		provider.ProblemExecutableUnsafe,
		provider.ProblemConfigHomeUnsafe:
		return true
	case provider.ProblemCredentialMissing:
		return name == core.ProviderClaude || name == core.ProviderGemini
	case provider.ProblemCredentialFileUnsafe:
		return name == core.ProviderGemini
	default:
		return false
	}
}

func providerProofRow(row Provider, interval provider.Range) bool {
	expectedProblems := make([]string, 0, 3)
	versionReady, valid := validateProviderVersion(row.Version, interval, &expectedProblems)
	if !valid {
		return false
	}
	capabilitiesReady, valid := validateProviderCapabilities(row, &expectedProblems)
	if !valid {
		return false
	}
	authReady, authUnknown, valid := validateProviderAuth(row, &expectedProblems)
	if !valid {
		return false
	}
	slices.Sort(expectedProblems)
	if !slices.Equal(row.Problems, expectedProblems) {
		return false
	}

	expectedStatus := provider.HealthNotReady
	switch {
	case versionReady && capabilitiesReady && authReady:
		expectedStatus = provider.HealthReady
	case versionReady && capabilitiesReady && authUnknown && row.Name != core.ProviderGemini:
		expectedStatus = provider.HealthUnknown
	}
	return row.Status == expectedStatus
}

func validateProviderVersion(
	version string,
	interval provider.Range,
	expectedProblems *[]string,
) (bool, bool) {
	if version == "" {
		*expectedProblems = append(*expectedProblems, provider.ProblemVersionUnreadable)
		return false, true
	}
	parsed, err := provider.ParseVersion(version)
	if err != nil || parsed.String() != version {
		return false, false
	}
	if !interval.Contains(parsed) {
		*expectedProblems = append(*expectedProblems, provider.ProblemVersionUnsupported)
		return false, true
	}
	return true, true
}

func validateProviderCapabilities(
	row Provider,
	expectedProblems *[]string,
) (bool, bool) {
	expected := readyCapabilities(row.Name)
	if slices.Equal(row.Capabilities, expected) {
		return true, true
	}
	if len(row.Capabilities) == 0 {
		*expectedProblems = append(*expectedProblems, provider.ProblemCapabilityMissing)
		return false, true
	}
	return false, false
}

func validateProviderAuth(
	row Provider,
	expectedProblems *[]string,
) (ready, unknown, valid bool) {
	switch row.Name {
	case core.ProviderClaude, core.ProviderCodex:
		switch row.Auth {
		case "authenticated":
			return true, false, true
		case "missing":
			*expectedProblems = append(*expectedProblems, provider.ProblemAuthMissing)
			return false, false, true
		case "unknown":
			*expectedProblems = append(*expectedProblems, provider.ProblemAuthUnknown)
			return false, true, true
		default:
			return false, false, false
		}
	case core.ProviderGemini:
		switch row.Auth {
		case "configured":
			return true, false, true
		case "missing":
			*expectedProblems = append(*expectedProblems, provider.ProblemCredentialMissing)
			return false, false, true
		case "unknown":
			*expectedProblems = append(*expectedProblems, provider.ProblemAuthUnknown)
			return false, true, true
		default:
			return false, false, false
		}
	default:
		return false, false, false
	}
}

func validProviderAuthValue(name core.ProviderName, auth string) bool {
	switch name {
	case core.ProviderClaude, core.ProviderCodex:
		return auth == "authenticated" || auth == "missing" || auth == "unknown"
	case core.ProviderGemini:
		return auth == "configured" || auth == "missing" || auth == "unknown"
	default:
		return false
	}
}

func allProviderProblemsAllowed(name core.ProviderName, problems []string) bool {
	for _, problem := range problems {
		if !providerProblemAllowed(name, problem) {
			return false
		}
	}
	return true
}

func providerProblemAllowed(name core.ProviderName, problem string) bool {
	switch name {
	case core.ProviderCodex:
		switch problem {
		case provider.ProblemExecutableMissing,
			provider.ProblemExecutableUnsafe,
			provider.ProblemVersionUnreadable,
			provider.ProblemVersionUnsupported,
			provider.ProblemCapabilityMissing,
			provider.ProblemConfigHomeUnsafe,
			provider.ProblemAuthMissing,
			provider.ProblemAuthUnknown:
			return true
		default:
			return false
		}
	case core.ProviderClaude:
		return problem == provider.ProblemCredentialMissing ||
			providerProblemAllowed(core.ProviderCodex, problem)
	case core.ProviderGemini:
		switch problem {
		case provider.ProblemExecutableMissing,
			provider.ProblemExecutableUnsafe,
			provider.ProblemVersionUnreadable,
			provider.ProblemVersionUnsupported,
			provider.ProblemCapabilityMissing,
			provider.ProblemConfigHomeUnsafe,
			provider.ProblemAuthUnknown,
			provider.ProblemCredentialMissing,
			provider.ProblemCredentialFileUnsafe:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func readyCapabilities(name core.ProviderName) []string {
	switch name {
	case core.ProviderClaude:
		return []string{
			"empty_settings",
			"empty_tools",
			"json_envelope",
			"no_session_persistence",
			"safe_mode",
			"stdin_prompt",
		}
	case core.ProviderCodex:
		return []string{
			"ephemeral",
			"feature_hardening",
			"never_approve",
			"read_only",
			"schema_file",
			"stdin_prompt",
		}
	case core.ProviderGemini:
		return []string{
			"disposable_home",
			"empty_core_tools",
			"extensions_disabled",
			"json_envelope",
			"stdin_prompt",
			"system_settings_isolated",
		}
	default:
		return nil
	}
}

func strictlySortedUnique(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func makeReportDTO(report Report) reportDTO {
	checks := report.Core()
	providers := report.Providers()
	models := report.Models()
	dto := reportDTO{
		Core:      make([]checkDTO, len(checks)),
		Providers: make([]providerDTO, len(providers)),
		Models:    models,
	}
	for index, check := range checks {
		dto.Core[index] = checkDTO(check)
	}
	for index, result := range providers {
		capabilities := make([]string, len(result.Capabilities))
		copy(capabilities, result.Capabilities)
		dto.Providers[index] = providerDTO{
			Name:         result.Name,
			Status:       result.Status,
			Version:      result.Version,
			Auth:         result.Auth,
			Capabilities: capabilities,
			Problems:     slices.Clone(result.Problems),
		}
	}
	return dto
}

func appendTextColumn(payload []byte, value string) []byte {
	return append(payload, value...)
}

func textValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func textList(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

func writeReportPayload(writer io.Writer, payload []byte) error {
	if writer == nil {
		return ErrReportWrite
	}
	written, err := writer.Write(payload)
	if err != nil || written != len(payload) {
		return ErrReportWrite
	}
	return nil
}
