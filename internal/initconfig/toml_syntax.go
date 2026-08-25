package initconfig

import (
	"bytes"
	"strings"

	"github.com/pelletier/go-toml/v2/unstable"
)

type byteRange struct {
	Start int
	End   int
}

type representation uint8

const (
	representationTable representation = iota + 1
	representationDotted
	representationInline
)

type sourceEntry struct {
	Path            []string
	Expression      byteRange
	Value           byteRange
	LeadingComments []byte
}

type sourceGroup struct {
	Path           []string
	ArrayIndex     int
	Representation representation
	Header         byteRange
	Owned          byteRange
	Entries        map[string]sourceEntry
}

type sourceIndex struct {
	Groups []sourceGroup
	ByPath map[string][]int
}

type sourceIndexBuilder struct {
	source          []byte
	index           sourceIndex
	currentPath     []string
	currentGroup    int
	pendingComments byteRange
	hasPending      bool
	arrayIndexes    map[string]int
}

func buildSourceIndex(source []byte) (sourceIndex, error) {
	builder := sourceIndexBuilder{
		source: source,
		index: sourceIndex{
			ByPath: make(map[string][]int),
		},
		currentGroup: -1,
		arrayIndexes: make(map[string]int),
	}
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(source)
	for parser.NextExpression() {
		expression := parser.Expression()
		if expression == nil {
			return sourceIndex{}, ErrPlan
		}
		switch expression.Kind {
		case unstable.Comment:
			if !builder.noteComment(rangeOf(expression.Raw)) {
				return sourceIndex{}, ErrPlan
			}
		case unstable.Table, unstable.ArrayTable:
			if !builder.addTable(expression) {
				return sourceIndex{}, ErrPlan
			}
		case unstable.KeyValue:
			if !builder.addKeyValue(expression) {
				return sourceIndex{}, ErrPlan
			}
		case unstable.Invalid, unstable.Key, unstable.Array, unstable.InlineTable,
			unstable.String, unstable.Bool, unstable.Float, unstable.Integer,
			unstable.LocalDate, unstable.LocalTime, unstable.LocalDateTime,
			unstable.DateTime:
			return sourceIndex{}, ErrPlan
		default:
			return sourceIndex{}, ErrPlan
		}
	}
	if parser.Error() != nil {
		return sourceIndex{}, ErrPlan
	}
	return cloneSourceIndex(builder.index), nil
}

func extractTOMLValue(document []byte) ([]byte, error) {
	parser := unstable.Parser{}
	parser.Reset(document)
	if !parser.NextExpression() {
		return nil, ErrPlan
	}
	expression := parser.Expression()
	if expression == nil || expression.Kind != unstable.KeyValue {
		return nil, ErrPlan
	}
	keys, _, ok := copiedKey(expression)
	if !ok || len(keys) != 1 || keys[0] != "value" {
		return nil, ErrPlan
	}
	_, value, ok := keyValueRanges(document, expression, false)
	if !ok || parser.NextExpression() || parser.Error() != nil {
		return nil, ErrPlan
	}
	return append([]byte(nil), document[value.Start:value.End]...), nil
}

func (builder *sourceIndexBuilder) noteComment(raw byteRange) bool {
	if !validByteRange(raw, len(builder.source)) {
		return false
	}
	line := lineRange(builder.source, raw.Start)
	if builder.hasPending && onlyHorizontalSpace(
		builder.source[builder.pendingComments.End:line.Start],
	) {
		builder.pendingComments.End = line.End
		return true
	}
	builder.pendingComments = line
	builder.hasPending = true
	return true
}

func (builder *sourceIndexBuilder) addTable(node *unstable.Node) bool {
	path, first, ok := copiedKey(node)
	if !ok || !validByteRange(first, len(builder.source)) {
		return false
	}
	header := lineRange(builder.source, first.Start)
	arrayIndex := -1
	if node.Kind == unstable.ArrayTable {
		key := sourcePathKey(path)
		arrayIndex = builder.arrayIndexes[key]
		builder.arrayIndexes[key] = arrayIndex + 1
	}
	group := sourceGroup{
		Path:           path,
		ArrayIndex:     arrayIndex,
		Representation: representationTable,
		Header:         header,
		Owned:          header,
		Entries:        make(map[string]sourceEntry),
	}
	builder.currentGroup = builder.appendGroup(group)
	builder.currentPath = append([]string(nil), path...)
	builder.hasPending = false
	return true
}

func (builder *sourceIndexBuilder) addKeyValue(node *unstable.Node) bool {
	keys, _, ok := copiedKey(node)
	if !ok {
		return false
	}
	expression, value, ok := keyValueRanges(builder.source, node, true)
	if !ok {
		return false
	}
	fullPath := append(append([]string(nil), builder.currentPath...), keys...)
	leading := builder.takeLeading(expression.Start)
	if builder.addInlineValue(fullPath, node.Value(), value) {
		return true
	}

	groupPath, field, ok := semanticEntryPath(fullPath)
	if !ok {
		builder.hasPending = false
		return true
	}
	groupIndex := builder.tableGroupFor(groupPath)
	if groupIndex < 0 {
		groupIndex = builder.dottedGroupFor(groupPath, expression)
	}
	group := &builder.index.Groups[groupIndex]
	if _, duplicate := group.Entries[field]; duplicate {
		return false
	}
	group.Entries[field] = sourceEntry{
		Path:            append([]string(nil), fullPath...),
		Expression:      expression,
		Value:           value,
		LeadingComments: leading,
	}
	if expression.End > group.Owned.End {
		group.Owned.End = expression.End
	}
	return true
}

func (builder *sourceIndexBuilder) addInlineValue(
	path []string,
	valueNode *unstable.Node,
	value byteRange,
) bool {
	if valueNode == nil {
		return false
	}
	switch {
	case len(path) == 1 &&
		(path[0] == "server" || path[0] == "runtime") &&
		valueNode.Kind == unstable.InlineTable:
		return builder.addInlineGroup(path, valueNode, value, -1)
	case len(path) == 1 && path[0] == "providers" &&
		valueNode.Kind == unstable.InlineTable:
		children := valueNode.Children()
		for children.Next() {
			child := children.Node()
			if child.Kind == unstable.Comment {
				continue
			}
			if child.Kind != unstable.KeyValue {
				return false
			}
			providerPath, _, ok := copiedKey(child)
			if !ok || len(providerPath) != 1 ||
				child.Value().Kind != unstable.InlineTable {
				return false
			}
			_, providerValue, ok := keyValueRanges(builder.source, child, false)
			if !ok || !builder.addInlineGroup(
				[]string{"providers", providerPath[0]},
				child.Value(),
				providerValue,
				-1,
			) {
				return false
			}
		}
		return true
	case len(path) == 2 && path[0] == "providers" &&
		valueNode.Kind == unstable.InlineTable:
		return builder.addInlineGroup(path, valueNode, value, -1)
	case len(path) == 1 && path[0] == "models" &&
		valueNode.Kind == unstable.Array:
		children := valueNode.Children()
		for children.Next() {
			child := children.Node()
			if child.Kind == unstable.Comment {
				continue
			}
			if child.Kind != unstable.InlineTable {
				return false
			}
			owned, ok := containerRange(builder.source, child, '{', '}')
			if !ok {
				return false
			}
			key := sourcePathKey([]string{"models"})
			arrayIndex := builder.arrayIndexes[key]
			builder.arrayIndexes[key] = arrayIndex + 1
			if !builder.addInlineGroup(
				[]string{"models"},
				child,
				owned,
				arrayIndex,
			) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (builder *sourceIndexBuilder) addInlineGroup(
	path []string,
	node *unstable.Node,
	owned byteRange,
	arrayIndex int,
) bool {
	if !validByteRange(owned, len(builder.source)) {
		return false
	}
	group := sourceGroup{
		Path:           append([]string(nil), path...),
		ArrayIndex:     arrayIndex,
		Representation: representationInline,
		Owned:          owned,
		Entries:        make(map[string]sourceEntry),
	}
	children := node.Children()
	var pending []byte
	for children.Next() {
		child := children.Node()
		if child.Kind == unstable.Comment {
			comment := rangeOf(child.Raw)
			if !validByteRange(comment, len(builder.source)) {
				return false
			}
			pending = append(pending, builder.source[comment.Start:comment.End]...)
			pending = append(pending, '\n')
			continue
		}
		if child.Kind != unstable.KeyValue {
			return false
		}
		keys, _, ok := copiedKey(child)
		if !ok || len(keys) != 1 {
			return false
		}
		expression, value, ok := keyValueRanges(builder.source, child, false)
		if !ok {
			return false
		}
		field := keys[0]
		if _, duplicate := group.Entries[field]; duplicate {
			return false
		}
		group.Entries[field] = sourceEntry{
			Path:            append(append([]string(nil), path...), field),
			Expression:      expression,
			Value:           value,
			LeadingComments: append([]byte(nil), pending...),
		}
		pending = nil
	}
	builder.appendGroup(group)
	return true
}

func (builder *sourceIndexBuilder) takeLeading(expressionStart int) []byte {
	defer func() {
		builder.hasPending = false
	}()
	if !builder.hasPending || builder.pendingComments.End > expressionStart ||
		!onlyHorizontalSpace(builder.source[builder.pendingComments.End:expressionStart]) {
		return nil
	}
	return append(
		[]byte(nil),
		builder.source[builder.pendingComments.Start:builder.pendingComments.End]...,
	)
}

func (builder *sourceIndexBuilder) tableGroupFor(path []string) int {
	if builder.currentGroup < 0 || builder.currentGroup >= len(builder.index.Groups) {
		return -1
	}
	group := builder.index.Groups[builder.currentGroup]
	if group.Representation == representationTable && equalPath(group.Path, path) {
		return builder.currentGroup
	}
	return -1
}

func (builder *sourceIndexBuilder) dottedGroupFor(
	path []string,
	expression byteRange,
) int {
	key := sourcePathKey(path)
	indexes := builder.index.ByPath[key]
	for index := len(indexes) - 1; index >= 0; index-- {
		groupIndex := indexes[index]
		if builder.index.Groups[groupIndex].Representation == representationDotted {
			return groupIndex
		}
	}
	return builder.appendGroup(sourceGroup{
		Path:           append([]string(nil), path...),
		ArrayIndex:     -1,
		Representation: representationDotted,
		Owned:          expression,
		Entries:        make(map[string]sourceEntry),
	})
}

func (builder *sourceIndexBuilder) appendGroup(group sourceGroup) int {
	index := len(builder.index.Groups)
	builder.index.Groups = append(builder.index.Groups, group)
	key := sourcePathKey(group.Path)
	builder.index.ByPath[key] = append(builder.index.ByPath[key], index)
	return index
}

func semanticEntryPath(path []string) ([]string, string, bool) {
	switch {
	case len(path) == 2 && (path[0] == "server" || path[0] == "runtime"):
		return append([]string(nil), path[:1]...), path[1], true
	case len(path) == 3 && path[0] == "providers":
		return append([]string(nil), path[:2]...), path[2], true
	case len(path) == 2 && path[0] == "models":
		return []string{"models"}, path[1], true
	default:
		return nil, "", false
	}
}

func copiedKey(node *unstable.Node) ([]string, byteRange, bool) {
	if node == nil {
		return nil, byteRange{}, false
	}
	iterator := node.Key()
	var path []string
	var first byteRange
	for iterator.Next() {
		key := iterator.Node()
		if key == nil || key.Kind != unstable.Key {
			return nil, byteRange{}, false
		}
		if len(path) == 0 {
			first = rangeOf(key.Raw)
		}
		path = append(path, string(append([]byte(nil), key.Data...)))
	}
	return path, first, len(path) > 0
}

func keyValueRanges(
	source []byte,
	node *unstable.Node,
	topLevel bool,
) (byteRange, byteRange, bool) {
	if node == nil || node.Kind != unstable.KeyValue {
		return byteRange{}, byteRange{}, false
	}
	raw := rangeOf(node.Raw)
	if !validByteRange(raw, len(source)) || raw.Start == raw.End {
		return byteRange{}, byteRange{}, false
	}
	iterator := node.Key()
	lastKeyEnd := -1
	for iterator.Next() {
		keyRange := rangeOf(iterator.Node().Raw)
		if !validByteRange(keyRange, len(source)) {
			return byteRange{}, byteRange{}, false
		}
		lastKeyEnd = keyRange.End
	}
	if lastKeyEnd < raw.Start || lastKeyEnd >= raw.End {
		return byteRange{}, byteRange{}, false
	}
	equals := bytes.IndexByte(source[lastKeyEnd:raw.End], '=')
	if equals < 0 {
		return byteRange{}, byteRange{}, false
	}
	valueStart := lastKeyEnd + equals + 1
	for valueStart < raw.End && isTOMLWhitespace(source[valueStart]) {
		valueStart++
	}
	if valueStart >= raw.End {
		return byteRange{}, byteRange{}, false
	}
	expression := raw
	if topLevel {
		expression = lineRange(source, raw.Start)
	}
	return expression, byteRange{Start: valueStart, End: raw.End}, true
}

func containerRange(
	source []byte,
	node *unstable.Node,
	open byte,
	closing byte,
) (byteRange, bool) {
	if node == nil {
		return byteRange{}, false
	}
	raw := rangeOf(node.Raw)
	if !validByteRange(raw, len(source)) || raw.Start >= len(source) ||
		source[raw.Start] != open {
		return byteRange{}, false
	}
	end, ok := matchingContainerEnd(source, raw.Start, open, closing)
	if !ok {
		return byteRange{}, false
	}
	return byteRange{Start: raw.Start, End: end}, true
}

func matchingContainerEnd(
	source []byte,
	start int,
	open byte,
	closing byte,
) (int, bool) {
	depth := 0
	for index := start; index < len(source); {
		switch source[index] {
		case '#':
			index++
			for index < len(source) && source[index] != '\n' {
				index++
			}
		case '\'', '"':
			var ok bool
			index, ok = skipTOMLString(source, index)
			if !ok {
				return 0, false
			}
		default:
			if source[index] == open {
				depth++
			}
			if source[index] == closing {
				depth--
				if depth == 0 {
					return index + 1, true
				}
			}
			index++
		}
	}
	return 0, false
}

func skipTOMLString(source []byte, start int) (int, bool) {
	quote := source[start]
	multiline := start+2 < len(source) &&
		source[start+1] == quote && source[start+2] == quote
	index := start + 1
	if multiline {
		index = start + 3
	}
	for index < len(source) {
		if quote == '"' && source[index] == '\\' {
			index += 2
			continue
		}
		if multiline {
			if index+2 < len(source) && source[index] == quote &&
				source[index+1] == quote && source[index+2] == quote {
				return index + 3, true
			}
			index++
			continue
		}
		if source[index] == quote {
			return index + 1, true
		}
		index++
	}
	return 0, false
}

func lineRange(source []byte, offset int) byteRange {
	start := bytes.LastIndexByte(source[:offset], '\n') + 1
	newline := bytes.IndexByte(source[offset:], '\n')
	if newline < 0 {
		return byteRange{Start: start, End: len(source)}
	}
	return byteRange{Start: start, End: offset + newline + 1}
}

func rangeOf(raw unstable.Range) byteRange {
	start := int(raw.Offset)
	return byteRange{Start: start, End: start + int(raw.Length)}
}

func validByteRange(value byteRange, length int) bool {
	return value.Start >= 0 && value.End >= value.Start && value.End <= length
}

func onlyHorizontalSpace(value []byte) bool {
	for _, character := range value {
		if character != ' ' && character != '\t' {
			return false
		}
	}
	return true
}

func isTOMLWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func sourcePathKey(path []string) string {
	return strings.Join(path, "\x00")
}

func equalPath(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneSourceIndex(index sourceIndex) sourceIndex {
	cloned := sourceIndex{
		Groups: make([]sourceGroup, len(index.Groups)),
		ByPath: make(map[string][]int, len(index.ByPath)),
	}
	for groupIndex, group := range index.Groups {
		cloned.Groups[groupIndex] = group
		cloned.Groups[groupIndex].Path = append([]string(nil), group.Path...)
		cloned.Groups[groupIndex].Entries = make(map[string]sourceEntry, len(group.Entries))
		for name, entry := range group.Entries {
			entry.Path = append([]string(nil), entry.Path...)
			entry.LeadingComments = append([]byte(nil), entry.LeadingComments...)
			cloned.Groups[groupIndex].Entries[name] = entry
		}
	}
	for path, indexes := range index.ByPath {
		cloned.ByPath[path] = append([]int(nil), indexes...)
	}
	return cloned
}
