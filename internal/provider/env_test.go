package provider

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildEnvIncludesOnlyExplicitFixedAndSelectedLookupValues(t *testing.T) {
	values := map[string]string{
		"AI_CLI_GATEWAY_API_KEY": "gateway-secret",
		"OPENAI_API_KEY":         "wrong-provider-secret",
		"ANTHROPIC_API_KEY":      "claude-secret",
		"GEMINI_API_KEY":         "gemini-secret",
		"PATH":                   "/attacker-controlled/bin",
	}
	var lookedUp []string
	lookup := func(name string) (string, bool) {
		lookedUp = append(lookedUp, name)
		value, ok := values[name]
		return value, ok
	}
	spec := EnvSpec{
		Fixed: []EnvVar{
			{Name: "HOME", Value: "/runtime/home"},
			{Name: "GEMINI_CLI_HOME", Value: "/runtime/gemini"},
			{Name: "TMPDIR", Value: "/runtime/tmp"},
			{Name: "TMP", Value: "/runtime/tmp"},
			{Name: "TEMP", Value: "/runtime/tmp"},
			{Name: "NO_COLOR", Value: "1"},
		},
		SafePath:       "/validated/provider/bin:/usr/bin:/bin",
		RequiredLookup: []string{"GEMINI_API_KEY"},
	}
	want := []string{
		"GEMINI_API_KEY=gemini-secret",
		"GEMINI_CLI_HOME=/runtime/gemini",
		"HOME=/runtime/home",
		"NO_COLOR=1",
		"PATH=/validated/provider/bin:/usr/bin:/bin",
		"TEMP=/runtime/tmp",
		"TMP=/runtime/tmp",
		"TMPDIR=/runtime/tmp",
	}

	got, err := BuildEnv(spec, lookup)
	if err != nil {
		t.Fatalf("BuildEnv() error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEnv()=%q, want %q", got, want)
	}
	if !reflect.DeepEqual(lookedUp, []string{"GEMINI_API_KEY"}) {
		t.Fatalf("lookups=%q, want only GEMINI_API_KEY", lookedUp)
	}

	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{
		"gateway-secret",
		"wrong-provider-secret",
		"claude-secret",
		"/attacker-controlled/bin",
		"AI_CLI_GATEWAY_API_KEY",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("environment contains forbidden value %q", forbidden)
		}
	}
}

func TestBuildEnvRequiresNonemptySafePath(t *testing.T) {
	for _, safePath := range []string{"", "secret\x00path"} {
		_, err := BuildEnv(EnvSpec{SafePath: safePath}, nil)
		if err == nil || err.Error() != "provider environment is invalid" {
			t.Fatalf("BuildEnv(SafePath=%q) error=%v", safePath, err)
		}
		for _, forbidden := range []string{"secret", "path"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("error %q exposed %q", err, forbidden)
			}
		}
	}
}

func TestBuildEnvRequiresLookupAndRequiredValues(t *testing.T) {
	tests := []struct {
		name   string
		lookup LookupEnv
		want   string
	}{
		{
			name: "nil lookup",
			want: "provider environment lookup is unavailable",
		},
		{
			name:   "missing value",
			lookup: func(string) (string, bool) { return "", false },
			want:   "required provider environment value is missing",
		},
		{
			name:   "present empty value",
			lookup: func(string) (string, bool) { return "", true },
			want:   "required provider environment value is missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildEnv(EnvSpec{
				SafePath:       "/safe/bin",
				RequiredLookup: []string{"PLANTED_SECRET_TOKEN"},
			}, test.lookup)
			if err == nil || err.Error() != test.want {
				t.Fatalf("BuildEnv() error=%v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "PLANTED_SECRET_TOKEN") {
				t.Fatalf("error exposed environment name: %v", err)
			}
		})
	}
}

func TestBuildEnvRejectsInvalidNamesAndNULWithoutEchoingData(t *testing.T) {
	tests := []struct {
		name   string
		spec   EnvSpec
		lookup LookupEnv
	}{
		{
			name: "empty fixed name",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Value: "secret"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "leading digit",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Name: "9TOKEN", Value: "secret"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "dash",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Name: "BAD-NAME", Value: "secret"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "equals in name",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Name: "BAD=NAME", Value: "secret"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "NUL in name",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Name: "BAD\x00NAME", Value: "secret"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "NUL in fixed value",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Name: "TOKEN", Value: "secret\x00tail"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "invalid lookup name",
			spec: EnvSpec{
				SafePath:       "/safe/bin",
				RequiredLookup: []string{"BAD NAME"},
			},
			lookup: func(string) (string, bool) { return "secret", true },
		},
		{
			name: "NUL in lookup value",
			spec: EnvSpec{
				SafePath:       "/safe/bin",
				RequiredLookup: []string{"TOKEN"},
			},
			lookup: func(string) (string, bool) { return "secret\x00tail", true },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildEnv(test.spec, test.lookup)
			if err == nil || err.Error() != "provider environment is invalid" {
				t.Fatalf("BuildEnv() error=%v", err)
			}
			for _, forbidden := range []string{"secret", "TOKEN", "BAD"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error %q exposed %q", err, forbidden)
				}
			}
		})
	}
}

func TestBuildEnvAllowsEqualsInValuesAndSortsDeterministically(t *testing.T) {
	spec := EnvSpec{
		Fixed: []EnvVar{
			{Name: "ZED", Value: "z=value"},
			{Name: "ALPHA", Value: "a=b=c"},
		},
		SafePath:       "/safe/bin",
		RequiredLookup: []string{"MIDDLE"},
	}
	lookup := func(string) (string, bool) { return "left=right", true }
	want := []string{
		"ALPHA=a=b=c",
		"MIDDLE=left=right",
		"PATH=/safe/bin",
		"ZED=z=value",
	}

	got, err := BuildEnv(spec, lookup)
	if err != nil {
		t.Fatalf("BuildEnv() error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEnv()=%q, want %q", got, want)
	}
}

func TestBuildEnvDeduplicatesIdenticalLookupNames(t *testing.T) {
	calls := 0
	lookup := func(name string) (string, bool) {
		calls++
		if name != "TOKEN" {
			t.Fatalf("unexpected lookup %q", name)
		}
		return "value", true
	}

	got, err := BuildEnv(EnvSpec{
		SafePath:       "/safe/bin",
		RequiredLookup: []string{"TOKEN", "TOKEN", "TOKEN"},
	}, lookup)
	if err != nil {
		t.Fatalf("BuildEnv() error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("lookup called %d times, want 1", calls)
	}
	want := []string{"PATH=/safe/bin", "TOKEN=value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEnv()=%q, want %q", got, want)
	}
}

func TestBuildEnvNeverLooksUpPATH(t *testing.T) {
	var lookedUp []string
	lookup := func(name string) (string, bool) {
		lookedUp = append(lookedUp, name)
		switch name {
		case "PATH":
			return "/attacker/bin", true
		case "TOKEN":
			return "selected-secret", true
		default:
			return "", false
		}
	}

	got, err := BuildEnv(EnvSpec{
		SafePath:       "/safe/bin",
		RequiredLookup: []string{"PATH", "PATH", "TOKEN"},
	}, lookup)
	if err != nil {
		t.Fatalf("BuildEnv() error: %v", err)
	}
	if !reflect.DeepEqual(lookedUp, []string{"TOKEN"}) {
		t.Fatalf("lookups=%q, want only TOKEN", lookedUp)
	}
	want := []string{"PATH=/safe/bin", "TOKEN=selected-secret"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEnv()=%q, want %q", got, want)
	}
}

func TestBuildEnvRejectsDuplicateAndCaseFoldedCollisions(t *testing.T) {
	tests := []struct {
		name string
		spec EnvSpec
	}{
		{
			name: "duplicate identical fixed",
			spec: EnvSpec{Fixed: []EnvVar{
				{Name: "HOME", Value: "/runtime"},
				{Name: "HOME", Value: "/runtime"},
			}, SafePath: "/safe/bin"},
		},
		{
			name: "duplicate conflicting fixed",
			spec: EnvSpec{Fixed: []EnvVar{
				{Name: "HOME", Value: "/one"},
				{Name: "HOME", Value: "/two"},
			}, SafePath: "/safe/bin"},
		},
		{
			name: "fixed and lookup collision",
			spec: EnvSpec{
				Fixed:          []EnvVar{{Name: "TOKEN", Value: "fixed-secret"}},
				SafePath:       "/safe/bin",
				RequiredLookup: []string{"TOKEN"},
			},
		},
		{
			name: "fixed PATH conflicts with safe path",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Name: "PATH", Value: "/attacker/bin"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "case folded fixed PATH",
			spec: EnvSpec{
				Fixed:    []EnvVar{{Name: "Path", Value: "/attacker/bin"}},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "case folded fixed names",
			spec: EnvSpec{
				Fixed: []EnvVar{
					{Name: "SystemRoot", Value: `C:\Windows`},
					{Name: "SYSTEMROOT", Value: `D:\Attacker`},
				},
				SafePath: "/safe/bin",
			},
		},
		{
			name: "case folded lookup names",
			spec: EnvSpec{
				SafePath:       "/safe/bin",
				RequiredLookup: []string{"SystemRoot", "SYSTEMROOT"},
			},
		},
		{
			name: "case folded cross source",
			spec: EnvSpec{
				Fixed:          []EnvVar{{Name: "SystemRoot", Value: `C:\Windows`}},
				SafePath:       "/safe/bin",
				RequiredLookup: []string{"SYSTEMROOT"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildEnv(test.spec, func(string) (string, bool) {
				return "looked-up-secret", true
			})
			if err == nil || err.Error() != "provider environment has conflicting names" {
				t.Fatalf("BuildEnv() error=%v", err)
			}
			for _, forbidden := range []string{
				"fixed-secret",
				"looked-up-secret",
				"/attacker/bin",
				"TOKEN",
				"HOME",
			} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error %q exposed %q", err, forbidden)
				}
			}
		})
	}
}

func TestBuildEnvReturnsFreshOwnedSlices(t *testing.T) {
	spec := EnvSpec{
		Fixed:    []EnvVar{{Name: "HOME", Value: "/runtime"}},
		SafePath: "/safe/bin",
	}
	first, err := BuildEnv(spec, nil)
	if err != nil {
		t.Fatalf("first BuildEnv() error: %v", err)
	}
	second, err := BuildEnv(spec, nil)
	if err != nil {
		t.Fatalf("second BuildEnv() error: %v", err)
	}

	first[0] = "MUTATED=secret"
	if reflect.DeepEqual(first, second) {
		t.Fatal("BuildEnv returned shared output slice")
	}
	want := []string{"HOME=/runtime", "PATH=/safe/bin"}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("second BuildEnv() changed to %q, want %q", second, want)
	}
}
