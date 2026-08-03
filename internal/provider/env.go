package provider

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

var environmentNamePattern = regexp.MustCompile(
	`^[A-Za-z_][A-Za-z0-9_]*$`,
)

var (
	errInvalidEnvironment = errors.New("provider environment is invalid")
	errEnvironmentNames   = errors.New(
		"provider environment has conflicting names",
	)
	errEnvironmentLookup = errors.New(
		"provider environment lookup is unavailable",
	)
	errEnvironmentMissing = errors.New(
		"required provider environment value is missing",
	)
)

// EnvVar is one fixed child-process environment value.
type EnvVar struct {
	Name  string
	Value string
}

// EnvSpec explicitly defines the complete provider child environment.
type EnvSpec struct {
	Fixed          []EnvVar
	SafePath       string
	RequiredLookup []string
}

// BuildEnv creates a fresh, sorted provider environment from only the supplied
// fixed values, authoritative safe PATH, and explicitly named lookups.
func BuildEnv(spec EnvSpec, lookup LookupEnv) ([]string, error) {
	if spec.SafePath == "" || strings.IndexByte(spec.SafePath, 0) >= 0 {
		return nil, errInvalidEnvironment
	}

	values := make(map[string]string, len(spec.Fixed)+len(spec.RequiredLookup)+1)
	canonicalNames := make(
		map[string]string,
		len(spec.Fixed)+len(spec.RequiredLookup)+1,
	)
	values["PATH"] = spec.SafePath
	canonicalNames[canonicalEnvironmentName("PATH")] = "PATH"

	for _, fixed := range spec.Fixed {
		if !validEnvironmentName(fixed.Name) ||
			strings.IndexByte(fixed.Value, 0) >= 0 {
			return nil, errInvalidEnvironment
		}
		canonical := canonicalEnvironmentName(fixed.Name)
		if _, exists := canonicalNames[canonical]; exists {
			return nil, errEnvironmentNames
		}
		values[fixed.Name] = fixed.Value
		canonicalNames[canonical] = fixed.Name
	}

	lookupNames, err := normalizedLookupNames(
		spec.RequiredLookup,
		canonicalNames,
	)
	if err != nil {
		return nil, err
	}
	if len(lookupNames) > 0 && lookup == nil {
		return nil, errEnvironmentLookup
	}

	for _, name := range lookupNames {
		value, present := lookup(name)
		if !present || value == "" {
			return nil, errEnvironmentMissing
		}
		if strings.IndexByte(value, 0) >= 0 {
			return nil, errInvalidEnvironment
		}
		values[name] = value
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment, nil
}

func normalizedLookupNames(
	requested []string,
	existing map[string]string,
) ([]string, error) {
	exactNames := make(map[string]struct{}, len(requested))
	canonicalNames := make(map[string]string, len(requested))
	names := make([]string, 0, len(requested))

	for _, name := range requested {
		if !validEnvironmentName(name) {
			return nil, errInvalidEnvironment
		}
		if name == "PATH" {
			continue
		}
		if _, duplicate := exactNames[name]; duplicate {
			continue
		}
		exactNames[name] = struct{}{}

		canonical := canonicalEnvironmentName(name)
		if _, collision := existing[canonical]; collision {
			return nil, errEnvironmentNames
		}
		if prior, collision := canonicalNames[canonical]; collision &&
			prior != name {
			return nil, errEnvironmentNames
		}
		canonicalNames[canonical] = name
		names = append(names, name)
	}

	sort.Strings(names)
	return names, nil
}

func validEnvironmentName(name string) bool {
	return strings.IndexByte(name, 0) < 0 &&
		environmentNamePattern.MatchString(name)
}

func canonicalEnvironmentName(name string) string {
	return strings.ToUpper(name)
}
