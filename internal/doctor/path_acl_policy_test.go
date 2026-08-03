package doctor

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

const (
	testUserSID       = "S-1-5-21-1000"
	testEnabledGroup  = "S-1-5-21-2000"
	testDisabledGroup = "S-1-5-21-3000"
	testDenyOnlyGroup = "S-1-5-21-4000"
	testUntrustedSID  = "S-1-1-0"
)

func TestWindowsACLMasksAreExact(t *testing.T) {
	want := map[string]aclMask{
		"integrity forbidden":           0x000d0156,
		"confidentiality and integrity": 0x000d01ff,
		"executable required":           0x000000a1,
		"PATH directory required":       0x000000a1,
		"private ancestor required":     0x00000020,
		"config home required":          0x000000e7,
		"service credential required":   0x00000081,
		"all supported concrete rights": 0x001f01ff,
	}
	got := map[string]aclMask{
		"integrity forbidden":           aclIntegrityForbidden,
		"confidentiality and integrity": aclConfidentialityForbidden,
		"executable required":           aclExecutableRequired,
		"PATH directory required":       aclPathDirectoryRequired,
		"private ancestor required":     aclPrivateAncestorRequired,
		"config home required":          aclConfigHomeRequired,
		"service credential required":   aclCredentialRequired,
		"all supported concrete rights": aclConcreteRights,
	}
	for name, wantMask := range want {
		if got[name] != wantMask {
			t.Errorf("%s mask = %#08x, want %#08x", name, got[name], wantMask)
		}
	}
}

func TestWindowsPathKindPolicySelectionIsPortableAndClosed(t *testing.T) {
	for _, test := range []struct {
		kind         pathKind
		leaf         windowsACLPolicy
		ancestor     windowsACLPolicy
		wantSelected bool
	}{
		{pathKindExecutable, windowsExecutablePolicy, windowsPathDirectoryPolicy, true},
		{pathKindEntrypoint, windowsExecutablePolicy, windowsPathDirectoryPolicy, true},
		{pathKindConfigHome, windowsConfigHomePolicy, windowsPrivateAncestorPolicy, true},
		{pathKindCredential, windowsCredentialPolicy, windowsPrivateAncestorPolicy, true},
		{pathKindSafeDirectory, windowsPathDirectoryPolicy, windowsPathDirectoryPolicy, true},
		{pathKind(255), windowsACLPolicyUnknown, windowsACLPolicyUnknown, false},
	} {
		leaf, ancestor, selected := windowsPoliciesForPathKind(test.kind)
		if leaf != test.leaf || ancestor != test.ancestor ||
			selected != test.wantSelected {
			t.Errorf(
				"kind %d selection=(%d, %d, %t), want (%d, %d, %t)",
				test.kind,
				leaf,
				ancestor,
				selected,
				test.leaf,
				test.ancestor,
				test.wantSelected,
			)
		}
	}
}

func TestWindowsLeafShapePolicyUsesWindowsSeparatorsOnEveryHost(t *testing.T) {
	for _, path := range []string{
		`C:\bin\codex.cmd`,
		`C:\bin\CLAUDE.BAT`,
		`C:/bin/gemini.CmD`,
	} {
		if windowsLeafShapeAllowed(pathKindExecutable, path) {
			t.Fatalf("shell shim %q was accepted", path)
		}
	}
	for _, path := range []string{
		`C:\bin\codex.exe`,
		`C:/bin/extensionless`,
	} {
		if !windowsLeafShapeAllowed(pathKindExecutable, path) {
			t.Fatalf("native executable %q was rejected", path)
		}
	}
	for _, path := range []string{
		`C:\cli\index.js`,
		`C:/cli/index.MJS`,
	} {
		if !windowsLeafShapeAllowed(pathKindEntrypoint, path) {
			t.Fatalf("Node entrypoint %q was rejected", path)
		}
	}
	for _, path := range []string{
		`C:\cli\index.cjs`,
		`C:/cli/index.exe`,
	} {
		if windowsLeafShapeAllowed(pathKindEntrypoint, path) {
			t.Fatalf("Node entrypoint %q was accepted", path)
		}
	}
	if windowsLeafShapeAllowed(pathKind(255), `C:\bin\tool.exe`) {
		t.Fatal("unknown path kind was accepted")
	}
}

func TestExpandWindowsGenericRights(t *testing.T) {
	tests := []struct {
		name string
		mask aclMask
		want aclMask
	}{
		{
			name: "read",
			mask: aclGenericRead,
			want: aclReadControl | aclReadData | aclReadEA |
				aclReadAttributes | aclSynchronize,
		},
		{
			name: "write",
			mask: aclGenericWrite,
			want: aclReadControl | aclWriteData | aclAppendData |
				aclWriteEA | aclWriteAttributes | aclSynchronize,
		},
		{
			name: "execute",
			mask: aclGenericExecute,
			want: aclReadControl | aclExecute | aclReadAttributes |
				aclSynchronize,
		},
		{name: "all", mask: aclGenericAll, want: aclConcreteRights},
		{
			name: "combined and concrete",
			mask: aclGenericRead | aclGenericExecute | aclDeleteChild,
			want: aclReadControl | aclReadData | aclReadEA |
				aclReadAttributes | aclExecute | aclSynchronize |
				aclDeleteChild,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, object := range []aclObject{aclObjectFile, aclObjectDirectory} {
				got, ok := expandWindowsGeneric(test.mask, object)
				if !ok || got != test.want {
					t.Errorf("expandWindowsGeneric(%#x, %v) = (%#x, %v), want (%#x, true)",
						test.mask, object, got, ok, test.want)
				}
			}
		})
	}
	for _, test := range []struct {
		name   string
		mask   aclMask
		object aclObject
	}{
		{name: "unknown object", mask: aclGenericRead, object: aclObjectUnknown},
		{name: "maximum allowed", mask: aclMaximumAllowed, object: aclObjectFile},
		{name: "system security", mask: aclAccessSystemSecurity, object: aclObjectFile},
		{name: "unknown right", mask: 0x04000000, object: aclObjectDirectory},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := expandWindowsGeneric(test.mask, test.object); ok {
				t.Fatal("unsupported generic/object input was accepted")
			}
		})
	}
}

func TestEvaluateWindowsACLRejectsUnsupportedDescriptorTokenAndPathEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*windowsACLSnapshot)
	}{
		{name: "descriptor unsupported", mutate: func(s *windowsACLSnapshot) { s.DescriptorSupported = false }},
		{name: "token unsupported", mutate: func(s *windowsACLSnapshot) { s.Token.Supported = false }},
		{name: "DACL absent", mutate: func(s *windowsACLSnapshot) { s.DACLPresent = false }},
		{name: "null DACL", mutate: func(s *windowsACLSnapshot) { s.DACLNull = true }},
		{name: "owner absent", mutate: func(s *windowsACLSnapshot) { s.OwnerSID = "" }},
		{name: "token user absent", mutate: func(s *windowsACLSnapshot) { s.Token.UserSID = "" }},
		{name: "reparse point", mutate: func(s *windowsACLSnapshot) { s.Reparse = true }},
		{name: "wrong object type", mutate: func(s *windowsACLSnapshot) { s.Object = aclObjectDirectory }},
		{name: "changed identity", mutate: func(s *windowsACLSnapshot) { s.ReopenedID.Index++ }},
		{name: "missing canonical path", mutate: func(s *windowsACLSnapshot) { s.CanonicalPath = "" }},
		{name: "relative canonical path", mutate: func(s *windowsACLSnapshot) { s.CanonicalPath = `relative\tool.exe` }},
		{name: "empty DACL", mutate: func(s *windowsACLSnapshot) { s.ACEs = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
			test.mutate(&snapshot)
			assertUnsafeWindowsACL(t, snapshot, windowsExecutablePolicy)
		})
	}
}

func TestEvaluateWindowsACLAppliesExactOwnerPolicy(t *testing.T) {
	integrityPolicies := []windowsACLPolicy{
		windowsExecutablePolicy,
		windowsPathDirectoryPolicy,
		windowsPrivateAncestorPolicy,
	}
	for _, policy := range integrityPolicies {
		for _, owner := range []string{
			testUserSID,
			aclLocalSystemSID,
			aclBuiltinAdministratorsSID,
			aclTrustedInstallerSID,
		} {
			t.Run(fmt.Sprintf("integrity_%d_%s", policy, owner), func(t *testing.T) {
				snapshot := safeWindowsSnapshot(policy)
				snapshot.OwnerSID = owner
				assertSafeWindowsACL(t, snapshot, policy)
			})
		}
		t.Run(fmt.Sprintf("integrity_%d_untrusted", policy), func(t *testing.T) {
			snapshot := safeWindowsSnapshot(policy)
			snapshot.OwnerSID = testUntrustedSID
			assertUnsafeWindowsACL(t, snapshot, policy)
		})
	}

	for _, policy := range []windowsACLPolicy{
		windowsConfigHomePolicy,
		windowsCredentialPolicy,
	} {
		t.Run(fmt.Sprintf("private_%d_user", policy), func(t *testing.T) {
			assertSafeWindowsACL(t, safeWindowsSnapshot(policy), policy)
		})
		for _, owner := range []string{
			aclLocalSystemSID,
			aclBuiltinAdministratorsSID,
			aclTrustedInstallerSID,
			testUntrustedSID,
		} {
			t.Run(fmt.Sprintf("private_%d_reject_%s", policy, owner), func(t *testing.T) {
				snapshot := safeWindowsSnapshot(policy)
				snapshot.OwnerSID = owner
				assertUnsafeWindowsACL(t, snapshot, policy)
			})
		}
	}
}

func TestEvaluateWindowsACLUsesOrderedAllowDenySemantics(t *testing.T) {
	for _, test := range []struct {
		name string
		aces []aclACE
		safe bool
	}{
		{
			name: "allow before deny retains granted rights",
			aces: []aclACE{
				allowACE(testUserSID, aclExecutableRequired),
				denyACE(testUserSID, aclExecutableRequired),
			},
			safe: true,
		},
		{
			name: "deny before allow rejects outstanding rights",
			aces: []aclACE{
				denyACE(testUserSID, aclExecutableRequired),
				allowACE(testUserSID, aclExecutableRequired),
			},
		},
		{
			name: "partial grants accumulate",
			aces: []aclACE{
				allowACE(testUserSID, aclReadData),
				allowACE(testUserSID, aclExecute|aclReadAttributes),
			},
			safe: true,
		},
		{
			name: "missing one required right rejects",
			aces: []aclACE{
				allowACE(testUserSID, aclExecutableRequired&^aclReadAttributes),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
			snapshot.ACEs = test.aces
			if test.safe {
				assertSafeWindowsACL(t, snapshot, windowsExecutablePolicy)
			} else {
				assertUnsafeWindowsACL(t, snapshot, windowsExecutablePolicy)
			}
		})
	}
}

func TestEvaluateWindowsACLHandlesEnabledDisabledAndDenyOnlyGroups(t *testing.T) {
	baseToken := aclTokenSnapshot{
		Supported: true,
		UserSID:   testUserSID,
		Groups: []aclTokenGroup{
			{SID: testEnabledGroup, Enabled: true},
			{SID: testDisabledGroup},
			{SID: testDenyOnlyGroup, DenyOnly: true},
		},
	}
	tests := []struct {
		name string
		aces []aclACE
		safe bool
	}{
		{name: "enabled group grants", aces: []aclACE{allowACE(testEnabledGroup, aclExecutableRequired)}, safe: true},
		{name: "disabled group does not grant", aces: []aclACE{allowACE(testDisabledGroup, aclExecutableRequired)}},
		{name: "deny-only group does not grant", aces: []aclACE{allowACE(testDenyOnlyGroup, aclExecutableRequired)}},
		{
			name: "enabled group deny applies",
			aces: []aclACE{
				denyACE(testEnabledGroup, aclExecutableRequired),
				allowACE(testUserSID, aclExecutableRequired),
			},
		},
		{
			name: "deny-only group deny applies",
			aces: []aclACE{
				denyACE(testDenyOnlyGroup, aclExecutableRequired),
				allowACE(testUserSID, aclExecutableRequired),
			},
		},
		{
			name: "disabled group deny is ignored",
			aces: []aclACE{
				denyACE(testDisabledGroup, aclExecutableRequired),
				allowACE(testUserSID, aclExecutableRequired),
			},
			safe: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
			snapshot.Token = baseToken
			snapshot.ACEs = test.aces
			if test.safe {
				assertSafeWindowsACL(t, snapshot, windowsExecutablePolicy)
			} else {
				assertUnsafeWindowsACL(t, snapshot, windowsExecutablePolicy)
			}
		})
	}

	snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
	snapshot.Token.Groups = []aclTokenGroup{{
		SID:      testEnabledGroup,
		Enabled:  true,
		DenyOnly: true,
	}}
	assertUnsafeWindowsACL(t, snapshot, windowsExecutablePolicy)
}

func TestEvaluateWindowsACLAppliesInheritedButNotInheritOnlyACEs(t *testing.T) {
	for _, test := range []struct {
		name string
		aces []aclACE
		safe bool
	}{
		{
			name: "inherit-only deny skipped",
			aces: []aclACE{
				withFlags(denyACE(testUserSID, aclExecutableRequired), aclACEInheritOnly),
				allowACE(testUserSID, aclExecutableRequired),
			},
			safe: true,
		},
		{
			name: "applicable inherited allow grants",
			aces: []aclACE{
				withFlags(allowACE(testUserSID, aclExecutableRequired), aclACEInherited),
			},
			safe: true,
		},
		{
			name: "applicable inherited deny rejects",
			aces: []aclACE{
				withFlags(denyACE(testUserSID, aclExecutableRequired), aclACEInherited),
				allowACE(testUserSID, aclExecutableRequired),
			},
		},
		{
			name: "propagation flags do not stop current-object applicability",
			aces: []aclACE{
				withFlags(allowACE(testUserSID, aclExecutableRequired), aclACEObjectInherit|aclACEContainerInherit|aclACENoPropagate),
			},
			safe: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
			snapshot.ACEs = test.aces
			if test.safe {
				assertSafeWindowsACL(t, snapshot, windowsExecutablePolicy)
			} else {
				assertUnsafeWindowsACL(t, snapshot, windowsExecutablePolicy)
			}
		})
	}
}

func TestEvaluateWindowsACLRejectsUnsafeUntrustedAllowIndependentOfDeny(t *testing.T) {
	for _, dangerous := range individualMaskBits(aclIntegrityForbidden) {
		t.Run(fmt.Sprintf("integrity_%08x", dangerous), func(t *testing.T) {
			snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
			snapshot.ACEs = append([]aclACE{
				denyACE(testUntrustedSID, dangerous),
				allowACE(testUntrustedSID, dangerous),
			}, snapshot.ACEs...)
			assertUnsafeWindowsACL(t, snapshot, windowsExecutablePolicy)
		})
	}

	snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
	snapshot.ACEs = append([]aclACE{
		allowACE(testUntrustedSID, aclReadData|aclReadEA|aclExecute|aclReadAttributes),
	}, snapshot.ACEs...)
	assertSafeWindowsACL(t, snapshot, windowsExecutablePolicy)

	for _, policy := range []windowsACLPolicy{
		windowsConfigHomePolicy,
		windowsCredentialPolicy,
	} {
		for _, dangerous := range individualMaskBits(aclConfidentialityForbidden) {
			t.Run(fmt.Sprintf("private_%d_%08x", policy, dangerous), func(t *testing.T) {
				snapshot := safeWindowsSnapshot(policy)
				snapshot.ACEs = append([]aclACE{
					denyACE(testUntrustedSID, dangerous),
					allowACE(testUntrustedSID, dangerous),
				}, snapshot.ACEs...)
				assertUnsafeWindowsACL(t, snapshot, policy)
			})
		}
	}
}

func TestEvaluateWindowsACLUsesPolicySpecificTrustedGrantPrincipals(t *testing.T) {
	for _, trusted := range []string{
		testUserSID,
		aclLocalSystemSID,
		aclBuiltinAdministratorsSID,
		aclTrustedInstallerSID,
	} {
		t.Run("integrity_"+trusted, func(t *testing.T) {
			snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
			snapshot.ACEs = append([]aclACE{
				allowACE(trusted, aclIntegrityForbidden),
			}, snapshot.ACEs...)
			assertSafeWindowsACL(t, snapshot, windowsExecutablePolicy)
		})
	}

	for _, policy := range []windowsACLPolicy{
		windowsConfigHomePolicy,
		windowsCredentialPolicy,
	} {
		for _, trusted := range []string{
			testUserSID,
			aclLocalSystemSID,
			aclBuiltinAdministratorsSID,
		} {
			t.Run(fmt.Sprintf("private_%d_%s", policy, trusted), func(t *testing.T) {
				snapshot := safeWindowsSnapshot(policy)
				snapshot.ACEs = append([]aclACE{
					allowACE(trusted, aclConfidentialityForbidden),
				}, snapshot.ACEs...)
				assertSafeWindowsACL(t, snapshot, policy)
			})
		}
		t.Run(fmt.Sprintf("private_%d_reject_TrustedInstaller", policy), func(t *testing.T) {
			snapshot := safeWindowsSnapshot(policy)
			snapshot.ACEs = append([]aclACE{
				allowACE(aclTrustedInstallerSID, aclReadData),
			}, snapshot.ACEs...)
			assertUnsafeWindowsACL(t, snapshot, policy)
		})
	}
}

func TestEvaluateWindowsACLRequiresEveryPolicyAccessBit(t *testing.T) {
	for _, test := range []struct {
		name     string
		policy   windowsACLPolicy
		required aclMask
	}{
		{name: "executable", policy: windowsExecutablePolicy, required: aclExecutableRequired},
		{name: "PATH directory", policy: windowsPathDirectoryPolicy, required: aclPathDirectoryRequired},
		{name: "private ancestor", policy: windowsPrivateAncestorPolicy, required: aclPrivateAncestorRequired},
		{name: "config home", policy: windowsConfigHomePolicy, required: aclConfigHomeRequired},
		{name: "credential", policy: windowsCredentialPolicy, required: aclCredentialRequired},
	} {
		t.Run(test.name+" exact", func(t *testing.T) {
			snapshot := safeWindowsSnapshot(test.policy)
			snapshot.ACEs = []aclACE{allowACE(testUserSID, test.required)}
			assertSafeWindowsACL(t, snapshot, test.policy)
		})
		for _, bit := range individualMaskBits(test.required) {
			t.Run(fmt.Sprintf("%s missing_%08x", test.name, bit), func(t *testing.T) {
				snapshot := safeWindowsSnapshot(test.policy)
				snapshot.ACEs = []aclACE{allowACE(testUserSID, test.required&^bit)}
				assertUnsafeWindowsACL(t, snapshot, test.policy)
			})
		}
	}
}

func TestEvaluateWindowsACLRejectsUnsupportedACEAndTokenForms(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*windowsACLSnapshot)
	}{
		{
			name: "unknown ACE type",
			mutate: func(s *windowsACLSnapshot) {
				s.ACEs = []aclACE{{Kind: aclACEUnknown, SID: testUserSID, Mask: aclExecutableRequired}}
			},
		},
		{
			name: "unsupported ACE type even when inherit-only",
			mutate: func(s *windowsACLSnapshot) {
				s.ACEs = []aclACE{{Kind: aclACEUnknown, Flags: aclACEInheritOnly, SID: testUserSID, Mask: aclExecutableRequired}}
			},
		},
		{
			name: "unsupported ACE flag",
			mutate: func(s *windowsACLSnapshot) {
				s.ACEs[0].Flags = 0x20
			},
		},
		{
			name: "maximum allowed in ACE",
			mutate: func(s *windowsACLSnapshot) {
				s.ACEs[0].Mask |= aclMaximumAllowed
			},
		},
		{
			name: "system security in ACE",
			mutate: func(s *windowsACLSnapshot) {
				s.ACEs[0].Mask |= aclAccessSystemSecurity
			},
		},
		{
			name: "empty ACE SID",
			mutate: func(s *windowsACLSnapshot) {
				s.ACEs[0].SID = ""
			},
		},
		{
			name: "enabled deny-only token group",
			mutate: func(s *windowsACLSnapshot) {
				s.Token.Groups = []aclTokenGroup{{SID: testEnabledGroup, Enabled: true, DenyOnly: true}}
			},
		},
		{
			name: "empty token group SID",
			mutate: func(s *windowsACLSnapshot) {
				s.Token.Groups = []aclTokenGroup{{Enabled: true}}
			},
		},
		{
			name: "duplicate token group with conflicting state",
			mutate: func(s *windowsACLSnapshot) {
				s.Token.Groups = []aclTokenGroup{
					{SID: testEnabledGroup, Enabled: true},
					{SID: testEnabledGroup, DenyOnly: true},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := safeWindowsSnapshot(windowsExecutablePolicy)
			test.mutate(&snapshot)
			assertUnsafeWindowsACL(t, snapshot, windowsExecutablePolicy)
		})
	}
}

func TestWindowsPathCaseKeyCanonicalizesDriveAndUNCForms(t *testing.T) {
	for _, test := range []struct {
		name  string
		paths []string
		want  string
	}{
		{
			name: "drive",
			paths: []string{
				`C:\Program Files\Provider\tool.EXE`,
				`c:/program files/provider/./TOOL.exe`,
				`\\?\C:\PROGRAM FILES\PROVIDER\tool.exe`,
			},
			want: `c:\program files\provider\tool.exe`,
		},
		{
			name: "drive clean parent",
			paths: []string{
				`C:\safe\child\..\tool.exe`,
				`c:\SAFE\tool.exe`,
			},
			want: `c:\safe\tool.exe`,
		},
		{
			name: "UNC",
			paths: []string{
				`\\Server\Share\Provider\tool.exe`,
				`//server/share/provider/./TOOL.EXE`,
				`\\?\UNC\SERVER\SHARE\provider\tool.exe`,
			},
			want: `\\server\share\provider\tool.exe`,
		},
		{
			name:  "UNC share root",
			paths: []string{`\\Server\Share`, `//server/share/`},
			want:  `\\server\share`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var keys []string
			for _, path := range test.paths {
				key, err := windowsPathCaseKey(path)
				if err != nil {
					t.Fatalf("windowsPathCaseKey(%q): %v", path, err)
				}
				keys = append(keys, key)
			}
			if !slices.Equal(keys, makeRepeated(test.want, len(keys))) {
				t.Fatalf("keys = %q, want every key %q", keys, test.want)
			}
		})
	}
}

func TestWindowsPathCaseKeyRejectsUnsupportedForms(t *testing.T) {
	for _, path := range []string{
		"",
		"relative\\tool.exe",
		`C:relative\tool.exe`,
		`\root-relative\tool.exe`,
		`\\server`,
		`\\.\pipe\name`,
		`\\?\GLOBALROOT\Device\HarddiskVolume1\tool.exe`,
		`C:\safe\tool.exe:stream`,
		"C:\\safe\\tool\x00.exe",
	} {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			if _, err := windowsPathCaseKey(path); !errors.Is(err, errInvalidWindowsPathKey) {
				t.Fatalf("error = %v, want errInvalidWindowsPathKey", err)
			}
		})
	}
}

func safeWindowsSnapshot(policy windowsACLPolicy) windowsACLSnapshot {
	required, object, ok := windowsPolicyRequired(policy)
	if !ok {
		panic("test requested unknown policy")
	}
	return windowsACLSnapshot{
		DescriptorSupported: true,
		DACLPresent:         true,
		Object:              object,
		OwnerSID:            testUserSID,
		Token: aclTokenSnapshot{
			Supported: true,
			UserSID:   testUserSID,
		},
		ACEs: []aclACE{allowACE(testUserSID, required)},
		OpenedID: windowsFileID{
			Volume: 7,
			Index:  11,
		},
		ReopenedID: windowsFileID{
			Volume: 7,
			Index:  11,
		},
		CanonicalPath: `C:\safe\object`,
	}
}

func allowACE(sid string, mask aclMask) aclACE {
	return aclACE{Kind: aclACEAllow, SID: sid, Mask: mask}
}

func denyACE(sid string, mask aclMask) aclACE {
	return aclACE{Kind: aclACEDeny, SID: sid, Mask: mask}
}

func withFlags(ace aclACE, flags uint8) aclACE {
	ace.Flags = flags
	return ace
}

func individualMaskBits(mask aclMask) []aclMask {
	bits := make([]aclMask, 0, 32)
	for bit := aclMask(1); bit != 0; bit <<= 1 {
		if mask&bit != 0 {
			bits = append(bits, bit)
		}
	}
	return bits
}

func assertSafeWindowsACL(
	t *testing.T,
	snapshot windowsACLSnapshot,
	policy windowsACLPolicy,
) {
	t.Helper()
	if err := evaluateWindowsACL(snapshot, policy); err != nil {
		t.Fatalf("evaluateWindowsACL() error = %v", err)
	}
}

func assertUnsafeWindowsACL(
	t *testing.T,
	snapshot windowsACLSnapshot,
	policy windowsACLPolicy,
) {
	t.Helper()
	if err := evaluateWindowsACL(snapshot, policy); !errors.Is(err, errUnsafeWindowsACL) {
		t.Fatalf("evaluateWindowsACL() error = %v, want errUnsafeWindowsACL", err)
	}
}

func makeRepeated(value string, count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = value
	}
	return values
}
