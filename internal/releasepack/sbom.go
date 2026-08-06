package releasepack

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/safejson"
)

const (
	maxRawSPDXBytes  = 16 << 20
	syftCreator      = "Tool: syft-1.50.0"
	evidenceComment  = "evident-by: indicates the package's existence is evident by the given file"
	releaseGoVersion = "go1.26.5"
)

// SBOMOptions identifies the validated inputs used to publish the release SBOM.
type SBOMOptions struct {
	RepositoryRoot string
	StagingRoot    string
	OutputRoot     string
	RawPath        string
	Tag            string
	SourceTime     time.Time
}

type sbomComponent struct {
	Name    string
	Version string
}

type sbomTargetCatalog struct {
	Target       target
	RelativePath string
	SHA256       string
	GoVersion    string
	Main         sbomComponent
	Dependencies []sbomComponent
}

type sbomCatalog struct {
	Targets []sbomTargetCatalog
}

var (
	sbomReadBuildInfo func(string) (*debug.BuildInfo, error) = buildinfo.ReadFile
	sbomOpenRaw                                              = os.Open
	sbomWriteOutput                                          = func(writer io.Writer, data []byte) (int, error) { return writer.Write(data) }
)

// WriteSBOM publishes a deterministic SPDX JSON document for the release binaries.
func WriteSBOM(options SBOMOptions) (asset Asset, resultErr error) {
	if err := validateRootSet(options.RepositoryRoot, options.StagingRoot, options.OutputRoot, false); err != nil {
		return Asset{}, err
	}
	version, sourceTime, err := validateTagAndSourceTime(options.Tag, options.SourceTime)
	if err != nil {
		return Asset{}, err
	}
	_, binaries, err := validateRepositoryAndStaging(options.RepositoryRoot, options.StagingRoot)
	if err != nil {
		return Asset{}, err
	}
	if _, err := validateExactRegularFiles(options.OutputRoot, expectedArchiveNames(version)); err != nil {
		return Asset{}, err
	}
	if err := validateRawSBOMPath(options); err != nil {
		return Asset{}, err
	}

	catalog, err := buildSBOMCatalog(binaries)
	if err != nil {
		return Asset{}, newSBOMFailure()
	}
	raw, err := readRawSPDX(options.RawPath)
	if err != nil {
		return Asset{}, newSBOMFailure()
	}
	options.SourceTime = sourceTime
	finalized, err := finalizeSPDX(raw, catalog, options, version)
	if err != nil {
		return Asset{}, newSBOMFailure()
	}

	asset = Asset{
		Name: "ai-cli-gateway_" + version + "_sbom.spdx.json",
		Path: filepath.Join(options.OutputRoot, "ai-cli-gateway_"+version+"_sbom.spdx.json"),
	}
	//nolint:gosec // The SBOM is a public release asset and must be world-readable.
	file, err := os.OpenFile(asset.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Asset{}, newSBOMFailure()
	}
	createdInfo, statErr := file.Stat()
	if statErr != nil || !createdInfo.Mode().IsRegular() {
		_ = file.Close()
		cleanupCreatedFile(asset.Path, createdInfo)
		return Asset{}, newSBOMFailure()
	}
	succeeded := false
	defer func() {
		if !succeeded {
			asset = Asset{}
			cleanupCreatedFile(filepath.Join(options.OutputRoot, "ai-cli-gateway_"+version+"_sbom.spdx.json"), createdInfo)
			resultErr = newSBOMFailure()
		}
	}()
	if n, writeErr := sbomWriteOutput(file, finalized); writeErr != nil || n != len(finalized) {
		_ = file.Close()
		return Asset{}, newSBOMFailure()
	}
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return Asset{}, newSBOMFailure()
	}
	if err := file.Close(); err != nil {
		return Asset{}, newSBOMFailure()
	}
	current, err := os.Lstat(asset.Path)
	if err != nil || !os.SameFile(createdInfo, current) {
		return Asset{}, newSBOMFailure()
	}
	succeeded = true
	return asset, nil
}

func validateRawSBOMPath(options SBOMOptions) error {
	if err := validateAbsoluteCleanPath(options.RawPath); err != nil {
		return err
	}
	if err := validateExistingComponents(options.RawPath); err != nil {
		return err
	}
	for _, root := range []string{options.RepositoryRoot, options.StagingRoot, options.OutputRoot} {
		if rootsOverlap(root, options.RawPath) {
			return newCategorizedError(categoryUnsafePath)
		}
	}
	info, err := os.Lstat(options.RawPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newCategorizedError(categoryMissingInput)
		}
		return newCategorizedError(categoryUnsafePath)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return newCategorizedError(categoryUnsafePath)
	}
	return nil
}

func buildSBOMCatalog(binaries []stagedBinary) (sbomCatalog, error) {
	if len(binaries) != len(releaseTargets) {
		return sbomCatalog{}, errors.New("invalid binary set")
	}
	catalog := sbomCatalog{Targets: make([]sbomTargetCatalog, 0, len(binaries))}
	for i, binary := range binaries {
		if binary.Target != releaseTargets[i] {
			return sbomCatalog{}, errors.New("invalid binary order")
		}
		before, err := os.Lstat(binary.Path)
		if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
			return sbomCatalog{}, errors.New("binary changed")
		}
		file, err := os.Open(binary.Path)
		if err != nil {
			return sbomCatalog{}, errors.New("binary read failed")
		}
		descriptorInfo, statErr := file.Stat()
		hasher := sha256.New()
		_, copyErr := io.Copy(hasher, file)
		closeErr := file.Close()
		after, pathErr := os.Lstat(binary.Path)
		if statErr != nil || copyErr != nil || closeErr != nil || pathErr != nil ||
			!descriptorInfo.Mode().IsRegular() || !os.SameFile(before, descriptorInfo) || !os.SameFile(descriptorInfo, after) || after.Mode()&os.ModeSymlink != 0 {
			return sbomCatalog{}, errors.New("binary read failed")
		}
		info, err := sbomReadBuildInfo(binary.Path)
		if err != nil || info == nil || info.GoVersion != releaseGoVersion || info.Main.Path != releaseModulePath || info.Main.Version != "(devel)" || info.Main.Replace != nil {
			return sbomCatalog{}, errors.New("invalid build info")
		}
		finalInfo, pathErr := os.Lstat(binary.Path)
		if pathErr != nil || !os.SameFile(before, finalInfo) || finalInfo.Mode()&os.ModeSymlink != 0 {
			return sbomCatalog{}, errors.New("binary changed")
		}
		dependencies := make([]sbomComponent, 0, len(info.Deps))
		seen := make(map[string]struct{})
		for _, dependency := range info.Deps {
			if dependency == nil || dependency.Path == "" || dependency.Version == "" || dependency.Replace != nil {
				return sbomCatalog{}, errors.New("invalid build dependency")
			}
			key := dependency.Path + "\x00" + dependency.Version
			if _, exists := seen[key]; exists {
				return sbomCatalog{}, errors.New("duplicate build dependency")
			}
			seen[key] = struct{}{}
			dependencies = append(dependencies, sbomComponent{Name: dependency.Path, Version: dependency.Version})
		}
		slices.SortFunc(dependencies, func(first, second sbomComponent) int {
			if compared := strings.Compare(first.Name, second.Name); compared != 0 {
				return compared
			}
			return strings.Compare(first.Version, second.Version)
		})
		catalog.Targets = append(catalog.Targets, sbomTargetCatalog{
			Target:       binary.Target,
			RelativePath: filepath.ToSlash(filepath.Join(binary.Target.Directory, binary.Target.Executable)),
			SHA256:       hex.EncodeToString(hasher.Sum(nil)),
			GoVersion:    info.GoVersion,
			Main:         sbomComponent{Name: info.Main.Path, Version: info.Main.Version},
			Dependencies: dependencies,
		})
	}
	return catalog, nil
}

func readRawSPDX(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("raw read failed")
	}
	file, err := sbomOpenRaw(path)
	if err != nil || file == nil {
		return nil, errors.New("raw read failed")
	}
	descriptorInfo, statErr := file.Stat()
	data, err := io.ReadAll(io.LimitReader(file, maxRawSPDXBytes+1))
	closeErr := file.Close()
	after, pathErr := os.Lstat(path)
	if statErr != nil || err != nil || closeErr != nil || pathErr != nil || len(data) > maxRawSPDXBytes ||
		!descriptorInfo.Mode().IsRegular() || !os.SameFile(before, descriptorInfo) || !os.SameFile(descriptorInfo, after) || after.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("raw read failed")
	}
	return data, nil
}

type rawPackage struct {
	id, name, version, purl string
	purlCount               int
}
type rawFile struct{ id, name, hash string }
type rawRelationship struct{ source, kind, target, comment string }

func finalizeSPDX(raw []byte, catalog sbomCatalog, options SBOMOptions, version string) ([]byte, error) {
	value, err := safejson.Parse(raw, safejson.Limits{MaxDepth: 64, MaxNumberBytes: 128})
	if err != nil {
		return nil, errors.New("invalid SPDX")
	}
	document, ok := value.(map[string]any)
	if !ok || stringField(document, "spdxVersion") != "SPDX-2.3" || stringField(document, "dataLicense") != "CC0-1.0" || stringField(document, "SPDXID") != "SPDXRef-DOCUMENT" {
		return nil, errors.New("invalid SPDX")
	}
	if !hasExactCreator(document) || len(catalog.Targets) != len(releaseTargets) {
		return nil, errors.New("invalid SPDX")
	}
	for i, targetCatalog := range catalog.Targets {
		want := releaseTargets[i]
		if targetCatalog.Target != want || targetCatalog.RelativePath != filepath.ToSlash(filepath.Join(want.Directory, want.Executable)) || !isLowerSHA256(targetCatalog.SHA256) || targetCatalog.GoVersion != releaseGoVersion || targetCatalog.Main != (sbomComponent{Name: releaseModulePath, Version: "(devel)"}) {
			return nil, errors.New("invalid catalog")
		}
	}

	packageValues, ok := document["packages"].([]any)
	if !ok || len(packageValues) == 0 {
		return nil, errors.New("invalid SPDX")
	}
	fileValues, ok := document["files"].([]any)
	if !ok || len(fileValues) == 0 {
		return nil, errors.New("invalid SPDX")
	}
	relationshipValues, ok := document["relationships"].([]any)
	if !ok || len(relationshipValues) == 0 {
		return nil, errors.New("invalid SPDX")
	}
	ids := map[string]string{"SPDXRef-DOCUMENT": "document"}
	packages := make(map[string]rawPackage, len(packageValues))
	for _, value := range packageValues {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("invalid package")
		}
		pkg, err := parseRawPackage(object)
		if err != nil {
			return nil, err
		}
		if !addSPDXID(ids, pkg.id, "package") {
			return nil, errors.New("duplicate SPDX ID")
		}
		packages[pkg.id] = pkg
	}
	files := make(map[string]rawFile, len(fileValues))
	for _, value := range fileValues {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("invalid file")
		}
		file, err := parseRawFile(object)
		if err != nil {
			return nil, err
		}
		if !addSPDXID(ids, file.id, "file") {
			return nil, errors.New("duplicate SPDX ID")
		}
		files[file.id] = file
	}
	relationships := make([]rawRelationship, 0, len(relationshipValues))
	normalized := make(map[string]struct{})
	for _, value := range relationshipValues {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("invalid relationship")
		}
		comment, err := optionalStringField(object, "comment")
		if err != nil {
			return nil, errors.New("invalid relationship")
		}
		relation := rawRelationship{source: stringField(object, "spdxElementId"), kind: stringField(object, "relationshipType"), target: stringField(object, "relatedSpdxElement"), comment: comment}
		if ids[relation.source] == "" || ids[relation.target] == "" || relation.kind == "" {
			return nil, errors.New("dangling relationship")
		}
		key := normalizedRelationshipKey(relation)
		if _, exists := normalized[key]; exists {
			return nil, errors.New("duplicate relationship")
		}
		normalized[key] = struct{}{}
		relationships = append(relationships, relation)
	}

	rootID := ""
	rootNames := 0
	describeCount := 0
	for id, pkg := range packages {
		if pkg.name == options.StagingRoot {
			rootNames++
			rootID = id
		}
	}
	for _, relation := range relationships {
		if relation.source == "SPDXRef-DOCUMENT" && relation.kind == "DESCRIBES" {
			describeCount++
			if relation.target != rootID {
				return nil, errors.New("invalid root")
			}
		}
	}
	if rootNames != 1 || describeCount != 1 || rootID == "" {
		return nil, errors.New("invalid root")
	}

	targetByFileID := make(map[string]int)
	seenTargetFiles := make([]bool, len(catalog.Targets))
	for id, file := range files {
		index := -1
		for i, targetCatalog := range catalog.Targets {
			if file.name == targetCatalog.RelativePath {
				index = i
				break
			}
		}
		if index < 0 || seenTargetFiles[index] || file.hash != catalog.Targets[index].SHA256 {
			return nil, errors.New("invalid target file")
		}
		seenTargetFiles[index] = true
		targetByFileID[id] = index
	}
	if len(files) != len(catalog.Targets) {
		return nil, errors.New("invalid target files")
	}
	for _, seen := range seenTargetFiles {
		if !seen {
			return nil, errors.New("missing target file")
		}
	}

	evidenceTarget := make(map[string]int)
	evidenceCount := make(map[string]int)
	containmentCount := make(map[string]int)
	relationCount := make(map[string]int)
	for _, relation := range relationships {
		relationCount[relation.source]++
		relationCount[relation.target]++
		switch relation.kind {
		case "DESCRIBES":
			if relation.source != "SPDXRef-DOCUMENT" || relation.target != rootID || relation.comment != "" {
				return nil, errors.New("invalid describes")
			}
		case "CONTAINS", "CONTAINED_BY":
			parent, child := relation.source, relation.target
			if relation.kind == "CONTAINED_BY" {
				parent, child = child, parent
			}
			if parent != rootID || relation.comment != "" {
				return nil, errors.New("invalid containment")
			}
			if _, exists := packages[child]; !exists || child == rootID {
				return nil, errors.New("invalid containment")
			}
			containmentCount[child]++
		case "OTHER":
			index, exists := targetByFileID[relation.target]
			if !exists || relation.comment != evidenceComment {
				return nil, errors.New("invalid evidence")
			}
			if _, exists := packages[relation.source]; !exists || relation.source == rootID {
				return nil, errors.New("invalid evidence")
			}
			evidenceCount[relation.source]++
			evidenceTarget[relation.source] = index
		case "DEPENDENCY_OF", "DEPENDS_ON":
			// Validated after each package is assigned to exactly one target.
			if relation.comment != "" {
				return nil, errors.New("invalid dependency")
			}
		default:
			return nil, errors.New("invalid relationship type")
		}
	}
	packageTarget := make(map[string]int)
	packageIdentity := make(map[string]string)
	classifierID := ""
	classifierCount := 0
	for id, pkg := range packages {
		if id == rootID {
			continue
		}
		if evidenceCount[id] != 1 {
			return nil, errors.New("invalid evidence count")
		}
		index := evidenceTarget[id]
		targetCatalog := catalog.Targets[index]
		if isClassifierShape(pkg) {
			if targetCatalog.Target.GOOS != "windows" || containmentCount[id] != 1 || relationCount[id] != 2 {
				return nil, errors.New("invalid classifier")
			}
			classifierCount++
			classifierID = id
			packageTarget[id] = index
			continue
		}
		if containmentCount[id] != 1 {
			return nil, errors.New("invalid module containment")
		}
		identity, err := validateRawGoPackage(pkg, targetCatalog)
		if err != nil {
			return nil, err
		}
		for existingID, existingIdentity := range packageIdentity {
			if packageTarget[existingID] == index && existingIdentity == identity {
				return nil, errors.New("duplicate target package")
			}
		}
		packageIdentity[id] = identity
		packageTarget[id] = index
	}
	if classifierCount != 1 || classifierID == "" {
		return nil, errors.New("invalid classifier count")
	}

	for i, targetCatalog := range catalog.Targets {
		want := expectedTargetIdentities(targetCatalog)
		got := make(map[string]struct{})
		for id, identity := range packageIdentity {
			if packageTarget[id] == i {
				got[identity] = struct{}{}
			}
		}
		if len(got) != len(want) {
			return nil, errors.New("package set mismatch")
		}
		for identity := range want {
			if _, exists := got[identity]; !exists {
				return nil, errors.New("missing package")
			}
		}
	}
	stdlibEdges := make(map[string]int)
	for _, relation := range relationships {
		if relation.kind != "DEPENDENCY_OF" && relation.kind != "DEPENDS_ON" {
			continue
		}
		firstTarget, firstOK := packageTarget[relation.source]
		secondTarget, secondOK := packageTarget[relation.target]
		if !firstOK || !secondOK || firstTarget != secondTarget {
			return nil, errors.New("cross-target dependency")
		}
		if strings.HasPrefix(packageIdentity[relation.source], "stdlib\x00") {
			if relation.kind != "DEPENDENCY_OF" || packageIdentity[relation.target] != "" && packageIdentity[relation.target] != componentIdentity(catalog.Targets[firstTarget].Main) {
				return nil, errors.New("invalid stdlib dependency")
			}
			stdlibEdges[relation.source]++
		}
	}
	for id, identity := range packageIdentity {
		if strings.HasPrefix(identity, "stdlib\x00") && stdlibEdges[id] != 1 {
			return nil, errors.New("missing stdlib dependency")
		}
	}

	finalDocument := buildFinalSPDX(catalog, options, version)
	if err := scanFinalSPDX(finalDocument, options); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(finalDocument, "", "  ")
	if err != nil {
		return nil, errors.New("marshal failed")
	}
	return append(encoded, '\n'), nil
}

func parseRawPackage(object map[string]any) (rawPackage, error) {
	pkg := rawPackage{id: stringField(object, "SPDXID"), name: stringField(object, "name"), version: stringField(object, "versionInfo")}
	if !validSPDXID(pkg.id) || pkg.name == "" {
		return rawPackage{}, errors.New("invalid package")
	}
	if refs, exists := object["externalRefs"]; exists {
		array, ok := refs.([]any)
		if !ok {
			return rawPackage{}, errors.New("invalid external refs")
		}
		purls := 0
		for _, value := range array {
			ref, ok := value.(map[string]any)
			if !ok {
				return rawPackage{}, errors.New("invalid external ref")
			}
			category := stringField(ref, "referenceCategory")
			referenceType := stringField(ref, "referenceType")
			locator := stringField(ref, "referenceLocator")
			if category == "" || referenceType == "" || locator == "" {
				return rawPackage{}, errors.New("invalid external ref")
			}
			if category == "PACKAGE-MANAGER" && referenceType == "purl" {
				purls++
				pkg.purl = locator
			}
		}
		if purls > 1 {
			return rawPackage{}, errors.New("duplicate purl")
		}
		pkg.purlCount = purls
	}
	return pkg, nil
}

func parseRawFile(object map[string]any) (rawFile, error) {
	file := rawFile{id: stringField(object, "SPDXID"), name: stringField(object, "fileName")}
	if !validSPDXID(file.id) || file.name == "" {
		return rawFile{}, errors.New("invalid file")
	}
	types, ok := object["fileTypes"].([]any)
	if !ok {
		return rawFile{}, errors.New("invalid file types")
	}
	binary := false
	for _, v := range types {
		if v == "BINARY" {
			binary = true
		}
	}
	if !binary {
		return rawFile{}, errors.New("not binary")
	}
	checksums, ok := object["checksums"].([]any)
	if !ok || len(checksums) != 2 {
		return rawFile{}, errors.New("invalid checksum")
	}
	sha1Seen := false
	sha256 := ""
	for _, value := range checksums {
		checksum, ok := value.(map[string]any)
		if !ok {
			return rawFile{}, errors.New("invalid checksum")
		}
		digest := stringField(checksum, "checksumValue")
		switch stringField(checksum, "algorithm") {
		case "SHA1":
			if sha1Seen || !isLowerHex(digest, 40) {
				return rawFile{}, errors.New("invalid checksum")
			}
			sha1Seen = true
		case "SHA256":
			if sha256 != "" || !isLowerSHA256(digest) {
				return rawFile{}, errors.New("invalid checksum")
			}
			sha256 = digest
		default:
			return rawFile{}, errors.New("invalid checksum")
		}
	}
	if !sha1Seen || sha256 == "" {
		return rawFile{}, errors.New("invalid checksum")
	}
	// Syft's SHA1 is validated only as part of the pinned raw structure. The
	// independently computed SHA256 remains the sole trusted and projected hash.
	file.hash = sha256
	return file, nil
}

func validateRawGoPackage(pkg rawPackage, target sbomTargetCatalog) (string, error) {
	components := append([]sbomComponent{target.Main}, target.Dependencies...)
	components = append(components, sbomComponent{Name: "stdlib", Version: target.GoVersion})
	for _, component := range components {
		if pkg.name != component.Name {
			continue
		}
		version := component.Version
		purl := "pkg:golang/" + component.Name + "@" + component.Version
		if component == target.Main {
			version = "UNKNOWN"
			purl = "pkg:golang/" + component.Name
		}
		if component.Name == "stdlib" {
			purl = "pkg:golang/stdlib@" + strings.TrimPrefix(component.Version, "go")
		}
		if pkg.version != version || pkg.purlCount != 1 || pkg.purl != purl || strings.ContainsAny(pkg.purl, "%?#") {
			return "", errors.New("invalid Go package")
		}
		return componentIdentity(component), nil
	}
	return "", errors.New("unexpected Go package")
}

func expectedTargetIdentities(target sbomTargetCatalog) map[string]struct{} {
	result := map[string]struct{}{
		componentIdentity(target.Main):                                              {},
		componentIdentity(sbomComponent{Name: "stdlib", Version: target.GoVersion}): {},
	}
	for _, dependency := range target.Dependencies {
		result[componentIdentity(dependency)] = struct{}{}
	}
	return result
}

func buildFinalSPDX(catalog sbomCatalog, options SBOMOptions, version string) map[string]any {
	type projected struct {
		component         sbomComponent
		version, purl, id string
	}
	projectedByIdentity := make(map[string]projected)
	membership := make([][]string, len(catalog.Targets))
	for i, targetCatalog := range catalog.Targets {
		components := append([]sbomComponent{targetCatalog.Main}, targetCatalog.Dependencies...)
		components = append(components, sbomComponent{Name: "stdlib", Version: targetCatalog.GoVersion})
		for _, component := range components {
			identity := componentIdentity(component)
			versionInfo := component.Version
			purl := "pkg:golang/" + component.Name + "@" + component.Version
			if component == targetCatalog.Main {
				versionInfo = "UNKNOWN"
				purl = "pkg:golang/" + component.Name
			}
			if component.Name == "stdlib" {
				purl = "pkg:golang/stdlib@" + strings.TrimPrefix(component.Version, "go")
			}
			hash := sha256.Sum256([]byte(component.Name + "\x00" + versionInfo))
			id := "SPDXRef-Package-" + hex.EncodeToString(hash[:])
			projectedByIdentity[identity] = projected{component: component, version: versionInfo, purl: purl, id: id}
			membership[i] = append(membership[i], id)
		}
	}
	packages := make([]any, 0, len(projectedByIdentity))
	for _, value := range projectedByIdentity {
		packages = append(packages, map[string]any{"name": value.component.Name, "SPDXID": value.id, "versionInfo": value.version, "downloadLocation": "NOASSERTION", "filesAnalyzed": false, "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION", "copyrightText": "NOASSERTION", "externalRefs": []any{map[string]any{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": value.purl}}})
	}
	slices.SortFunc(packages, func(a, b any) int {
		return strings.Compare(a.(map[string]any)["SPDXID"].(string), b.(map[string]any)["SPDXID"].(string))
	})
	files := make([]any, 0, len(catalog.Targets))
	relationships := make([]any, 0, len(catalog.Targets)*4)
	for i, targetCatalog := range catalog.Targets {
		fileID := "SPDXRef-File-ai-cli-gateway-" + targetCatalog.Target.GOOS + "-" + targetCatalog.Target.GOARCH
		files = append(files, map[string]any{"fileName": "./" + targetCatalog.RelativePath, "SPDXID": fileID, "fileTypes": []any{"BINARY"}, "checksums": []any{map[string]any{"algorithm": "SHA256", "checksumValue": targetCatalog.SHA256}}, "licenseConcluded": "NOASSERTION", "copyrightText": "NOASSERTION"})
		relationships = append(relationships, map[string]any{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": fileID})
		for _, packageID := range membership[i] {
			relationships = append(relationships, map[string]any{"spdxElementId": fileID, "relationshipType": "CONTAINS", "relatedSpdxElement": packageID})
		}
	}
	slices.SortFunc(files, func(a, b any) int {
		return strings.Compare(a.(map[string]any)["fileName"].(string), b.(map[string]any)["fileName"].(string))
	})
	slices.SortFunc(relationships, func(a, b any) int {
		first, second := a.(map[string]any), b.(map[string]any)
		for _, key := range []string{"spdxElementId", "relationshipType", "relatedSpdxElement"} {
			if compared := strings.Compare(first[key].(string), second[key].(string)); compared != 0 {
				return compared
			}
		}
		return 0
	})
	return map[string]any{"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT", "name": "ai-cli-gateway-" + version + "-five-target-collection", "documentNamespace": "https://github.com/krkarma777/ai-cli-gateway/releases/download/" + options.Tag + "/ai-cli-gateway_" + version + "_sbom.spdx.json", "creationInfo": map[string]any{"created": options.SourceTime.Format(time.RFC3339), "creators": []any{syftCreator}}, "packages": packages, "files": files, "relationships": relationships}
}

func scanFinalSPDX(value any, options SBOMOptions) error {
	sensitive := []string{options.RepositoryRoot, options.StagingRoot, options.OutputRoot, "/home/", "/root/", "/Users/", "C:/Users/", "C:\\Users\\"}
	var scan func(any) error
	scan = func(value any) error {
		switch typed := value.(type) {
		case string:
			if !utf8.ValidString(typed) {
				return errors.New("invalid string")
			}
			for _, r := range typed {
				if r < 0x20 || r == 0x7f {
					return errors.New("control string")
				}
			}
			normalized := strings.ReplaceAll(typed, "\\", "/")
			for _, prefix := range sensitive {
				if prefix != "" && (strings.Contains(typed, prefix) || strings.Contains(strings.ToLower(normalized), strings.ToLower(strings.ReplaceAll(prefix, "\\", "/")))) {
					return errors.New("sensitive string")
				}
			}
		case []any:
			for _, child := range typed {
				if err := scan(child); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, child := range typed {
				if err := scan(key); err != nil {
					return err
				}
				if err := scan(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return scan(value)
}

func cleanupCreatedFile(path string, created os.FileInfo) {
	if created == nil {
		return
	}
	current, err := os.Lstat(path)
	if err == nil && os.SameFile(created, current) {
		_ = os.Remove(path)
	}
}
func componentIdentity(component sbomComponent) string {
	return component.Name + "\x00" + component.Version
}
func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}
func optionalStringField(object map[string]any, key string) (string, error) {
	value, exists := object[key]
	if !exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("invalid string field")
	}
	return text, nil
}
func hasExactCreator(document map[string]any) bool {
	creation, ok := document["creationInfo"].(map[string]any)
	if !ok {
		return false
	}
	creators, ok := creation["creators"].([]any)
	if !ok {
		return false
	}
	count := 0
	for _, creator := range creators {
		text, ok := creator.(string)
		if !ok {
			return false
		}
		if text == syftCreator {
			count++
			continue
		}
		if !validNonToolCreator(text) {
			return false
		}
	}
	return count == 1
}
func validNonToolCreator(creator string) bool {
	var payload string
	switch {
	case strings.HasPrefix(creator, "Person: "):
		payload = strings.TrimPrefix(creator, "Person: ")
	case strings.HasPrefix(creator, "Organization: "):
		payload = strings.TrimPrefix(creator, "Organization: ")
	default:
		return false
	}
	if payload == "" || payload != strings.TrimSpace(payload) {
		return false
	}
	for _, r := range payload {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func isClassifierShape(pkg rawPackage) bool {
	return strings.HasPrefix(pkg.id, "SPDXRef-Package-binary-ai-cli-gateway-") && pkg.name == "ai-cli-gateway" && pkg.version == "UNKNOWN" && pkg.purlCount == 0
}
func addSPDXID(ids map[string]string, id, kind string) bool {
	if !validSPDXID(id) {
		return false
	}
	if _, exists := ids[id]; exists {
		return false
	}
	ids[id] = kind
	return true
}
func validSPDXID(id string) bool {
	if !strings.HasPrefix(id, "SPDXRef-") || len(id) == len("SPDXRef-") {
		return false
	}
	for _, r := range strings.TrimPrefix(id, "SPDXRef-") {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}
func isLowerSHA256(value string) bool {
	return isLowerHex(value, 64)
}
func isLowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
func normalizedRelationshipKey(relation rawRelationship) string {
	source, target, kind := relation.source, relation.target, relation.kind
	if kind == "CONTAINED_BY" {
		source, target, kind = target, source, "CONTAINS"
	}
	return fmt.Sprintf("%s\x00%s\x00%s", source, kind, target)
}
