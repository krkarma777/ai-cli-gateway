//go:build linux || darwin

package releasepack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"
)

const testSyftCreator = "Tool: syft-1.50.0"

func TestFinalizeSPDXAcceptsClosedFiveTargetProjection(t *testing.T) {
	options, catalog := testSBOMCatalog()
	raw := marshalRawSPDX(t, rawSPDXForCatalog(catalog, options.StagingRoot))

	finalized, err := finalizeSPDX(raw, catalog, options, "0.1.0")
	if err != nil {
		t.Fatalf("finalizeSPDX() error = %v", err)
	}
	if !strings.HasSuffix(string(finalized), "\n") {
		t.Fatal("final SPDX lacks one trailing newline")
	}
	if strings.Contains(string(finalized), "fixture-root-marker") {
		t.Fatal("final SPDX retained raw fixture root data")
	}

	var document map[string]any
	if err := json.Unmarshal(finalized, &document); err != nil {
		t.Fatalf("Unmarshal(final SPDX): %v", err)
	}
	if got := document["name"]; got != "ai-cli-gateway-0.1.0-five-target-collection" {
		t.Fatalf("name = %v", got)
	}
	if got := document["documentNamespace"]; got != "https://github.com/krkarma777/ai-cli-gateway/releases/download/v0.1.0/ai-cli-gateway_0.1.0_sbom.spdx.json" {
		t.Fatalf("documentNamespace = %v", got)
	}
	files := mustJSONArray(t, document, "files")
	if len(files) != len(releaseTargets) {
		t.Fatalf("len(files) = %d, want %d", len(files), len(releaseTargets))
	}
	wantFiles := []string{
		"./darwin_amd64/ai-cli-gateway",
		"./darwin_arm64/ai-cli-gateway",
		"./linux_amd64/ai-cli-gateway",
		"./linux_arm64/ai-cli-gateway",
		"./windows_amd64/ai-cli-gateway.exe",
	}
	gotFiles := make([]string, 0, len(files))
	wantFilesByName := make(map[string]sbomTargetCatalog)
	for _, targetCatalog := range catalog.Targets {
		wantFilesByName["./"+targetCatalog.RelativePath] = targetCatalog
	}
	for _, value := range files {
		file := value.(map[string]any)
		name := file["fileName"].(string)
		gotFiles = append(gotFiles, name)
		targetCatalog, exists := wantFilesByName[name]
		if !exists {
			t.Fatalf("unexpected final file %q", name)
		}
		wantID := "SPDXRef-File-ai-cli-gateway-" + targetCatalog.Target.GOOS + "-" + targetCatalog.Target.GOARCH
		if got := file["SPDXID"]; got != wantID {
			t.Fatalf("file %q SPDXID = %v, want %q", name, got, wantID)
		}
		checksums := file["checksums"].([]any)
		if len(checksums) != 1 || checksums[0].(map[string]any)["algorithm"] != "SHA256" || checksums[0].(map[string]any)["checksumValue"] != targetCatalog.SHA256 {
			t.Fatalf("file %q checksums = %#v", name, checksums)
		}
		if file["licenseConcluded"] != "NOASSERTION" || file["copyrightText"] != "NOASSERTION" {
			t.Fatalf("file has non-fixed legal fields: %#v", file)
		}
	}
	if !slices.Equal(gotFiles, wantFiles) {
		t.Fatalf("file names = %q, want %q", gotFiles, wantFiles)
	}
	packages := mustJSONArray(t, document, "packages")
	if len(packages) != 6 {
		t.Fatalf("len(packages) = %d, want deduplicated main, four dependencies, stdlib", len(packages))
	}
	wantPackageIDs := make(map[string]string)
	wantPURLs := map[string]string{
		releaseModulePath:                          "pkg:golang/" + releaseModulePath,
		"github.com/pelletier/go-toml/v2":          "pkg:golang/github.com/pelletier/go-toml/v2@v2.4.3",
		"github.com/santhosh-tekuri/jsonschema/v6": "pkg:golang/github.com/santhosh-tekuri/jsonschema/v6@v6.0.2",
		"golang.org/x/sys":                         "pkg:golang/golang.org/x/sys@v0.47.0",
		"golang.org/x/text":                        "pkg:golang/golang.org/x/text@v0.14.0",
		"stdlib":                                   "pkg:golang/stdlib@1.26.5",
	}
	packageSortKeys := make([]string, 0, len(packages))
	for _, value := range packages {
		pkg := value.(map[string]any)
		if pkg["name"] == "ai-cli-gateway" {
			t.Fatalf("Windows classifier was projected: %#v", pkg)
		}
		name := pkg["name"].(string)
		versionInfo := pkg["versionInfo"].(string)
		digest := sha256.Sum256([]byte(name + "\x00" + versionInfo))
		wantID := "SPDXRef-Package-" + hex.EncodeToString(digest[:])
		if got := pkg["SPDXID"].(string); got != wantID {
			t.Fatalf("package %q SPDXID = %q, want %q", name, got, wantID)
		}
		wantPackageIDs[name] = wantID
		packageSortKeys = append(packageSortKeys, wantID)
		refs := pkg["externalRefs"].([]any)
		if len(refs) != 1 || refs[0].(map[string]any)["referenceLocator"] != wantPURLs[name] {
			t.Fatalf("package %q externalRefs = %#v, want canonical purl %q", name, refs, wantPURLs[name])
		}
	}
	if !slices.IsSorted(packageSortKeys) {
		t.Fatalf("packages are not sorted by SPDXID: %q", packageSortKeys)
	}

	wantRelationships := make(map[string]struct{})
	for _, targetCatalog := range catalog.Targets {
		fileID := "SPDXRef-File-ai-cli-gateway-" + targetCatalog.Target.GOOS + "-" + targetCatalog.Target.GOARCH
		wantRelationships["SPDXRef-DOCUMENT\x00DESCRIBES\x00"+fileID] = struct{}{}
		components := append([]sbomComponent{targetCatalog.Main}, targetCatalog.Dependencies...)
		components = append(components, sbomComponent{Name: "stdlib", Version: targetCatalog.GoVersion})
		for _, component := range components {
			wantRelationships[fileID+"\x00CONTAINS\x00"+wantPackageIDs[component.Name]] = struct{}{}
		}
	}
	relationships := mustJSONArray(t, document, "relationships")
	if len(relationships) != len(wantRelationships) {
		t.Fatalf("len(relationships) = %d, want %d", len(relationships), len(wantRelationships))
	}
	previous := ""
	for _, value := range relationships {
		relation := value.(map[string]any)
		key := relation["spdxElementId"].(string) + "\x00" + relation["relationshipType"].(string) + "\x00" + relation["relatedSpdxElement"].(string)
		if previous != "" && key < previous {
			t.Fatalf("relationships are not sorted: %q before %q", previous, key)
		}
		if _, exists := wantRelationships[key]; !exists {
			t.Fatalf("unexpected final relationship %q", key)
		}
		delete(wantRelationships, key)
		previous = key
	}
	if len(wantRelationships) != 0 {
		t.Fatalf("missing final relationships: %#v", wantRelationships)
	}
	creation := document["creationInfo"].(map[string]any)
	if got := creation["creators"].([]any); len(got) != 1 || got[0] != testSyftCreator {
		t.Fatalf("creators = %#v, want exact tool only", got)
	}
}

func TestFinalizeSPDXAcceptsCheckedSyft150Fixture(t *testing.T) {
	options, catalog := testSBOMCatalog()
	raw, err := os.ReadFile(filepath.Join("testdata", "syft-1.50.0-five-binaries.spdx.json"))
	if err != nil {
		t.Fatalf("ReadFile(Syft fixture): %v", err)
	}
	finalized, err := finalizeSPDX(raw, catalog, options, "0.1.0")
	if err != nil {
		t.Fatalf("finalizeSPDX(checked fixture) error = %v", err)
	}
	if strings.Contains(string(finalized), "fixture-root-marker") {
		t.Fatal("checked fixture root marker leaked into projection")
	}
	var rawDocument map[string]any
	if err := json.Unmarshal(raw, &rawDocument); err != nil {
		t.Fatalf("Unmarshal(checked fixture): %v", err)
	}
	if got := len(rawDocument["packages"].([]any)); got != 32 {
		t.Fatalf("checked fixture package count = %d, want real Syft count 32", got)
	}
	if got := len(rawDocument["files"].([]any)); got != 5 {
		t.Fatalf("checked fixture file count = %d, want 5", got)
	}
	for _, value := range rawDocument["files"].([]any) {
		file := value.(map[string]any)
		checksums := file["checksums"].([]any)
		if len(checksums) != 2 || rawChecksum(checksums, "SHA1") == nil || rawChecksum(checksums, "SHA256") == nil {
			t.Fatalf("checked fixture file %q checksums = %#v, want real Syft SHA1 and SHA256", file["fileName"], checksums)
		}
	}
	if got := len(rawDocument["relationships"].([]any)); got != 88 {
		t.Fatalf("checked fixture relationship count = %d, want real Syft count 88", got)
	}
	for _, rawID := range []string{"SPDXRef-DocumentRoot-Directory-fixture-root-marker", "SPDXRef-File-darwin-amd64-ai-cli-gateway-fe4b5f1ef8dea09f"} {
		if strings.Contains(string(finalized), rawID) {
			t.Fatalf("finalized SPDX copied raw ID %q", rawID)
		}
	}
}

func TestFinalizeSPDXAcceptsReversedRawFileChecksumOrder(t *testing.T) {
	options, catalog := testSBOMCatalog()
	document := rawSPDXForCatalog(catalog, options.StagingRoot)
	for _, value := range document["files"].([]any) {
		checksums := value.(map[string]any)["checksums"].([]any)
		slices.Reverse(checksums)
	}

	finalized, err := finalizeSPDX(marshalRawSPDX(t, document), catalog, options, "0.1.0")
	if err != nil {
		t.Fatalf("finalizeSPDX(reversed checksums) error = %v", err)
	}
	var finalDocument map[string]any
	if err := json.Unmarshal(finalized, &finalDocument); err != nil {
		t.Fatalf("Unmarshal(final SPDX): %v", err)
	}
	for _, value := range finalDocument["files"].([]any) {
		checksums := value.(map[string]any)["checksums"].([]any)
		if len(checksums) != 1 || checksums[0].(map[string]any)["algorithm"] != "SHA256" {
			t.Fatalf("final checksums = %#v, want trusted SHA256 only", checksums)
		}
	}
}

func TestFinalizeSPDXRejectsInvalidRawFileChecksumSets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]any) []any
	}{
		{"missing SHA1", func(values []any) []any { return removeRawChecksum(values, "SHA1") }},
		{"missing SHA256", func(values []any) []any { return removeRawChecksum(values, "SHA256") }},
		{"duplicate SHA1", func(values []any) []any {
			checksum := cloneRawChecksum(rawChecksum(values, "SHA1"))
			return []any{checksum, cloneRawChecksum(checksum)}
		}},
		{"duplicate SHA256", func(values []any) []any {
			checksum := cloneRawChecksum(rawChecksum(values, "SHA256"))
			return []any{checksum, cloneRawChecksum(checksum)}
		}},
		{"unknown algorithm", func(values []any) []any {
			rawChecksum(values, "SHA1")["algorithm"] = "SHA512"
			return values
		}},
		{"non object", func(values []any) []any { values[0] = true; return values }},
		{"uppercase SHA1", func(values []any) []any {
			rawChecksum(values, "SHA1")["checksumValue"] = strings.Repeat("A", 40)
			return values
		}},
		{"uppercase SHA256", func(values []any) []any {
			rawChecksum(values, "SHA256")["checksumValue"] = strings.Repeat("A", 64)
			return values
		}},
		{"short SHA1", func(values []any) []any {
			rawChecksum(values, "SHA1")["checksumValue"] = strings.Repeat("a", 39)
			return values
		}},
		{"non hex SHA1", func(values []any) []any {
			rawChecksum(values, "SHA1")["checksumValue"] = strings.Repeat("g", 40)
			return values
		}},
		{"short SHA256", func(values []any) []any {
			rawChecksum(values, "SHA256")["checksumValue"] = strings.Repeat("a", 63)
			return values
		}},
		{"non hex SHA256", func(values []any) []any {
			rawChecksum(values, "SHA256")["checksumValue"] = strings.Repeat("g", 64)
			return values
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, catalog := testSBOMCatalog()
			document := rawSPDXForCatalog(catalog, options.StagingRoot)
			file := document["files"].([]any)[0].(map[string]any)
			file["checksums"] = test.mutate(file["checksums"].([]any))
			if _, err := finalizeSPDX(marshalRawSPDX(t, document), catalog, options, "0.1.0"); err == nil {
				t.Fatal("finalizeSPDX() error = nil")
			}
		})
	}
}

func TestFinalizeSPDXValidatesCreatorIdentities(t *testing.T) {
	valid := []struct {
		name     string
		creators []any
	}{
		{"Syft organization", []any{"Organization: Anchore, Inc", testSyftCreator}},
		{"person", []any{"Person: Alice Example", testSyftCreator}},
	}
	for _, test := range valid {
		t.Run("accepts "+test.name, func(t *testing.T) {
			options, catalog := testSBOMCatalog()
			document := rawSPDXForCatalog(catalog, options.StagingRoot)
			document["creationInfo"].(map[string]any)["creators"] = test.creators

			finalized, err := finalizeSPDX(marshalRawSPDX(t, document), catalog, options, "0.1.0")
			if err != nil {
				t.Fatalf("finalizeSPDX() error = %v", err)
			}
			var finalDocument map[string]any
			if err := json.Unmarshal(finalized, &finalDocument); err != nil {
				t.Fatalf("Unmarshal(final SPDX): %v", err)
			}
			got := finalDocument["creationInfo"].(map[string]any)["creators"].([]any)
			if len(got) != 1 || got[0] != testSyftCreator {
				t.Fatalf("creators = %#v, want exact tool only", got)
			}
		})
	}

	invalid := []struct {
		name     string
		creators []any
	}{
		{"second different tool", []any{testSyftCreator, "Tool: other-1.0"}},
		{"no identity prefix", []any{"Anchore, Inc", testSyftCreator}},
		{"person prefix only", []any{"Person: ", testSyftCreator}},
		{"organization prefix only", []any{"Organization: ", testSyftCreator}},
		{"non-string", []any{true, testSyftCreator}},
		{"leading payload whitespace", []any{"Person:  Alice", testSyftCreator}},
		{"trailing payload whitespace", []any{"Organization: Anchore, Inc ", testSyftCreator}},
		{"control character", []any{"Person: Alice\nExample", testSyftCreator}},
	}
	for _, test := range invalid {
		t.Run("rejects "+test.name, func(t *testing.T) {
			options, catalog := testSBOMCatalog()
			document := rawSPDXForCatalog(catalog, options.StagingRoot)
			document["creationInfo"].(map[string]any)["creators"] = test.creators

			if _, err := finalizeSPDX(marshalRawSPDX(t, document), catalog, options, "0.1.0"); err == nil {
				t.Fatal("finalizeSPDX() error = nil")
			}
		})
	}
}

func TestFinalizeSPDXAcceptsInverseContainmentDirection(t *testing.T) {
	options, catalog := testSBOMCatalog()
	document := rawSPDXForCatalog(catalog, options.StagingRoot)
	for _, relationship := range document["relationships"].([]any) {
		relation := relationship.(map[string]any)
		if relation["relationshipType"] == "CONTAINS" {
			relation["spdxElementId"], relation["relatedSpdxElement"] = relation["relatedSpdxElement"], relation["spdxElementId"]
			relation["relationshipType"] = "CONTAINED_BY"
		}
	}
	if _, err := finalizeSPDX(marshalRawSPDX(t, document), catalog, options, "0.1.0"); err != nil {
		t.Fatalf("finalizeSPDX(inverse containment) error = %v", err)
	}
}

func TestFinalizeSPDXRejectsIncompleteAndDuplicateJSON(t *testing.T) {
	options, catalog := testSBOMCatalog()
	skeleton := []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","creationInfo":{"creators":["Tool: syft-1.50.0"]},"packages":[],"relationships":[]}`)
	if _, err := finalizeSPDX(skeleton, catalog, options, "0.1.0"); err == nil {
		t.Fatal("finalizeSPDX(incomplete) error = nil")
	}
	duplicate := []byte(`{"spdxVersion":"SPDX-2.3","spdxVersion":"SPDX-2.3"}`)
	if _, err := finalizeSPDX(duplicate, catalog, options, "0.1.0"); err == nil {
		t.Fatal("finalizeSPDX(duplicate JSON key) error = nil")
	}
}

func TestFinalizeSPDXRejectsParserDepthAndNumberBounds(t *testing.T) {
	options, catalog := testSBOMCatalog()
	base := marshalRawSPDX(t, rawSPDXForCatalog(catalog, options.StagingRoot))
	base = base[:len(base)-1]
	deep := append(append([]byte{}, base...), []byte(`,"deep":`+strings.Repeat("[", 65)+`null`+strings.Repeat("]", 65)+`}`)...)
	if _, err := finalizeSPDX(deep, catalog, options, "0.1.0"); err == nil {
		t.Fatal("finalizeSPDX(over-depth) error = nil")
	}
	longNumber := append(append([]byte{}, base...), []byte(`,"number":`+strings.Repeat("1", 129)+`}`)...)
	if _, err := finalizeSPDX(longNumber, catalog, options, "0.1.0"); err == nil {
		t.Fatal("finalizeSPDX(overlong number) error = nil")
	}
}

func TestFinalizeSPDXRejectsClosedWorldViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any, *sbomCatalog, *SBOMOptions)
	}{
		{"missing creator", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			doc["creationInfo"].(map[string]any)["creators"] = []any{"Tool: syft-1.49.0"}
		}},
		{"empty packages", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) { doc["packages"] = []any{} }},
		{"empty relationships", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) { doc["relationships"] = []any{} }},
		{"duplicate package ID", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			packages := doc["packages"].([]any)
			packages[1].(map[string]any)["SPDXID"] = packages[0].(map[string]any)["SPDXID"]
		}},
		{"dangling relationship", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			rels := doc["relationships"].([]any)
			rels[0].(map[string]any)["relatedSpdxElement"] = "SPDXRef-Package-missing"
		}},
		{"zero staging root", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			doc["packages"].([]any)[0].(map[string]any)["name"] = "wrong-root"
		}},
		{"multiple staging roots", func(doc map[string]any, _ *sbomCatalog, options *SBOMOptions) {
			doc["packages"].([]any)[1].(map[string]any)["name"] = options.StagingRoot
		}},
		{"missing target file", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) { doc["files"] = doc["files"].([]any)[1:] }},
		{"duplicate target file", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			files := doc["files"].([]any)
			duplicate := cloneJSONMap(t, files[0].(map[string]any))
			duplicate["SPDXID"] = "SPDXRef-File-duplicate"
			doc["files"] = append(files, duplicate)
		}},
		{"extra target file", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			files := doc["files"].([]any)
			doc["files"] = append(files, map[string]any{"SPDXID": "SPDXRef-File-extra", "fileName": "extra/file", "fileTypes": []any{"BINARY"}, "checksums": rawFileChecksums(strings.Repeat("f", 40), strings.Repeat("f", 64))})
		}},
		{"raw hash mismatch", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			checksums := doc["files"].([]any)[0].(map[string]any)["checksums"].([]any)
			rawChecksum(checksums, "SHA256")["checksumValue"] = strings.Repeat("f", 64)
		}},
		{"missing per target dependency", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			removeEvidenceRelationship(doc, "SPDXRef-Package-dep-0")
		}},
		{"missing per target stdlib", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			removeEvidenceRelationship(doc, "SPDXRef-Package-stdlib-0")
		}},
		{"package not connected to evidence", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			removeEvidenceRelationship(doc, "SPDXRef-Package-main-0")
		}},
		{"package not connected to root", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			removeContainmentRelationship(doc, "SPDXRef-Package-main-0")
		}},
		{"unexpected evidence package", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			addUnexpectedEvidencePackage(doc, "SPDXRef-File-target-0")
		}},
		{"package tied to multiple files", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			doc["relationships"] = append(doc["relationships"].([]any), map[string]any{"spdxElementId": "SPDXRef-Package-main-0", "relationshipType": "OTHER", "relatedSpdxElement": "SPDXRef-File-target-1", "comment": evidenceComment})
		}},
		{"cross target stdlib edge", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findRelationship(doc, "SPDXRef-Package-stdlib-0", "DEPENDENCY_OF")["relatedSpdxElement"] = "SPDXRef-Package-main-1"
		}},
		{"malformed stdlib edge", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findRelationship(doc, "SPDXRef-Package-stdlib-0", "DEPENDENCY_OF")["relationshipType"] = "DEPENDS_ON"
		}},
		{"non string relationship comment", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findRelationship(doc, "SPDXRef-Package-stdlib-0", "DEPENDENCY_OF")["comment"] = true
		}},
		{"stdlib edge to same target classifier", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findRelationship(doc, "SPDXRef-Package-stdlib-4", "DEPENDENCY_OF")["relatedSpdxElement"] = "SPDXRef-Package-binary-ai-cli-gateway-windows"
		}},
		{"duplicate inverse containment", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			doc["relationships"] = append(doc["relationships"].([]any), map[string]any{"spdxElementId": "SPDXRef-Package-main-0", "relationshipType": "CONTAINED_BY", "relatedSpdxElement": "SPDXRef-Package-root"})
		}},
		{"non Windows classifier", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findRelationship(doc, "SPDXRef-Package-binary-ai-cli-gateway-windows", "OTHER")["relatedSpdxElement"] = "SPDXRef-File-target-0"
		}},
		{"duplicate classifier", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			addClassifier(doc, "SPDXRef-Package-binary-ai-cli-gateway-second", "SPDXRef-File-target-4")
		}},
		{"malformed classifier", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findPackage(doc, "SPDXRef-Package-binary-ai-cli-gateway-windows")["versionInfo"] = "0.1.0"
		}},
		{"classifier with empty purl reference", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findPackage(doc, "SPDXRef-Package-binary-ai-cli-gateway-windows")["externalRefs"] = []any{map[string]any{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": ""}}
		}},
		{"classifier with malformed external reference", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findPackage(doc, "SPDXRef-Package-binary-ai-cli-gateway-windows")["externalRefs"] = []any{map[string]any{"referenceCategory": true, "referenceType": "cpe23Type", "referenceLocator": "cpe:2.3:a:example"}}
		}},
		{"wrong dependency version", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findPackage(doc, "SPDXRef-Package-dep-0")["versionInfo"] = "v9.9.9"
		}},
		{"wrong Go version", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			findPackage(doc, "SPDXRef-Package-stdlib-0")["versionInfo"] = "go9.9.9"
		}},
		{"purl prefix collision", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			setPackagePURL(findPackage(doc, "SPDXRef-Package-dep-0"), "pkg:golang/golang.org/x/text-extra@v0.14.0")
		}},
		{"encoded purl collision", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			setPackagePURL(findPackage(doc, "SPDXRef-Package-dep-0"), "pkg:golang/golang.org%2Fx/text@v0.14.0")
		}},
		{"purl qualifier", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			setPackagePURL(findPackage(doc, "SPDXRef-Package-dep-0"), "pkg:golang/golang.org/x/text@v0.14.0?x=y")
		}},
		{"purl subpath", func(doc map[string]any, _ *sbomCatalog, _ *SBOMOptions) {
			setPackagePURL(findPackage(doc, "SPDXRef-Package-dep-0"), "pkg:golang/golang.org/x/text@v0.14.0#sub")
		}},
		{"omitted catalog target", func(_ map[string]any, catalog *sbomCatalog, _ *SBOMOptions) { catalog.Targets = catalog.Targets[1:] }},
		{"wrong catalog target hash", func(_ map[string]any, catalog *sbomCatalog, _ *SBOMOptions) {
			catalog.Targets[0].SHA256 = strings.Repeat("e", 64)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, catalog := testSBOMCatalog()
			document := rawSPDXForCatalog(catalog, options.StagingRoot)
			test.mutate(document, &catalog, &options)
			if _, err := finalizeSPDX(marshalRawSPDX(t, document), catalog, options, "0.1.0"); err == nil {
				t.Fatal("finalizeSPDX() error = nil")
			}
		})
	}
}

func TestFinalizeSPDXRejectsSensitiveFinalStrings(t *testing.T) {
	baseOptions, _ := testSBOMCatalog()
	unixHome := string(filepath.Separator) + filepath.Join("home", "private", "module")
	macHome := string(filepath.Separator) + filepath.Join("Users", "private", "module")
	windowsHome := strings.Join([]string{"C:", "Users", "private", "module"}, `\`)
	for _, sensitive := range []string{unixHome, macHome, windowsHome, baseOptions.RepositoryRoot + "/module", baseOptions.StagingRoot + "/module", baseOptions.OutputRoot + "/module", "control\nmodule"} {
		t.Run(strings.ReplaceAll(sensitive, string(filepath.Separator), "_"), func(t *testing.T) {
			options, catalog := testSBOMCatalog()
			catalog.Targets[0].Dependencies = append(catalog.Targets[0].Dependencies, sbomComponent{Name: sensitive, Version: "v1.0.0"})
			document := rawSPDXForCatalog(catalog, options.StagingRoot)
			if _, err := finalizeSPDX(marshalRawSPDX(t, document), catalog, options, "0.1.0"); err == nil {
				t.Fatal("finalizeSPDX(sensitive component) error = nil")
			}
		})
	}
}

func TestWriteSBOMValidatesBuildInfoAndWritesNoClobberAsset(t *testing.T) {
	fixture := newReleaseFixture(t)
	mustMkdir(t, fixture.outputRoot)
	writeExpectedArchives(t, fixture.outputRoot, "0.1.0")
	options := SBOMOptions{RepositoryRoot: fixture.repositoryRoot, StagingRoot: fixture.stagingRoot, OutputRoot: fixture.outputRoot, RawPath: filepath.Join(filepath.Dir(fixture.repositoryRoot), "raw.spdx.json"), Tag: "v0.1.0", SourceTime: fixture.options.SourceTime}
	catalog := catalogForStagedFixture(t, fixture)
	mustWriteBytes(t, options.RawPath, marshalRawSPDX(t, rawSPDXForCatalog(catalog, options.StagingRoot)))
	withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) { return testBuildInfo(), nil })

	asset, err := WriteSBOM(options)
	if err != nil {
		t.Fatalf("WriteSBOM() error = %v", err)
	}
	if asset.Name != "ai-cli-gateway_0.1.0_sbom.spdx.json" || asset.Path != filepath.Join(fixture.outputRoot, asset.Name) {
		t.Fatalf("asset = %#v", asset)
	}
	if mode := mustStat(t, asset.Path).Mode().Perm(); mode != 0o644 {
		t.Fatalf("SBOM mode = %04o, want 0644", mode)
	}
	if _, err := WriteSBOM(options); err == nil || ErrorCategory(err) != string(categoryUnsafePath) {
		t.Fatalf("second WriteSBOM() error = %v, want unsafe_path", err)
	}
}

func TestWriteSBOMOperationalFailuresAreClosedAndClean(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *SBOMOptions)
	}{
		{"raw read", func(t *testing.T, _ *SBOMOptions) {
			original := sbomOpenRaw
			sbomOpenRaw = func(string) (*os.File, error) { return nil, errors.New("private raw path") }
			t.Cleanup(func() { sbomOpenRaw = original })
		}},
		{"raw parse", func(t *testing.T, options *SBOMOptions) { mustWriteFile(t, options.RawPath, `{`) }},
		{"build info", func(t *testing.T, _ *SBOMOptions) {
			withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) { return nil, errors.New("private build path") })
		}},
		{"replacement build info", func(t *testing.T, _ *SBOMOptions) {
			withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) {
				info := testBuildInfo()
				info.Deps[0].Replace = &debug.Module{Path: "private/replacement", Version: "v1.0.0"}
				return info, nil
			})
		}},
		{"output write", func(t *testing.T, _ *SBOMOptions) {
			original := sbomWriteOutput
			sbomWriteOutput = func(io.Writer, []byte) (int, error) { return 0, errors.New("private write path") }
			t.Cleanup(func() { sbomWriteOutput = original })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			mustMkdir(t, fixture.outputRoot)
			writeExpectedArchives(t, fixture.outputRoot, "0.1.0")
			options := SBOMOptions{RepositoryRoot: fixture.repositoryRoot, StagingRoot: fixture.stagingRoot, OutputRoot: fixture.outputRoot, RawPath: filepath.Join(filepath.Dir(fixture.repositoryRoot), "raw.spdx.json"), Tag: "v0.1.0", SourceTime: fixture.options.SourceTime}
			catalog := catalogForStagedFixture(t, fixture)
			mustWriteBytes(t, options.RawPath, marshalRawSPDX(t, rawSPDXForCatalog(catalog, options.StagingRoot)))
			withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) { return testBuildInfo(), nil })
			test.prepare(t, &options)
			asset, err := WriteSBOM(options)
			if asset != (Asset{}) {
				t.Fatalf("asset = %#v, want zero", asset)
			}
			assertCategory(t, err, categorySBOMFailure)
			if _, statErr := os.Lstat(filepath.Join(fixture.outputRoot, "ai-cli-gateway_0.1.0_sbom.spdx.json")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial output stat = %v", statErr)
			}
		})
	}
}

func TestWriteSBOMPreservesAttackerReplacement(t *testing.T) {
	fixture := newReleaseFixture(t)
	mustMkdir(t, fixture.outputRoot)
	writeExpectedArchives(t, fixture.outputRoot, "0.1.0")
	options := SBOMOptions{RepositoryRoot: fixture.repositoryRoot, StagingRoot: fixture.stagingRoot, OutputRoot: fixture.outputRoot, RawPath: filepath.Join(filepath.Dir(fixture.repositoryRoot), "raw.spdx.json"), Tag: "v0.1.0", SourceTime: fixture.options.SourceTime}
	catalog := catalogForStagedFixture(t, fixture)
	mustWriteBytes(t, options.RawPath, marshalRawSPDX(t, rawSPDXForCatalog(catalog, options.StagingRoot)))
	withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) { return testBuildInfo(), nil })
	finalPath := filepath.Join(fixture.outputRoot, "ai-cli-gateway_0.1.0_sbom.spdx.json")
	const attacker = "attacker-owned\n"
	original := sbomWriteOutput
	sbomWriteOutput = func(writer io.Writer, data []byte) (int, error) {
		n, err := writer.Write(data)
		if err != nil {
			return n, err
		}
		if err := os.Remove(finalPath); err != nil {
			return n, err
		}
		if err := os.WriteFile(finalPath, []byte(attacker), 0o600); err != nil {
			return n, err
		}
		return n, nil
	}
	t.Cleanup(func() { sbomWriteOutput = original })

	asset, err := WriteSBOM(options)
	if asset != (Asset{}) {
		t.Fatalf("asset = %#v, want zero", asset)
	}
	assertCategory(t, err, categorySBOMFailure)
	if got := string(mustReadFile(t, finalPath)); got != attacker {
		t.Fatalf("replacement = %q, want preserved", got)
	}
}

func TestWriteSBOMRejectsUnsafeOutputAuthorityBeforeRawOrBuildInfo(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*testing.T, *releaseFixture)
	}{
		{"mode", func(t *testing.T, fixture *releaseFixture) {
			mustMkdir(t, fixture.outputRoot)
			//nolint:gosec // The intentionally permissive directory mode is the condition under test.
			if err := os.Chmod(fixture.outputRoot, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign owner", func(t *testing.T, fixture *releaseFixture) {
			mustMkdir(t, fixture.outputRoot)
			original := releasepackEffectiveUID
			releasepackEffectiveUID = func() int { return os.Geteuid() + 1 }
			t.Cleanup(func() { releasepackEffectiveUID = original })
		}},
		{"writable ancestor", func(t *testing.T, fixture *releaseFixture) {
			ancestor := filepath.Join(filepath.Dir(fixture.outputRoot), "unsafe")
			mustMkdir(t, filepath.Join(ancestor, "output"))
			//nolint:gosec // The intentionally unsafe directory mode is required by this rejection test.
			if err := os.Chmod(ancestor, 0o777); err != nil {
				t.Fatal(err)
			}
			//nolint:gosec // Cleanup restores the owner-only mode on this test directory.
			t.Cleanup(func() { _ = os.Chmod(ancestor, 0o700) })
			fixture.outputRoot = filepath.Join(ancestor, "output")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			test.alter(t, &fixture)
			calls := 0
			withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) { calls++; return nil, errors.New("must not run") })
			_, err := WriteSBOM(SBOMOptions{RepositoryRoot: fixture.repositoryRoot, StagingRoot: fixture.stagingRoot, OutputRoot: fixture.outputRoot, RawPath: filepath.Join(filepath.Dir(fixture.repositoryRoot), "absent-raw"), Tag: "v0.1.0", SourceTime: fixture.options.SourceTime})
			assertCategory(t, err, categoryUnsafePath)
			if calls != 0 {
				t.Fatalf("build info calls = %d, want zero", calls)
			}
		})
	}
}

func TestWriteSBOMRejectsRelativeSymlinkedAndOverlappingRawPaths(t *testing.T) {
	tests := []struct {
		name    string
		rawPath func(*testing.T, releaseFixture) string
	}{
		{"relative", func(_ *testing.T, _ releaseFixture) string { return "relative.spdx.json" }},
		{"symlink", func(t *testing.T, fixture releaseFixture) string {
			target := filepath.Join(filepath.Dir(fixture.repositoryRoot), "raw-target.spdx.json")
			mustWriteFile(t, target, "{}")
			link := filepath.Join(filepath.Dir(fixture.repositoryRoot), "raw-link.spdx.json")
			mustSymlink(t, target, link)
			return link
		}},
		{"overlap", func(t *testing.T, fixture releaseFixture) string {
			path := filepath.Join(fixture.repositoryRoot, "raw.spdx.json")
			mustWriteFile(t, path, "{}")
			return path
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			mustMkdir(t, fixture.outputRoot)
			writeExpectedArchives(t, fixture.outputRoot, "0.1.0")
			calls := 0
			withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) { calls++; return testBuildInfo(), nil })
			_, err := WriteSBOM(SBOMOptions{RepositoryRoot: fixture.repositoryRoot, StagingRoot: fixture.stagingRoot, OutputRoot: fixture.outputRoot, RawPath: test.rawPath(t, fixture), Tag: "v0.1.0", SourceTime: fixture.options.SourceTime})
			assertCategory(t, err, categoryUnsafePath)
			if calls != 0 {
				t.Fatalf("build info calls = %d, want zero", calls)
			}
		})
	}
}

func TestWriteSBOMRejectsRawInputOver16MiB(t *testing.T) {
	fixture := newReleaseFixture(t)
	mustMkdir(t, fixture.outputRoot)
	writeExpectedArchives(t, fixture.outputRoot, "0.1.0")
	rawPath := filepath.Join(filepath.Dir(fixture.repositoryRoot), "oversized.spdx.json")
	mustWriteBytes(t, rawPath, make([]byte, maxRawSPDXBytes+1))
	withBuildInfoReader(t, func(string) (*debug.BuildInfo, error) { return testBuildInfo(), nil })
	asset, err := WriteSBOM(SBOMOptions{RepositoryRoot: fixture.repositoryRoot, StagingRoot: fixture.stagingRoot, OutputRoot: fixture.outputRoot, RawPath: rawPath, Tag: "v0.1.0", SourceTime: fixture.options.SourceTime})
	if asset != (Asset{}) {
		t.Fatalf("asset = %#v, want zero", asset)
	}
	assertCategory(t, err, categorySBOMFailure)
}

func testSBOMCatalog() (SBOMOptions, sbomCatalog) {
	options := SBOMOptions{RepositoryRoot: "fixture-repository-marker", StagingRoot: "fixture-root-marker", OutputRoot: "fixture-output-marker", Tag: "v0.1.0", SourceTime: time.Date(2026, 8, 4, 6, 29, 53, 0, time.UTC)}
	targets := make([]sbomTargetCatalog, 0, len(releaseTargets))
	for i, releaseTarget := range releaseTargets {
		targets = append(targets, sbomTargetCatalog{Target: releaseTarget, RelativePath: filepath.ToSlash(filepath.Join(releaseTarget.Directory, releaseTarget.Executable)), SHA256: strings.Repeat(string(rune('a'+i)), 64), GoVersion: "go1.26.5", Main: sbomComponent{Name: releaseModulePath, Version: "(devel)"}, Dependencies: []sbomComponent{{Name: "github.com/pelletier/go-toml/v2", Version: "v2.4.3"}, {Name: "github.com/santhosh-tekuri/jsonschema/v6", Version: "v6.0.2"}, {Name: "golang.org/x/sys", Version: "v0.47.0"}, {Name: "golang.org/x/text", Version: "v0.14.0"}}})
	}
	return options, sbomCatalog{Targets: targets}
}

func rawSPDXForCatalog(catalog sbomCatalog, stagingRoot string) map[string]any {
	packages := []any{map[string]any{"name": stagingRoot, "SPDXID": "SPDXRef-Package-root", "versionInfo": "UNKNOWN", "downloadLocation": "NOASSERTION", "filesAnalyzed": false}}
	files := make([]any, 0, len(catalog.Targets))
	relationships := []any{map[string]any{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": "SPDXRef-Package-root"}}
	for i, targetCatalog := range catalog.Targets {
		fileID := "SPDXRef-File-target-" + string(rune('0'+i))
		sha1 := strings.Repeat(string(rune('1'+i)), 40)
		files = append(files, map[string]any{"fileName": targetCatalog.RelativePath, "SPDXID": fileID, "fileTypes": []any{"BINARY"}, "checksums": rawFileChecksums(sha1, targetCatalog.SHA256), "licenseConcluded": "NOASSERTION", "copyrightText": "NOASSERTION"})
		components := append([]sbomComponent{targetCatalog.Main}, targetCatalog.Dependencies...)
		components = append(components, sbomComponent{Name: "stdlib", Version: targetCatalog.GoVersion})
		for componentIndex, component := range components {
			kind := "dep"
			if componentIndex > 1 && componentIndex < len(components)-1 {
				kind += string(rune('0' + componentIndex))
			}
			version := component.Version
			purl := "pkg:golang/" + component.Name + "@" + component.Version
			if componentIndex == 0 {
				kind = "main"
				version = "UNKNOWN"
				purl = "pkg:golang/" + component.Name
			}
			if component.Name == "stdlib" {
				kind = "stdlib"
				purl = "pkg:golang/stdlib@" + strings.TrimPrefix(component.Version, "go")
			}
			packageID := "SPDXRef-Package-" + kind + "-" + string(rune('0'+i))
			packages = append(packages, map[string]any{"name": component.Name, "SPDXID": packageID, "versionInfo": version, "downloadLocation": "NOASSERTION", "filesAnalyzed": false, "externalRefs": []any{map[string]any{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": purl}}})
			relationships = append(relationships, map[string]any{"spdxElementId": "SPDXRef-Package-root", "relationshipType": "CONTAINS", "relatedSpdxElement": packageID})
			relationships = append(relationships, map[string]any{"spdxElementId": packageID, "relationshipType": "OTHER", "relatedSpdxElement": fileID, "comment": "evident-by: indicates the package's existence is evident by the given file"})
			if kind == "stdlib" {
				relationships = append(relationships, map[string]any{"spdxElementId": packageID, "relationshipType": "DEPENDENCY_OF", "relatedSpdxElement": "SPDXRef-Package-main-" + string(rune('0'+i))})
			}
		}
	}
	windowFile := "SPDXRef-File-target-4"
	classifier := "SPDXRef-Package-binary-ai-cli-gateway-windows"
	packages = append(packages, map[string]any{"name": "ai-cli-gateway", "SPDXID": classifier, "versionInfo": "UNKNOWN", "downloadLocation": "NOASSERTION", "filesAnalyzed": false})
	relationships = append(relationships, map[string]any{"spdxElementId": classifier, "relationshipType": "OTHER", "relatedSpdxElement": windowFile, "comment": "evident-by: indicates the package's existence is evident by the given file"}, map[string]any{"spdxElementId": "SPDXRef-Package-root", "relationshipType": "CONTAINS", "relatedSpdxElement": classifier})
	return map[string]any{"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT", "name": "fixture-root-marker", "documentNamespace": "https://example.invalid/fixture-root-marker", "creationInfo": map[string]any{"creators": []any{"Organization: ignored", "Tool: syft-1.50.0"}, "created": "2026-08-04T06:29:53Z"}, "packages": packages, "files": files, "relationships": relationships}
}

func rawFileChecksums(sha1, sha256 string) []any {
	return []any{
		map[string]any{"algorithm": "SHA1", "checksumValue": sha1},
		map[string]any{"algorithm": "SHA256", "checksumValue": sha256},
	}
}

func rawChecksum(values []any, algorithm string) map[string]any {
	for _, value := range values {
		checksum, ok := value.(map[string]any)
		if ok && checksum["algorithm"] == algorithm {
			return checksum
		}
	}
	return nil
}

func removeRawChecksum(values []any, algorithm string) []any {
	kept := make([]any, 0, len(values))
	for _, value := range values {
		checksum, ok := value.(map[string]any)
		if ok && checksum["algorithm"] == algorithm {
			continue
		}
		kept = append(kept, value)
	}
	return kept
}

func cloneRawChecksum(value map[string]any) map[string]any {
	return map[string]any{"algorithm": value["algorithm"], "checksumValue": value["checksumValue"]}
}

func marshalRawSPDX(t *testing.T, document map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func cloneJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data := marshalRawSPDX(t, value)
	var clone map[string]any
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
func mustJSONArray(t *testing.T, object map[string]any, key string) []any {
	t.Helper()
	value, ok := object[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v", key, object[key])
	}
	return value
}
func findPackage(document map[string]any, id string) map[string]any {
	for _, v := range document["packages"].([]any) {
		p := v.(map[string]any)
		if p["SPDXID"] == id {
			return p
		}
	}
	panic("package not found")
}
func findRelationship(document map[string]any, id, kind string) map[string]any {
	for _, v := range document["relationships"].([]any) {
		r := v.(map[string]any)
		if r["spdxElementId"] == id && r["relationshipType"] == kind {
			return r
		}
	}
	panic("relationship not found")
}
func removeEvidenceRelationship(document map[string]any, id string) {
	rels := document["relationships"].([]any)
	kept := rels[:0]
	for _, v := range rels {
		r := v.(map[string]any)
		if r["spdxElementId"] == id && r["relationshipType"] == "OTHER" {
			continue
		}
		kept = append(kept, v)
	}
	document["relationships"] = kept
}
func removeContainmentRelationship(document map[string]any, id string) {
	rels := document["relationships"].([]any)
	kept := rels[:0]
	for _, v := range rels {
		r := v.(map[string]any)
		if r["spdxElementId"] == "SPDXRef-Package-root" && r["relationshipType"] == "CONTAINS" && r["relatedSpdxElement"] == id {
			continue
		}
		kept = append(kept, v)
	}
	document["relationships"] = kept
}
func setPackagePURL(pkg map[string]any, purl string) {
	pkg["externalRefs"].([]any)[0].(map[string]any)["referenceLocator"] = purl
}
func addUnexpectedEvidencePackage(document map[string]any, fileID string) {
	id := "SPDXRef-Package-unexpected"
	document["packages"] = append(document["packages"].([]any), map[string]any{"name": "unexpected", "SPDXID": id, "versionInfo": "UNKNOWN"})
	document["relationships"] = append(document["relationships"].([]any), map[string]any{"spdxElementId": id, "relationshipType": "OTHER", "relatedSpdxElement": fileID, "comment": "evident-by: indicates the package's existence is evident by the given file"})
}
func addClassifier(document map[string]any, id, fileID string) {
	document["packages"] = append(document["packages"].([]any), map[string]any{"name": "ai-cli-gateway", "SPDXID": id, "versionInfo": "UNKNOWN", "downloadLocation": "NOASSERTION", "filesAnalyzed": false})
	document["relationships"] = append(document["relationships"].([]any), map[string]any{"spdxElementId": id, "relationshipType": "OTHER", "relatedSpdxElement": fileID, "comment": "evident-by: indicates the package's existence is evident by the given file"}, map[string]any{"spdxElementId": "SPDXRef-Package-root", "relationshipType": "CONTAINS", "relatedSpdxElement": id})
}
func testBuildInfo() *debug.BuildInfo {
	return &debug.BuildInfo{GoVersion: "go1.26.5", Main: debug.Module{Path: releaseModulePath, Version: "(devel)"}, Deps: []*debug.Module{{Path: "github.com/pelletier/go-toml/v2", Version: "v2.4.3"}, {Path: "github.com/santhosh-tekuri/jsonschema/v6", Version: "v6.0.2"}, {Path: "golang.org/x/sys", Version: "v0.47.0"}, {Path: "golang.org/x/text", Version: "v0.14.0"}}}
}
func withBuildInfoReader(t *testing.T, reader func(string) (*debug.BuildInfo, error)) {
	t.Helper()
	original := sbomReadBuildInfo
	sbomReadBuildInfo = reader
	t.Cleanup(func() { sbomReadBuildInfo = original })
}
func catalogForStagedFixture(t *testing.T, fixture releaseFixture) sbomCatalog {
	t.Helper()
	targets := make([]sbomTargetCatalog, 0, len(releaseTargets))
	for _, releaseTarget := range releaseTargets {
		path := filepath.Join(fixture.stagingRoot, releaseTarget.Directory, releaseTarget.Executable)
		contents := mustReadFile(t, path)
		sum := sha256.Sum256(contents)
		targets = append(targets, sbomTargetCatalog{Target: releaseTarget, RelativePath: filepath.ToSlash(filepath.Join(releaseTarget.Directory, releaseTarget.Executable)), SHA256: hex.EncodeToString(sum[:]), GoVersion: "go1.26.5", Main: sbomComponent{Name: releaseModulePath, Version: "(devel)"}, Dependencies: []sbomComponent{{Name: "github.com/pelletier/go-toml/v2", Version: "v2.4.3"}, {Name: "github.com/santhosh-tekuri/jsonschema/v6", Version: "v6.0.2"}, {Name: "golang.org/x/sys", Version: "v0.47.0"}, {Name: "golang.org/x/text", Version: "v0.14.0"}}})
	}
	return sbomCatalog{Targets: targets}
}
func writeExpectedArchives(t *testing.T, outputRoot, version string) {
	t.Helper()
	for _, name := range expectedArchiveNames(version) {
		mustWriteFile(t, filepath.Join(outputRoot, name), "archive:"+name+"\n")
	}
}
func mustWriteBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
