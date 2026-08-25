package initconfig

import (
	"bytes"
	"io"
	"reflect"
	"sort"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

// DiffKind classifies one semantic plan entry.
type DiffKind uint8

const (
	// DiffUnchanged marks a selected target whose managed fields are unchanged.
	DiffUnchanged DiffKind = iota + 1
	// DiffAdded marks a target newly added by the plan.
	DiffAdded
	// DiffReplaced marks an existing target whose managed fields change.
	DiffReplaced
)

// DiffTarget identifies one closed configuration target.
type DiffTarget uint8

const (
	// DiffGatewayAuth identifies the closed Gateway authentication target.
	DiffGatewayAuth DiffTarget = iota + 1
	// DiffProvider identifies a provider configuration target.
	DiffProvider
	// DiffModel identifies a model-alias target.
	DiffModel
)

// DiffField contains only normalized allowlisted values.
type DiffField struct {
	Name   string
	Before string
	After  string
}

// DiffEntry is one safe semantic change.
type DiffEntry struct {
	Kind   DiffKind
	Target DiffTarget
	Name   string
	Fields []DiffField
}

// SemanticDiff contains safe entries only.
type SemanticDiff struct {
	Entries []DiffEntry
}

var diffFieldOrder = map[DiffTarget][]string{
	DiffGatewayAuth: {"mode", "key_file", "key_env"},
	DiffProvider: {
		"executable",
		"prefix_args",
		"config_home",
		"credential_env",
		"concurrency",
		"queue_size",
		"queue_bytes",
		"queue_timeout",
		"execution_timeout",
	},
	DiffModel: {"provider", "provider_model"},
}

// WriteTo writes one deterministic colorless semantic summary. It validates
// the complete structure before invoking the writer, so unsafe data cannot
// leak through partial output.
func (diff SemanticDiff) WriteTo(writer io.Writer) (int64, error) {
	if nilInterface(writer) {
		return 0, ErrPlan
	}
	entries, ok := validatedDiffEntries(diff.Entries)
	if !ok {
		return 0, ErrPlan
	}
	var output bytes.Buffer
	for _, entry := range entries {
		output.WriteString(diffMarker(entry.Kind))
		output.WriteString(diffTargetName(entry.Target))
		output.WriteByte(' ')
		output.WriteString(entry.Name)
		output.WriteByte('\n')
		for _, field := range entry.Fields {
			output.WriteString("  ")
			output.WriteString(field.Name)
			output.WriteString(": ")
			writeDiffFieldValue(&output, entry.Kind, field)
			output.WriteByte('\n')
		}
	}
	value := output.Bytes()
	written, err := writer.Write(value)
	if err != nil || written != len(value) {
		return int64(written), ErrPlan
	}
	return int64(written), nil
}

func validatedDiffEntries(entries []DiffEntry) ([]DiffEntry, bool) {
	cloned := append([]DiffEntry(nil), entries...)
	seenEntries := make(map[string]struct{}, len(cloned))
	for index := range cloned {
		entry := &cloned[index]
		if !validDiffKind(entry.Kind) || !validDiffName(entry.Target, entry.Name) {
			return nil, false
		}
		entryKey := string(rune(entry.Target)) + "\x00" + entry.Name
		if _, duplicate := seenEntries[entryKey]; duplicate {
			return nil, false
		}
		seenEntries[entryKey] = struct{}{}
		entry.Fields = append([]DiffField(nil), entry.Fields...)
		order, exists := diffFieldOrder[entry.Target]
		if !exists {
			return nil, false
		}
		ranks := make(map[string]int, len(order))
		for rank, name := range order {
			ranks[name] = rank
		}
		seenFields := make(map[string]struct{}, len(entry.Fields))
		for _, field := range entry.Fields {
			if _, allowed := ranks[field.Name]; !allowed ||
				!safeDiffValue(field.Before) || !safeDiffValue(field.After) {
				return nil, false
			}
			if _, duplicate := seenFields[field.Name]; duplicate {
				return nil, false
			}
			seenFields[field.Name] = struct{}{}
			if entry.Target == DiffGatewayAuth && field.Name == "mode" &&
				(!safeGatewayMode(field.Before) || !safeGatewayMode(field.After)) {
				return nil, false
			}
		}
		sort.SliceStable(entry.Fields, func(left, right int) bool {
			return ranks[entry.Fields[left].Name] < ranks[entry.Fields[right].Name]
		})
	}
	sort.SliceStable(cloned, func(left, right int) bool {
		if cloned[left].Target != cloned[right].Target {
			return cloned[left].Target < cloned[right].Target
		}
		return cloned[left].Name < cloned[right].Name
	})
	return cloned, true
}

func validDiffKind(kind DiffKind) bool {
	return kind == DiffUnchanged || kind == DiffAdded || kind == DiffReplaced
}

func validDiffName(target DiffTarget, name string) bool {
	switch target {
	case DiffGatewayAuth:
		return name == "gateway"
	case DiffProvider:
		return knownProvider(core.ProviderName(name))
	case DiffModel:
		if !safeText(name) {
			return false
		}
		_, err := core.NewRegistry([]core.Model{{
			ID: name, Provider: core.ProviderCodex, ProviderModel: "diff-validation",
		}})
		return err == nil
	default:
		return false
	}
}

func safeDiffValue(value string) bool {
	return value == "" || safeText(value)
}

func safeGatewayMode(value string) bool {
	return value == "" || value == string(GatewayAuthFile) ||
		value == string(GatewayAuthEnvironment) || value == string(GatewayAuthNone)
}

func diffMarker(kind DiffKind) string {
	switch kind {
	case DiffUnchanged:
		return "unchanged "
	case DiffAdded:
		return "+ "
	case DiffReplaced:
		return "~ "
	default:
		return ""
	}
}

func diffTargetName(target DiffTarget) string {
	switch target {
	case DiffGatewayAuth:
		return "gateway-auth"
	case DiffProvider:
		return "provider"
	case DiffModel:
		return "model"
	default:
		return ""
	}
}

func writeDiffFieldValue(
	output *bytes.Buffer,
	kind DiffKind,
	field DiffField,
) {
	switch {
	case kind == DiffReplaced && field.Before != "" && field.After != "" &&
		field.Before != field.After:
		output.WriteString(field.Before)
		output.WriteString(" -> ")
		output.WriteString(field.After)
	case field.After != "":
		output.WriteString(field.After)
	case field.Before != "":
		output.WriteString(field.Before)
		output.WriteString(" -> (none)")
	default:
		output.WriteString("(none)")
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	case reflect.Invalid, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr, reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128, reflect.Array, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return false
	default:
		return false
	}
}
