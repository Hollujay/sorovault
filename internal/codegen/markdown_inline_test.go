package codegen_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/model"
)

// inlineDocumentPrefix is the generated document up to the first enum case,
// so each test can state the complete expected output without repeating the
// housekeeping above it.
func inlineDocumentPrefix(heading string) string {
	return fmt.Sprintf(
		"# Contract `%s`\n\nNetwork: `%s`  \nWASM hash: `%s`\n\n\n"+
			"## Functions\n\nNo functions.\n\n## Types\n\n### `%s`\n",
		src.ContractID, src.Network, src.WasmHash, heading)
}

// TestMarkdownInlineDocComments pins the exact rendering of enum case
// documentation. A doc comment comes from the contract author, so line
// breaks, pipes, backticks and leading hashes must stay inside the bullet
// rather than starting a new line, table or heading.
func TestMarkdownInlineDocComments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "single line",
			doc:  "One line.",
			want: " - One line.",
		},
		{
			name: "multiple lines",
			doc:  "First line.\nSecond line.",
			want: " - First line. Second line.",
		},
		{
			name: "pipes",
			doc:  "left | right",
			want: " - left | right",
		},
		{
			name: "table rows",
			doc:  "| a | b |\n|---|---|\n| 1 | 2 |",
			want: " - | a | b | |---|---| | 1 | 2 |",
		},
		{
			name: "backticks",
			doc:  "Use `scope` here.",
			want: " - Use `scope` here.",
		},
		{
			name: "leading hash",
			doc:  "# Not a heading",
			want: " - # Not a heading",
		},
		{
			name: "leading hash on a later line",
			doc:  "First line.\n# Not a heading",
			want: " - First line. # Not a heading",
		},
		{
			name: "empty",
			doc:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			iface := &model.Interface{Types: model.Types{Enums: []model.Enum{{
				Name:  "Status",
				Cases: []model.EnumCase{{Name: "Active", Value: 1, Doc: tt.doc}},
			}}}}

			got, err := codegen.Markdown(iface, src)
			require.NoError(t, err)

			want := inlineDocumentPrefix("enum Status") +
				"\n- `Active = 1`" + tt.want + "\n"
			assert.Equal(t, want, got)

			// The doc must not add structure of its own: the case is one
			// bullet and the only third-level heading is the enum's.
			assert.Equal(t, 1, countLinesWithPrefix(got, "### "))
			assert.Equal(t, 1, countLinesWithPrefix(got, "- "))
		})
	}
}

// TestMarkdownInlineErrorEnumDoc checks the other caller: an error enum's
// case documentation is rendered inline by the same helper.
func TestMarkdownInlineErrorEnumDoc(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{Types: model.Types{ErrorEnums: []model.ErrorEnum{{
		Name:  "Failing",
		Cases: []model.EnumCase{{Name: "Broken", Value: 7, Doc: "First line.\n# Not a heading | `code`"}},
	}}}}

	got, err := codegen.Markdown(iface, src)
	require.NoError(t, err)

	want := inlineDocumentPrefix("error Failing") +
		"\n- `Broken = 7` - First line. # Not a heading | `code`\n"
	assert.Equal(t, want, got)
	assert.Equal(t, 1, countLinesWithPrefix(got, "### "))
	assert.Equal(t, 1, countLinesWithPrefix(got, "- "))
}

// countLinesWithPrefix counts lines that begin with prefix, which is how a
// stray heading or bullet introduced by a doc comment would show up.
func countLinesWithPrefix(s, prefix string) int {
	count := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}
