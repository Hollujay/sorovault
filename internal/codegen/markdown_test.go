package codegen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/model"
)

// TestGoldenZKVoteMarkdown documents the real testnet contract whose decoded
// ABI the spec package pins and compares the result byte for byte with a
// checked-in file, the way TestGoldenZKVote does for the TypeScript client.
//
// The zkvote fixture declares functions, structs, an error enum and an
// event, but no union or plain enum. One of each is added to the decoded
// interface so the golden exercises every declaration kind the Markdown
// generator writes.
func TestGoldenZKVoteMarkdown(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../spec/testdata/zkvote.golden.json")
	require.NoError(t, err)
	var iface model.Interface
	require.NoError(t, json.Unmarshal(raw, &iface))

	iface.Types.Unions = append(iface.Types.Unions, model.Union{
		Name: "DataKey",
		Doc:  "Key under which a proposal is stored.",
		Cases: []model.UnionCase{
			{Name: "Admin", Doc: "The contract administrator."},
			{Name: "Balance", Doc: "A voter's balance.", Values: []model.Type{
				model.Scalar(model.KindAddress),
				model.Scalar(model.KindI128),
			}},
		},
	})
	iface.Types.Enums = append(iface.Types.Enums, model.Enum{
		Name: "Status",
		Doc:  "Lifecycle of a proposal.",
		Cases: []model.EnumCase{
			{Name: "Open", Value: 0, Doc: "Accepting votes."},
			{Name: "Closed", Value: 1, Doc: "No longer accepting votes."},
		},
	})

	got, err := codegen.Markdown(&iface, src)
	require.NoError(t, err)

	golden := filepath.Join("testdata", "zkvote.golden.md")
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		t.Logf("wrote %s", golden)
	}
	want, err := os.ReadFile(golden) //nolint:gosec // test-owned golden path
	require.NoError(t, err, "golden file missing; regenerate with -update")
	assert.Equal(t, string(want), got,
		"generated documentation changed; if intended, rerun with -update")

	// A regenerated golden would also absorb a regression that dropped a
	// section, so check that every declaration kind made it into the file.
	for _, section := range []string{
		"## Functions",
		"### `fn vote(",
		"## Types",
		"### `struct Proposal`",
		"### `struct VoteParams`",
		"### `enum DataKey`",
		"### `enum Status`",
		"### `error Error`",
		"## Events",
		"### `Voted`",
	} {
		assert.Contains(t, got, section)
	}
}
