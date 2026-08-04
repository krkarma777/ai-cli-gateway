//go:build !windows

package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestResolveUnixProviderCommandPinsExactEnvNodeLauncher(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(strconv.Quote(ending), func(t *testing.T) {
			fixture := newUnixNodeLauncherFixture(t, ending)
			calls := 0
			command, ok := resolveProviderCommand(
				fixture.launcher,
				nil,
				func(name string) (string, error) {
					calls++
					if name != "node" {
						t.Fatalf("lookup name=%q", name)
					}
					return fixture.node, nil
				},
			)
			if !ok || calls != 1 ||
				command.Executable.Resolved != fixture.node ||
				command.Entrypoint == nil ||
				command.Entrypoint.Clean != fixture.shim ||
				command.Entrypoint.Resolved != fixture.script ||
				!slices.Equal(command.PrefixArgs, []string{fixture.script}) {
				t.Fatalf("command=%+v calls=%d ok=%v", command, calls, ok)
			}
		})
	}
}

func TestResolveUnixProviderCommandKeepsNonNodeLaunchersNative(t *testing.T) {
	for _, payload := range []string{
		"native fixture",
		"#!/usr/bin/env -S node\n",
		"#!/usr/bin/env node --flag\n",
		"#!/usr/bin/env Node\n",
		"#!/usr/bin/env node",
		"#!/bin/sh\n",
	} {
		t.Run(strconv.Quote(payload), func(t *testing.T) {
			fixture := newUnixNodeLauncherFixture(t, "\n")
			//nolint:gosec // The executable fixture deliberately requires execute bits.
			if err := os.WriteFile(fixture.script, []byte(payload), 0o700); err != nil {
				t.Fatal(err)
			}
			//nolint:gosec // The executable fixture deliberately requires execute bits.
			if err := os.Chmod(fixture.script, 0o700); err != nil {
				t.Fatal(err)
			}
			launcher, disposition := validateExecutablePath(fixture.shim)
			if disposition != pathSafe {
				t.Fatalf("launcher disposition=%v", disposition)
			}

			command, ok := resolveProviderCommand(
				launcher,
				nil,
				func(string) (string, error) {
					t.Fatal("lookup called for native launcher")
					return "", nil
				},
			)
			if !ok || command.Executable.Resolved != launcher.Resolved ||
				command.Entrypoint != nil || len(command.PrefixArgs) != 0 {
				t.Fatalf("command=%+v ok=%v", command, ok)
			}
		})
	}
}

func TestResolveUnixProviderCommandRejectsUnsafeNodeResolution(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(t *testing.T, fixture unixNodeLauncherFixture) (string, error)
	}{
		{
			name: "nonempty configured prefix",
			resolve: func(t *testing.T, _ unixNodeLauncherFixture) (string, error) {
				t.Fatal("lookup called with configured prefix")
				return "", nil
			},
		},
		{
			name: "lookup error",
			resolve: func(_ *testing.T, _ unixNodeLauncherFixture) (string, error) {
				return "", errors.New("node unavailable")
			},
		},
		{
			name: "relative lookup result",
			resolve: func(_ *testing.T, _ unixNodeLauncherFixture) (string, error) {
				return "node", nil
			},
		},
		{
			name: "non-executable Node",
			resolve: func(t *testing.T, fixture unixNodeLauncherFixture) (string, error) {
				if err := os.Chmod(fixture.node, 0o600); err != nil {
					t.Fatal(err)
				}
				return fixture.node, nil
			},
		},
		{
			name: "group/world-writable Node",
			resolve: func(t *testing.T, fixture unixNodeLauncherFixture) (string, error) {
				//nolint:gosec // This deliberately creates an unsafe executable fixture.
				if err := os.Chmod(fixture.node, 0o722); err != nil {
					t.Fatal(err)
				}
				return fixture.node, nil
			},
		},
		{
			name: "unsafe Node ancestor",
			resolve: func(t *testing.T, fixture unixNodeLauncherFixture) (string, error) {
				//nolint:gosec // This deliberately creates an unsafe directory fixture.
				if err := os.Chmod(filepath.Dir(fixture.node), 0o770); err != nil {
					t.Fatal(err)
				}
				return fixture.node, nil
			},
		},
		{
			name: "launcher replacement",
			resolve: func(t *testing.T, fixture unixNodeLauncherFixture) (string, error) {
				if err := os.Remove(fixture.shim); err != nil {
					t.Fatal(err)
				}
				writeUnixTestFile(t, fixture.shim, 0o700)
				return fixture.node, nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixNodeLauncherFixture(t, "\n")
			prefix := []string(nil)
			if test.name == "nonempty configured prefix" {
				prefix = []string{"--configured"}
			}
			if command, ok := resolveProviderCommand(
				fixture.launcher,
				prefix,
				func(name string) (string, error) {
					if name != "node" {
						t.Fatalf("lookup name=%q", name)
					}
					return test.resolve(t, fixture)
				},
			); ok {
				t.Fatalf("unsafe command accepted: %+v", command)
			}
		})
	}
}

func TestBuildSafePathIncludesLauncherAndInterpreterDirectoriesOnce(t *testing.T) {
	fixture := newUnixNodeLauncherFixture(t, "\n")
	root := filepath.Dir(filepath.Dir(fixture.node))
	nodeAliasDir := filepath.Join(root, "node-alias-bin")
	if err := os.Mkdir(nodeAliasDir, 0o700); err != nil {
		t.Fatal(err)
	}
	nodeAlias := filepath.Join(nodeAliasDir, "node")
	if err := os.Symlink(fixture.node, nodeAlias); err != nil {
		t.Fatal(err)
	}
	node, disposition := validateExecutablePath(nodeAlias)
	if disposition != pathSafe {
		t.Fatalf("node disposition=%v", disposition)
	}
	launcher, disposition := validateExecutablePath(fixture.shim)
	if disposition != pathSafe {
		t.Fatalf("launcher disposition=%v", disposition)
	}
	tailOne := filepath.Join(root, "tail-one")
	tailTwo := filepath.Join(root, "tail-two")
	for _, directory := range []string{tailOne, tailTwo} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	validatedTailOne, disposition := validateSafeDirectoryPath(tailOne)
	if disposition != pathSafe {
		t.Fatalf("first tail disposition=%v", disposition)
	}
	validatedTailTwo, disposition := validateSafeDirectoryPath(tailTwo)
	if disposition != pathSafe {
		t.Fatalf("second tail disposition=%v", disposition)
	}

	safePath, err := buildSafePath(node, &launcher, platformDefaults{
		SafePathTail: []validatedPath{validatedTailOne, validatedTailOne, validatedTailTwo},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		nodeAliasDir,
		filepath.Dir(fixture.node),
		filepath.Dir(fixture.shim),
		filepath.Dir(fixture.script),
		tailOne,
		tailTwo,
	}, string(os.PathListSeparator))
	if safePath != want {
		t.Fatalf("SafePath=%q, want %q", safePath, want)
	}
}

type unixNodeLauncherFixture struct {
	node     string
	shim     string
	script   string
	launcher validatedPath
}

func newUnixNodeLauncherFixture(
	t *testing.T,
	ending string,
) unixNodeLauncherFixture {
	t.Helper()
	root := newSecureUnixTestTree(t)
	nodeDir := filepath.Join(root, "node-bin")
	shimDir := filepath.Join(root, "shim-bin")
	packageDir := filepath.Join(root, "package-bin")
	for _, directory := range []string{nodeDir, shimDir, packageDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	node := filepath.Join(nodeDir, "node")
	writeUnixTestFile(t, node, 0o700)
	script := filepath.Join(packageDir, "codex.js")
	//nolint:gosec // The executable fixture deliberately requires execute bits.
	if err := os.WriteFile(
		script,
		[]byte("#!/usr/bin/env node"+ending+"fixture"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // The executable fixture deliberately requires execute bits.
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(shimDir, "codex")
	if err := os.Symlink(script, shim); err != nil {
		t.Fatal(err)
	}
	launcher, disposition := validateExecutablePath(shim)
	if disposition != pathSafe {
		t.Fatalf("launcher disposition=%v", disposition)
	}
	return unixNodeLauncherFixture{
		node: node, shim: shim, script: script, launcher: launcher,
	}
}
