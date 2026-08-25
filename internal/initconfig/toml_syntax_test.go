package initconfig

import (
	"errors"
	"reflect"
	"testing"
)

func TestTOMLSourceIndexTracksStandardTablesAndCommentOwnership(t *testing.T) {
	t.Parallel()

	source := []byte("# document comment\n\n" +
		"[server] # server header\n" +
		"# auth comment\n" +
		"api_key_env = \"AI_CLI_GATEWAY_API_KEY\" # inline\n" +
		"# trailing server comment\n\n" +
		"[runtime]\n" +
		"root = \"/runtime\"\n\n" +
		"[providers.\"codex\"]\n" +
		"# executable comment\n" +
		"executable = \"/bin/codex\"\n" +
		"config_home = \"/home/codex\"\n\n" +
		"[[models]]\n" +
		"id = \"codex-local\"\n" +
		"provider = \"codex\"\n" +
		"provider_model = \"gpt-test\"\n")

	index, err := buildSourceIndex(source)
	if err != nil {
		t.Fatalf("buildSourceIndex() error = %v", err)
	}
	if len(index.Groups) != 4 {
		t.Fatalf("Groups = %#v, want 4", index.Groups)
	}

	server := requireSourceGroup(t, index, []string{"server"}, -1)
	if server.Representation != representationTable {
		t.Fatalf("server representation = %d", server.Representation)
	}
	if got := sourceSlice(source, server.Header); got != "[server] # server header\n" {
		t.Fatalf("server header = %q", got)
	}
	if got := sourceSlice(source, server.Owned); got != "[server] # server header\n# auth comment\napi_key_env = \"AI_CLI_GATEWAY_API_KEY\" # inline\n" {
		t.Fatalf("server owned = %q", got)
	}
	auth := server.Entries["api_key_env"]
	if !reflect.DeepEqual(auth.Path, []string{"server", "api_key_env"}) {
		t.Fatalf("auth path = %q", auth.Path)
	}
	if got := sourceSlice(source, auth.Expression); got != "api_key_env = \"AI_CLI_GATEWAY_API_KEY\" # inline\n" {
		t.Fatalf("auth expression = %q", got)
	}
	if got := sourceSlice(source, auth.Value); got != `"AI_CLI_GATEWAY_API_KEY"` {
		t.Fatalf("auth value = %q", got)
	}
	if got := string(auth.LeadingComments); got != "# auth comment\n" {
		t.Fatalf("auth leading comments = %q", got)
	}

	provider := requireSourceGroup(t, index, []string{"providers", "codex"}, -1)
	if got := sourceSlice(source, provider.Header); got != "[providers.\"codex\"]\n" {
		t.Fatalf("provider header = %q", got)
	}
	if got := string(provider.Entries["executable"].LeadingComments); got != "# executable comment\n" {
		t.Fatalf("executable leading comments = %q", got)
	}

	model := requireSourceGroup(t, index, []string{"models"}, 0)
	if model.Representation != representationTable {
		t.Fatalf("model representation = %d", model.Representation)
	}
	if got := sourceSlice(source, model.Header); got != "[[models]]\n" {
		t.Fatalf("model header = %q", got)
	}

	if got := len(index.ByPath[sourcePathKey([]string{"providers", "codex"})]); got != 1 {
		t.Fatalf("provider ByPath count = %d", got)
	}
}

func TestTOMLSourceIndexTracksDottedQuotedKeysCRLFAndNoFinalNewline(t *testing.T) {
	t.Parallel()

	source := []byte("server.api_key_env = \"KEY\"\r\n" +
		"runtime.root = \"/runtime\"\r\n" +
		"providers.\"codex\".executable = \"/bin/codex\"\r\n" +
		"# home comment\r\n" +
		"providers.\"codex\".config_home = \"/home/codex\"\r\n" +
		"models = [{ id = \"codex-local\", provider = \"codex\", provider_model = \"gpt-test\" }]")

	index, err := buildSourceIndex(source)
	if err != nil {
		t.Fatalf("buildSourceIndex() error = %v", err)
	}
	server := requireSourceGroup(t, index, []string{"server"}, -1)
	if server.Representation != representationDotted {
		t.Fatalf("server representation = %d", server.Representation)
	}
	if got := sourceSlice(source, server.Owned); got != "server.api_key_env = \"KEY\"\r\n" {
		t.Fatalf("server owned = %q", got)
	}
	provider := requireSourceGroup(t, index, []string{"providers", "codex"}, -1)
	if provider.Representation != representationDotted {
		t.Fatalf("provider representation = %d", provider.Representation)
	}
	if got := string(provider.Entries["config_home"].LeadingComments); got != "# home comment\r\n" {
		t.Fatalf("home leading comments = %q", got)
	}
	model := requireSourceGroup(t, index, []string{"models"}, 0)
	if model.Representation != representationInline {
		t.Fatalf("model representation = %d", model.Representation)
	}
	if got := sourceSlice(source, model.Owned); got != `{ id = "codex-local", provider = "codex", provider_model = "gpt-test" }` {
		t.Fatalf("model owned = %q", got)
	}
	if got := sourceSlice(source, model.Entries["provider_model"].Value); got != `"gpt-test"` {
		t.Fatalf("provider_model value = %q", got)
	}
	if model.Owned.End != len(source)-1 {
		t.Fatalf("model owned end = %d, want closing brace offset %d", model.Owned.End, len(source)-1)
	}
}

func TestTOMLSourceIndexTracksInlineContainersWithoutOwningSiblings(t *testing.T) {
	t.Parallel()

	source := []byte("server = { api_key_env = \"KEY\" }\n" +
		"runtime = { root = \"/runtime\" }\n" +
		"providers = { codex = { executable = \"/bin/codex\", config_home = \"/home/codex\" }, claude = { executable = \"/bin/claude\", config_home = \"/home/claude\", credential_env = [\"ANTHROPIC_API_KEY\"] } }\n" +
		"models = [\n" +
		"  { id = \"codex-local\", provider = \"codex\", provider_model = \"gpt-test\" },\n" +
		"  { id = \"claude-local\", provider = \"claude\", provider_model = \"sonnet\" },\n" +
		"]\n")

	index, err := buildSourceIndex(source)
	if err != nil {
		t.Fatalf("buildSourceIndex() error = %v", err)
	}
	server := requireSourceGroup(t, index, []string{"server"}, -1)
	if server.Representation != representationInline ||
		sourceSlice(source, server.Owned) != `{ api_key_env = "KEY" }` {
		t.Fatalf("server = %#v, owned %q", server, sourceSlice(source, server.Owned))
	}
	codex := requireSourceGroup(t, index, []string{"providers", "codex"}, -1)
	claude := requireSourceGroup(t, index, []string{"providers", "claude"}, -1)
	if sourceSlice(source, codex.Owned) != `{ executable = "/bin/codex", config_home = "/home/codex" }` {
		t.Fatalf("codex owned = %q", sourceSlice(source, codex.Owned))
	}
	if sourceSlice(source, claude.Owned) != `{ executable = "/bin/claude", config_home = "/home/claude", credential_env = ["ANTHROPIC_API_KEY"] }` {
		t.Fatalf("claude owned = %q", sourceSlice(source, claude.Owned))
	}
	if codex.Owned.End >= claude.Owned.Start {
		t.Fatalf("inline provider ranges overlap: %#v %#v", codex.Owned, claude.Owned)
	}
	first := requireSourceGroup(t, index, []string{"models"}, 0)
	second := requireSourceGroup(t, index, []string{"models"}, 1)
	if sourceSlice(source, first.Owned) != `{ id = "codex-local", provider = "codex", provider_model = "gpt-test" }` {
		t.Fatalf("first model owned = %q", sourceSlice(source, first.Owned))
	}
	if sourceSlice(source, second.Owned) != `{ id = "claude-local", provider = "claude", provider_model = "sonnet" }` {
		t.Fatalf("second model owned = %q", sourceSlice(source, second.Owned))
	}
}

func TestTOMLSourceIndexTracksMultipleArrayTablesAndBlankCommentBoundary(t *testing.T) {
	t.Parallel()

	source := []byte("[[models]]\n" +
		"id = \"one\"\n" +
		"provider = \"codex\"\n" +
		"provider_model = \"gpt-one\"\n\n" +
		"# detached by blank line\n\n" +
		"[[models]]\n" +
		"# attached\n" +
		"id = \"two\"\n" +
		"provider = \"codex\"\n" +
		"provider_model = \"gpt-two\"")

	index, err := buildSourceIndex(source)
	if err != nil {
		t.Fatalf("buildSourceIndex() error = %v", err)
	}
	first := requireSourceGroup(t, index, []string{"models"}, 0)
	second := requireSourceGroup(t, index, []string{"models"}, 1)
	if first.ArrayIndex != 0 || second.ArrayIndex != 1 {
		t.Fatalf("array indexes = %d, %d", first.ArrayIndex, second.ArrayIndex)
	}
	if got := string(second.Entries["id"].LeadingComments); got != "# attached\n" {
		t.Fatalf("attached comments = %q", got)
	}
	if got := sourceSlice(source, first.Owned); got == "" || got[len(got)-1] != '\n' {
		t.Fatalf("first owned range = %q", got)
	}
	if got := sourceSlice(source, second.Owned); got[len(got)-1] == '\n' {
		t.Fatalf("no-final-newline owned range = %q", got)
	}
}

func TestTOMLSourceIndexRejectsMalformedInputWithoutEcho(t *testing.T) {
	t.Parallel()

	const planted = "PLANTED_PARSE_SECRET"
	_, err := buildSourceIndex([]byte("server = { " + planted))
	if !errors.Is(err, ErrPlan) {
		t.Fatalf("buildSourceIndex() error = %v, want ErrPlan", err)
	}
	if err != nil && err.Error() != ErrPlan.Error() {
		t.Fatalf("buildSourceIndex() leaked parser detail: %q", err)
	}
}

func requireSourceGroup(
	t *testing.T,
	index sourceIndex,
	path []string,
	arrayIndex int,
) sourceGroup {
	t.Helper()
	for _, groupIndex := range index.ByPath[sourcePathKey(path)] {
		group := index.Groups[groupIndex]
		if group.ArrayIndex == arrayIndex {
			return group
		}
	}
	t.Fatalf("missing source group %q[%d] in %#v", path, arrayIndex, index.Groups)
	return sourceGroup{}
}

func sourceSlice(source []byte, span byteRange) string {
	if span.Start < 0 || span.End < span.Start || span.End > len(source) {
		return "<invalid>"
	}
	return string(source[span.Start:span.End])
}
