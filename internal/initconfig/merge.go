package initconfig

import (
	"bytes"
	"sort"
	"strings"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

// KeyAction describes the read-only or mutating key-file work a later storage
// layer must perform.
type KeyAction uint8

const (
	// KeyActionNone means the storage layer has no key-file work.
	KeyActionNone KeyAction = iota
	// KeyActionInspect means the storage layer must verify an existing key file.
	KeyActionInspect
	// KeyActionEnsure means the storage layer must create or validate a key file.
	KeyActionEnsure
)

// MergePlan is a validated, purely in-memory configuration plan.
type MergePlan struct {
	Candidate        []byte
	Config           config.Config
	Diff             SemanticDiff
	Collisions       []Collision
	Changed          bool
	KeyAction        KeyAction
	KeyPath          string
	KeyAllowExisting bool
}

// Collision describes a safe semantic replacement preview.
type Collision struct {
	Target DiffTarget
	Name   string
	Fields []DiffField
}

type providerChange struct {
	name    core.ProviderName
	before  config.Provider
	after   config.Provider
	added   bool
	changed map[string]struct{}
}

type modelChange struct {
	before config.Model
	after  config.Model
	added  bool
}

type sourceEdit struct {
	start       int
	end         int
	replacement []byte
}

var providerFieldOrder = []string{
	"executable",
	"prefix_args",
	"config_home",
	"credential_env",
	"concurrency",
	"queue_size",
	"queue_bytes",
	"queue_timeout",
	"execution_timeout",
}

var modelFieldOrder = []string{"id", "provider", "provider_model", "created"}

// PlanMerge validates an existing source and returns a comment-preserving
// candidate without touching the filesystem.
func PlanMerge(
	existing []byte,
	exists bool,
	desired DesiredState,
) (MergePlan, error) {
	if exists && len(existing) == 0 || !exists && len(existing) != 0 {
		return MergePlan{}, ErrPlan
	}
	if err := ValidateDesiredState(desired); err != nil {
		return MergePlan{}, ErrPlan
	}
	if !exists {
		candidate, err := renderFresh(desired)
		if err != nil {
			return MergePlan{}, ErrPlan
		}
		decoded, err := config.Decode(bytes.NewReader(candidate))
		if err != nil || !selectedModelsPresent(decoded, desired.SelectedProviders) {
			return MergePlan{}, ErrPlan
		}
		plan := MergePlan{
			Candidate: candidate,
			Config:    decoded,
			Diff:      freshSemanticDiff(desired),
			Changed:   true,
		}
		setKeyPlan(&plan, nil, desired)
		return cloneMergePlan(plan), nil
	}

	before, err := config.Decode(bytes.NewReader(existing))
	if err != nil {
		return MergePlan{}, ErrPlan
	}
	index, err := buildSourceIndex(existing)
	if err != nil {
		return MergePlan{}, ErrPlan
	}
	after := cloneConfig(before)
	providerChanges, providerDiff, providerCollisions := planProviders(
		&after,
		desired,
	)
	gatewayChanged, gatewayDiff := planGateway(&after, before, desired.Gateway)
	modelChanges, modelDiff, modelCollisions := planModels(&after, desired)
	collisions := append(providerCollisions, modelCollisions...)
	diff := SemanticDiff{Entries: append(providerDiff, gatewayDiff...)}
	diff.Entries = append(diff.Entries, modelDiff...)

	semanticChanged := gatewayChanged || len(providerChanges) != 0 || len(modelChanges) != 0
	if !semanticChanged {
		plan := MergePlan{
			Candidate: append([]byte(nil), existing...),
			Config:    cloneConfig(before),
			Diff:      diff,
			Changed:   false,
		}
		setKeyPlan(&plan, &before, desired)
		return cloneMergePlan(plan), nil
	}

	edits, appendBlocks, err := buildMergeEdits(
		existing,
		index,
		before,
		after,
		providerChanges,
		gatewayChanged,
		modelChanges,
	)
	if err != nil {
		return MergePlan{}, ErrPlan
	}
	candidate, err := applySourceEdits(existing, edits)
	if err != nil {
		return MergePlan{}, ErrPlan
	}
	candidate = appendCanonicalBlocks(candidate, appendBlocks, sourceLineEnding(existing))
	decoded, err := config.Decode(bytes.NewReader(candidate))
	if err != nil || !selectedModelsPresent(decoded, desired.SelectedProviders) {
		return MergePlan{}, ErrPlan
	}
	plan := MergePlan{
		Candidate:  candidate,
		Config:     decoded,
		Diff:       diff,
		Collisions: collisions,
		Changed:    true,
	}
	setKeyPlan(&plan, &before, desired)
	plan = cloneMergePlan(plan)
	if hasUnauthorizedCollision(collisions, desired) {
		return plan, ErrCollision
	}
	return plan, nil
}

func planProviders(
	after *config.Config,
	desired DesiredState,
) ([]providerChange, []DiffEntry, []Collision) {
	changes := make([]providerChange, 0, len(desired.Providers))
	diff := make([]DiffEntry, 0, len(desired.Providers))
	var collisions []Collision
	if after.Providers == nil {
		after.Providers = make(map[string]config.Provider)
	}
	for _, patch := range desired.Providers {
		before, exists := after.Providers[string(patch.Name)]
		updated := before
		updated.Executable = patch.Command.Value.Executable
		updated.PrefixArgs = append([]string(nil), patch.Command.Value.PrefixArgs...)
		updated.ConfigHome = patch.ConfigHome.Value
		updated.CredentialEnv = append([]string(nil), patch.CredentialEnv.Value...)
		fields, changed := providerDiffFields(before, updated, exists)
		kind := DiffUnchanged
		if !exists {
			kind = DiffAdded
		} else if len(fields) != 0 {
			kind = DiffReplaced
		}
		diff = append(diff, DiffEntry{
			Kind:   kind,
			Target: DiffProvider,
			Name:   string(patch.Name),
			Fields: cloneDiffFields(fields),
		})
		if !exists || len(fields) != 0 {
			after.Providers[string(patch.Name)] = updated
			changes = append(changes, providerChange{
				name:    patch.Name,
				before:  cloneProvider(before),
				after:   cloneProvider(updated),
				added:   !exists,
				changed: changed,
			})
		}
		if exists && len(fields) != 0 {
			collisions = append(collisions, Collision{
				Target: DiffProvider,
				Name:   string(patch.Name),
				Fields: cloneDiffFields(fields),
			})
		}
	}
	return changes, diff, collisions
}

func providerDiffFields(
	before config.Provider,
	after config.Provider,
	exists bool,
) ([]DiffField, map[string]struct{}) {
	changed := make(map[string]struct{})
	var fields []DiffField
	add := func(name, oldValue, newValue string) {
		if exists && oldValue == newValue {
			return
		}
		changed[name] = struct{}{}
		fields = append(fields, DiffField{Name: name, Before: oldValue, After: newValue})
	}
	add("executable", before.Executable, after.Executable)
	add("prefix_args", strings.Join(before.PrefixArgs, ","), strings.Join(after.PrefixArgs, ","))
	add("config_home", before.ConfigHome, after.ConfigHome)
	add(
		"credential_env",
		strings.Join(before.CredentialEnv, ","),
		strings.Join(after.CredentialEnv, ","),
	)
	return fields, changed
}

func planGateway(
	after *config.Config,
	before config.Config,
	patch GatewayAuthPatch,
) (bool, []DiffEntry) {
	if !patch.Set {
		return false, nil
	}
	oldMode, _ := gatewayMode(before.Server)
	updated := after.Server
	updated.APIKeyEnv = patch.APIKeyEnv
	updated.APIKeyFile = patch.APIKeyFile
	newMode, _ := gatewayMode(updated)
	changed := oldMode != newMode || before.Server.APIKeyFile != updated.APIKeyFile ||
		before.Server.APIKeyEnv != updated.APIKeyEnv
	kind := DiffUnchanged
	if changed {
		kind = DiffReplaced
		after.Server = updated
	}
	fields := []DiffField{{Name: "mode", Before: oldMode, After: newMode}}
	if before.Server.APIKeyFile != updated.APIKeyFile {
		fields = append(fields, DiffField{
			Name: "key_file", Before: before.Server.APIKeyFile, After: updated.APIKeyFile,
		})
	}
	if before.Server.APIKeyEnv != updated.APIKeyEnv {
		fields = append(fields, DiffField{
			Name: "key_env", Before: before.Server.APIKeyEnv, After: updated.APIKeyEnv,
		})
	}
	return changed, []DiffEntry{{
		Kind:   kind,
		Target: DiffGatewayAuth,
		Name:   "gateway",
		Fields: fields,
	}}
}

func gatewayMode(server config.Server) (string, string) {
	if server.APIKeyFile != "" {
		return string(GatewayAuthFile), server.APIKeyFile
	}
	if server.APIKeyEnv != "" {
		return string(GatewayAuthEnvironment), server.APIKeyEnv
	}
	return string(GatewayAuthNone), ""
}

func planModels(
	after *config.Config,
	desired DesiredState,
) ([]modelChange, []DiffEntry, []Collision) {
	byID := make(map[string]int, len(after.Models))
	for index, model := range after.Models {
		byID[model.ID] = index
	}
	var changes []modelChange
	diff := make([]DiffEntry, 0, len(desired.Models))
	var collisions []Collision
	for _, mapping := range desired.Models {
		index, exists := byID[mapping.ID]
		if !exists {
			model := config.Model{
				ID:            mapping.ID,
				Provider:      string(mapping.Provider),
				ProviderModel: mapping.ProviderModel,
				Created:       0,
			}
			after.Models = append(after.Models, model)
			byID[model.ID] = len(after.Models) - 1
			changes = append(changes, modelChange{after: model, added: true})
			diff = append(diff, DiffEntry{
				Kind:   DiffAdded,
				Target: DiffModel,
				Name:   model.ID,
				Fields: []DiffField{
					{Name: "provider", After: model.Provider},
					{Name: "provider_model", After: model.ProviderModel},
				},
			})
			continue
		}
		before := after.Models[index]
		updated := before
		updated.Provider = string(mapping.Provider)
		updated.ProviderModel = mapping.ProviderModel
		var fields []DiffField
		if before.Provider != updated.Provider {
			fields = append(fields, DiffField{
				Name: "provider", Before: before.Provider, After: updated.Provider,
			})
		}
		if before.ProviderModel != updated.ProviderModel {
			fields = append(fields, DiffField{
				Name:   "provider_model",
				Before: before.ProviderModel,
				After:  updated.ProviderModel,
			})
		}
		kind := DiffUnchanged
		if len(fields) != 0 {
			kind = DiffReplaced
			after.Models[index] = updated
			changes = append(changes, modelChange{before: before, after: updated})
			collisions = append(collisions, Collision{
				Target: DiffModel, Name: before.ID, Fields: cloneDiffFields(fields),
			})
		}
		diff = append(diff, DiffEntry{
			Kind: kind, Target: DiffModel, Name: before.ID, Fields: fields,
		})
	}
	return changes, diff, collisions
}

func buildMergeEdits(
	source []byte,
	index sourceIndex,
	before config.Config,
	after config.Config,
	providers []providerChange,
	gatewayChanged bool,
	models []modelChange,
) ([]sourceEdit, [][]byte, error) {
	eol := sourceLineEnding(source)
	var edits []sourceEdit
	var blocks [][]byte
	addedProviders := make([]providerChange, 0)
	for _, change := range providers {
		if change.added {
			addedProviders = append(addedProviders, change)
			continue
		}
		group, ok := singleSourceGroup(index, []string{"providers", string(change.name)})
		if !ok {
			return nil, nil, ErrPlan
		}
		providerEdits, err := providerSourceEdits(source, group, change, eol)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, providerEdits...)
	}
	sort.Slice(addedProviders, func(left, right int) bool {
		return addedProviders[left].name < addedProviders[right].name
	})
	for _, change := range addedProviders {
		fragment, err := renderProvider(change.name, change.after)
		if err != nil {
			return nil, nil, err
		}
		blocks = append(blocks, normalizeLineEndings(fragment, eol))
	}

	if gatewayChanged {
		gatewayEdits, gatewayBlock, err := gatewaySourceEdits(
			source,
			index,
			before.Server,
			after.Server,
			eol,
		)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, gatewayEdits...)
		if len(gatewayBlock) != 0 {
			blocks = append([][]byte{gatewayBlock}, blocks...)
		}
	}

	addedModels := make([]modelChange, 0)
	for _, change := range models {
		if change.added {
			addedModels = append(addedModels, change)
			continue
		}
		group, ok := modelSourceGroup(index, before.Models, change.before.ID)
		if !ok {
			return nil, nil, ErrPlan
		}
		modelEdits, err := modelSourceEdits(source, group, change.after, eol)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, modelEdits...)
	}
	if len(addedModels) != 0 {
		sort.Slice(addedModels, func(left, right int) bool {
			return addedModels[left].after.ID < addedModels[right].after.ID
		})
		modelBlocks, modelEdits, err := addedModelSource(
			index,
			addedModels,
			eol,
		)
		if err != nil {
			return nil, nil, err
		}
		blocks = append(blocks, modelBlocks...)
		edits = append(edits, modelEdits...)
	}
	return edits, blocks, nil
}

func providerSourceEdits(
	source []byte,
	group sourceGroup,
	change providerChange,
	eol []byte,
) ([]sourceEdit, error) {
	switch group.Representation {
	case representationTable:
		fragment, err := renderProvider(change.name, change.after)
		if err != nil {
			return nil, err
		}
		replacement, err := preserveStandardGroupComments(
			source,
			group,
			fragment,
			providerFieldOrder,
			eol,
		)
		if err != nil {
			return nil, err
		}
		return []sourceEdit{{group.Owned.Start, group.Owned.End, replacement}}, nil
	case representationDotted:
		return dottedProviderEdits(source, group, change, eol)
	case representationInline:
		replacement, err := renderProviderInline(change.after)
		if err != nil {
			return nil, err
		}
		return []sourceEdit{{group.Owned.Start, group.Owned.End, replacement}}, nil
	default:
		return nil, ErrPlan
	}
}

func dottedProviderEdits(
	source []byte,
	group sourceGroup,
	change providerChange,
	eol []byte,
) ([]sourceEdit, error) {
	values, err := renderedProviderValues(change.after)
	if err != nil {
		return nil, err
	}
	var edits []sourceEdit
	var additions bytes.Buffer
	for _, field := range providerFieldOrder {
		if _, changed := change.changed[field]; !changed {
			continue
		}
		value, present := values[field]
		entry, exists := group.Entries[field]
		switch {
		case exists && present:
			edits = append(edits, sourceEdit{entry.Value.Start, entry.Value.End, value})
		case exists && !present:
			edits = append(edits, sourceEdit{
				entry.Expression.Start,
				entry.Expression.End,
				removedExpressionComment(source, entry),
			})
		case !exists && present:
			additions.WriteString("providers.")
			additions.WriteString(string(change.name))
			additions.WriteByte('.')
			additions.WriteString(field)
			additions.WriteString(" = ")
			additions.Write(value)
			additions.Write(eol)
		}
	}
	if additions.Len() != 0 {
		prefix := []byte(nil)
		if group.Owned.End > 0 && source[group.Owned.End-1] != '\n' {
			prefix = eol
		}
		replacement := append(append([]byte(nil), prefix...), additions.Bytes()...)
		edits = append(edits, sourceEdit{group.Owned.End, group.Owned.End, replacement})
	}
	return edits, nil
}

func gatewaySourceEdits(
	source []byte,
	index sourceIndex,
	before config.Server,
	after config.Server,
	eol []byte,
) ([]sourceEdit, []byte, error) {
	group, exists := singleSourceGroup(index, []string{"server"})
	if !exists {
		fragment, err := renderGatewayAuth(after)
		if err != nil {
			return nil, nil, err
		}
		return nil, normalizeLineEndings(fragment, eol), nil
	}
	if group.Representation == representationInline {
		fragment, err := renderServerInline(after)
		if err != nil {
			return nil, nil, err
		}
		return []sourceEdit{{group.Owned.Start, group.Owned.End, fragment}}, nil, nil
	}

	oldField, _ := gatewaySourceField(before)
	if oldField == "" {
		if _, explicitDisabled := group.Entries["api_key_env"]; explicitDisabled {
			oldField = "api_key_env"
		}
	}
	newField, newValue := gatewaySourceField(after)
	oldEntry, oldExists := group.Entries[oldField]
	if oldExists && newField == oldField {
		encoded, err := encodeTOMLValue(newValue)
		if err != nil {
			return nil, nil, err
		}
		return []sourceEdit{{oldEntry.Value.Start, oldEntry.Value.End, encoded}}, nil, nil
	}
	if oldExists && newField == "" {
		return []sourceEdit{{
			oldEntry.Expression.Start,
			oldEntry.Expression.End,
			removedExpressionComment(source, oldEntry),
		}}, nil, nil
	}
	if oldExists {
		line, err := renderedAuthAssignment(group.Representation, newField, newValue, eol)
		if err != nil {
			return nil, nil, err
		}
		line = preserveInlineComment(source, oldEntry, line, eol)
		return []sourceEdit{{oldEntry.Expression.Start, oldEntry.Expression.End, line}}, nil, nil
	}
	if newField == "" {
		return nil, nil, nil
	}
	line, err := renderedAuthAssignment(group.Representation, newField, newValue, eol)
	if err != nil {
		return nil, nil, err
	}
	if group.Owned.End > 0 && source[group.Owned.End-1] != '\n' {
		line = append(append([]byte(nil), eol...), line...)
	}
	return []sourceEdit{{group.Owned.End, group.Owned.End, line}}, nil, nil
}

func gatewaySourceField(server config.Server) (string, string) {
	if server.APIKeyFile != "" {
		return "api_key_file", server.APIKeyFile
	}
	if server.APIKeyEnv != "" {
		return "api_key_env", server.APIKeyEnv
	}
	return "", ""
}

func renderedAuthAssignment(
	representation representation,
	field string,
	value string,
	eol []byte,
) ([]byte, error) {
	encoded, err := encodeTOMLValue(value)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if representation == representationDotted {
		output.WriteString("server.")
	}
	output.WriteString(field)
	output.WriteString(" = ")
	output.Write(encoded)
	output.Write(eol)
	return output.Bytes(), nil
}

func modelSourceEdits(
	source []byte,
	group sourceGroup,
	model config.Model,
	eol []byte,
) ([]sourceEdit, error) {
	switch group.Representation {
	case representationTable:
		fragment, err := renderModel(model)
		if err != nil {
			return nil, err
		}
		replacement, err := preserveStandardGroupComments(
			source,
			group,
			fragment,
			modelFieldOrder,
			eol,
		)
		if err != nil {
			return nil, err
		}
		return []sourceEdit{{group.Owned.Start, group.Owned.End, replacement}}, nil
	case representationInline:
		fragment, err := renderModelInline(model)
		if err != nil {
			return nil, err
		}
		return []sourceEdit{{group.Owned.Start, group.Owned.End, fragment}}, nil
	case representationDotted:
		return nil, ErrPlan
	default:
		return nil, ErrPlan
	}
}

func addedModelSource(
	index sourceIndex,
	models []modelChange,
	eol []byte,
) ([][]byte, []sourceEdit, error) {
	indexes := index.ByPath[sourcePathKey([]string{"models"})]
	if len(indexes) != 0 &&
		index.Groups[indexes[0]].Representation == representationInline {
		last := index.Groups[indexes[len(indexes)-1]]
		var insertion bytes.Buffer
		for _, model := range models {
			fragment, err := renderModelInline(model.after)
			if err != nil {
				return nil, nil, err
			}
			insertion.WriteString(", ")
			insertion.Write(fragment)
		}
		return nil, []sourceEdit{{last.Owned.End, last.Owned.End, insertion.Bytes()}}, nil
	}
	blocks := make([][]byte, 0, len(models))
	for _, model := range models {
		fragment, err := renderModel(model.after)
		if err != nil {
			return nil, nil, err
		}
		blocks = append(blocks, normalizeLineEndings(fragment, eol))
	}
	return blocks, nil, nil
}

func preserveStandardGroupComments(
	source []byte,
	original sourceGroup,
	rendered []byte,
	fieldOrder []string,
	eol []byte,
) ([]byte, error) {
	parsed, err := buildSourceIndex(rendered)
	if err != nil || len(parsed.Groups) != 1 {
		return nil, ErrPlan
	}
	newGroup := parsed.Groups[0]
	var output bytes.Buffer
	output.Write(source[original.Header.Start:original.Header.End])
	for _, field := range fieldOrder {
		oldEntry, oldExists := original.Entries[field]
		newEntry, newExists := newGroup.Entries[field]
		if oldExists && len(oldEntry.LeadingComments) != 0 {
			output.Write(oldEntry.LeadingComments)
		}
		if !newExists {
			if oldExists {
				output.Write(removedExpressionComment(source, oldEntry))
			}
			continue
		}
		line := append(
			[]byte(nil),
			rendered[newEntry.Expression.Start:newEntry.Expression.End]...,
		)
		line = normalizeLineEndings(line, eol)
		if oldExists {
			line = preserveInlineComment(source, oldEntry, line, eol)
		}
		output.Write(line)
	}
	return output.Bytes(), nil
}

func preserveInlineComment(
	source []byte,
	entry sourceEntry,
	replacement []byte,
	eol []byte,
) []byte {
	suffix := source[entry.Value.End:entry.Expression.End]
	comment := bytes.IndexByte(suffix, '#')
	if comment < 0 {
		return ensureLineEnding(replacement, eol)
	}
	start := comment
	for start > 0 && (suffix[start-1] == ' ' || suffix[start-1] == '\t') {
		start--
	}
	line := bytes.TrimSuffix(replacement, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	result := append([]byte(nil), line...)
	result = append(result, suffix[start:]...)
	if len(result) == 0 || result[len(result)-1] != '\n' {
		result = append(result, eol...)
	}
	return result
}

func removedExpressionComment(source []byte, entry sourceEntry) []byte {
	suffix := source[entry.Value.End:entry.Expression.End]
	comment := bytes.IndexByte(suffix, '#')
	if comment < 0 {
		return nil
	}
	return append([]byte(nil), suffix[comment:]...)
}

func renderProviderInline(provider config.Provider) ([]byte, error) {
	values, err := renderedProviderValues(provider)
	if err != nil {
		return nil, err
	}
	return renderInlineValues(values, providerFieldOrder)
}

func renderedProviderValues(provider config.Provider) (map[string][]byte, error) {
	values := make(map[string][]byte)
	fields := []struct {
		name    string
		value   any
		include bool
	}{
		{"executable", provider.Executable, true},
		{"prefix_args", provider.PrefixArgs, len(provider.PrefixArgs) != 0},
		{"config_home", provider.ConfigHome, true},
		{"credential_env", provider.CredentialEnv, len(provider.CredentialEnv) != 0},
		{"concurrency", provider.Concurrency, provider.Concurrency != defaultProviderConcurrency},
		{"queue_size", provider.QueueSize, provider.QueueSize != defaultProviderQueueSize},
		{"queue_bytes", provider.QueueBytes, provider.QueueBytes != defaultProviderQueueBytes},
		{
			"queue_timeout",
			time.Duration(provider.QueueTimeout).String(),
			time.Duration(provider.QueueTimeout) != defaultProviderQueueTimeout,
		},
		{
			"execution_timeout",
			time.Duration(provider.ExecutionTimeout).String(),
			time.Duration(provider.ExecutionTimeout) != defaultProviderExecutionTimeout,
		},
	}
	for _, field := range fields {
		if !field.include {
			continue
		}
		encoded, err := encodeTOMLValue(field.value)
		if err != nil {
			return nil, err
		}
		values[field.name] = encoded
	}
	return values, nil
}

func renderModelInline(model config.Model) ([]byte, error) {
	if _, err := renderModel(model); err != nil {
		return nil, err
	}
	values := make(map[string][]byte, len(modelFieldOrder))
	for _, field := range []struct {
		name  string
		value any
	}{
		{"id", model.ID},
		{"provider", model.Provider},
		{"provider_model", model.ProviderModel},
		{"created", model.Created},
	} {
		encoded, err := encodeTOMLValue(field.value)
		if err != nil {
			return nil, err
		}
		values[field.name] = encoded
	}
	return renderInlineValues(values, modelFieldOrder)
}

func renderServerInline(server config.Server) ([]byte, error) {
	if _, err := renderGatewayAuth(server); err != nil {
		return nil, err
	}
	values := make(map[string][]byte)
	fields := []struct {
		name    string
		value   any
		include bool
	}{
		{"api_key_file", server.APIKeyFile, server.APIKeyFile != ""},
		{"api_key_env", server.APIKeyEnv, server.APIKeyEnv != ""},
		{"listen", server.Listen, true},
		{"http_body_bytes", server.HTTPBodyBytes, true},
		{"input_bytes", server.InputBytes, true},
		{"instructions_bytes", server.InstructionsBytes, true},
		{"schema_bytes", server.SchemaBytes, true},
		{"handler_limit", server.HandlerLimit, true},
		{"body_reader_limit", server.BodyReaderLimit, true},
		{"max_header_bytes", server.MaxHeaderBytes, true},
		{"read_header_timeout", time.Duration(server.ReadHeaderTimeout).String(), true},
		{"body_read_timeout", time.Duration(server.BodyReadTimeout).String(), true},
		{"idle_timeout", time.Duration(server.IdleTimeout).String(), true},
		{"shutdown_timeout", time.Duration(server.ShutdownTimeout).String(), true},
	}
	order := make([]string, 0, len(fields))
	for _, field := range fields {
		if !field.include {
			continue
		}
		encoded, err := encodeTOMLValue(field.value)
		if err != nil {
			return nil, err
		}
		values[field.name] = encoded
		order = append(order, field.name)
	}
	return renderInlineValues(values, order)
}

func renderInlineValues(values map[string][]byte, order []string) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("{ ")
	written := 0
	for _, name := range order {
		value, exists := values[name]
		if !exists {
			continue
		}
		if written != 0 {
			output.WriteString(", ")
		}
		output.WriteString(name)
		output.WriteString(" = ")
		output.Write(value)
		written++
	}
	output.WriteString(" }")
	return output.Bytes(), nil
}

func singleSourceGroup(index sourceIndex, path []string) (sourceGroup, bool) {
	indexes := index.ByPath[sourcePathKey(path)]
	if len(indexes) != 1 {
		return sourceGroup{}, false
	}
	return index.Groups[indexes[0]], true
}

func modelSourceGroup(
	index sourceIndex,
	models []config.Model,
	alias string,
) (sourceGroup, bool) {
	groups := index.ByPath[sourcePathKey([]string{"models"})]
	if len(groups) != len(models) {
		return sourceGroup{}, false
	}
	for modelIndex, model := range models {
		if model.ID == alias {
			return index.Groups[groups[modelIndex]], true
		}
	}
	return sourceGroup{}, false
}

func applySourceEdits(source []byte, edits []sourceEdit) ([]byte, error) {
	for _, edit := range edits {
		if edit.start < 0 || edit.end < edit.start || edit.end > len(source) {
			return nil, ErrPlan
		}
	}
	sort.SliceStable(edits, func(left, right int) bool {
		if edits[left].start == edits[right].start {
			return edits[left].end > edits[right].end
		}
		return edits[left].start > edits[right].start
	})
	previousStart := len(source) + 1
	result := append([]byte(nil), source...)
	for _, edit := range edits {
		if edit.end > previousStart {
			return nil, ErrPlan
		}
		replacement := append([]byte(nil), edit.replacement...)
		result = append(result[:edit.start], append(replacement, result[edit.end:]...)...)
		previousStart = edit.start
	}
	return result, nil
}

func appendCanonicalBlocks(source []byte, blocks [][]byte, eol []byte) []byte {
	if len(blocks) == 0 {
		return source
	}
	result := append([]byte(nil), source...)
	if len(result) != 0 {
		if result[len(result)-1] != '\n' {
			result = append(result, eol...)
		}
		result = append(result, eol...)
	}
	for index, block := range blocks {
		if index != 0 {
			result = append(result, eol...)
		}
		result = append(result, bytes.TrimSuffix(block, eol)...)
		result = append(result, eol...)
	}
	return result
}

func sourceLineEnding(source []byte) []byte {
	newline := bytes.IndexByte(source, '\n')
	if newline > 0 && source[newline-1] == '\r' {
		return []byte("\r\n")
	}
	return []byte("\n")
}

func normalizeLineEndings(value []byte, eol []byte) []byte {
	value = bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	if bytes.Equal(eol, []byte("\n")) {
		return append([]byte(nil), value...)
	}
	return bytes.ReplaceAll(value, []byte("\n"), eol)
}

func ensureLineEnding(value []byte, eol []byte) []byte {
	result := bytes.TrimSuffix(value, []byte("\n"))
	result = bytes.TrimSuffix(result, []byte("\r"))
	return append(append([]byte(nil), result...), eol...)
}

func selectedModelsPresent(cfg config.Config, selected []core.ProviderName) bool {
	for _, provider := range selected {
		found := false
		for _, model := range cfg.Models {
			if model.Provider == string(provider) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func setKeyPlan(plan *MergePlan, before *config.Config, desired DesiredState) {
	if plan == nil || plan.Config.Server.APIKeyFile == "" {
		return
	}
	plan.KeyPath = plan.Config.Server.APIKeyFile
	if before != nil && before.Server.APIKeyFile == plan.KeyPath {
		plan.KeyAction = KeyActionInspect
		return
	}
	plan.KeyAction = KeyActionEnsure
	plan.KeyAllowExisting = desired.Gateway.Set && desired.Gateway.KeyExplicit &&
		desired.Gateway.APIKeyFile == plan.KeyPath
}

func hasUnauthorizedCollision(collisions []Collision, desired DesiredState) bool {
	for _, collision := range collisions {
		switch collision.Target {
		case DiffProvider:
			if _, approved := desired.ReplaceProviders[core.ProviderName(collision.Name)]; !approved {
				return true
			}
		case DiffModel:
			if _, approved := desired.ReplaceModels[collision.Name]; !approved {
				return true
			}
		case DiffGatewayAuth:
			return true
		default:
			return true
		}
	}
	return false
}

func freshSemanticDiff(desired DesiredState) SemanticDiff {
	entries := make([]DiffEntry, 0, len(desired.Providers)+len(desired.Models)+1)
	if desired.Gateway.Set {
		mode, source := gatewayMode(config.Server{
			APIKeyEnv: desired.Gateway.APIKeyEnv, APIKeyFile: desired.Gateway.APIKeyFile,
		})
		fields := []DiffField{{Name: "mode", After: mode}}
		if source != "" {
			name := "key_env"
			if mode == string(GatewayAuthFile) {
				name = "key_file"
			}
			fields = append(fields, DiffField{Name: name, After: source})
		}
		entries = append(entries, DiffEntry{
			Kind: DiffAdded, Target: DiffGatewayAuth, Name: "gateway", Fields: fields,
		})
	}
	for _, provider := range desired.Providers {
		entries = append(entries, DiffEntry{
			Kind: DiffAdded, Target: DiffProvider, Name: string(provider.Name),
		})
	}
	for _, model := range desired.Models {
		entries = append(entries, DiffEntry{
			Kind: DiffAdded, Target: DiffModel, Name: model.ID,
			Fields: []DiffField{
				{Name: "provider", After: string(model.Provider)},
				{Name: "provider_model", After: model.ProviderModel},
			},
		})
	}
	return SemanticDiff{Entries: entries}
}

func cloneProvider(provider config.Provider) config.Provider {
	provider.PrefixArgs = append([]string(nil), provider.PrefixArgs...)
	provider.CredentialEnv = append([]string(nil), provider.CredentialEnv...)
	return provider
}

func cloneConfig(cfg config.Config) config.Config {
	cloned := cfg
	cloned.Providers = make(map[string]config.Provider, len(cfg.Providers))
	for name, provider := range cfg.Providers {
		cloned.Providers[name] = cloneProvider(provider)
	}
	cloned.Models = append([]config.Model(nil), cfg.Models...)
	return cloned
}

func cloneDiffFields(fields []DiffField) []DiffField {
	return append([]DiffField(nil), fields...)
}

func cloneMergePlan(plan MergePlan) MergePlan {
	plan.Candidate = append([]byte(nil), plan.Candidate...)
	plan.Config = cloneConfig(plan.Config)
	plan.Diff.Entries = append([]DiffEntry(nil), plan.Diff.Entries...)
	for index := range plan.Diff.Entries {
		plan.Diff.Entries[index].Fields = cloneDiffFields(plan.Diff.Entries[index].Fields)
	}
	plan.Collisions = append([]Collision(nil), plan.Collisions...)
	for index := range plan.Collisions {
		plan.Collisions[index].Fields = cloneDiffFields(plan.Collisions[index].Fields)
	}
	return plan
}
