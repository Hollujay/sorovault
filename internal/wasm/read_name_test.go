package wasm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readName is unexported, so these cases live in the package itself. They pin
// how a malformed custom-section name is rejected before any section body is
// looked at.

func TestReadNameValid(t *testing.T) {
	t.Parallel()

	payload := append([]byte{0x08}, "testname"...)
	payload = append(payload, 0xAA, 0xBB)

	gotName, rest, err := readName(payload)
	require.NoError(t, err)
	assert.Equal(t, "testname", gotName)
	assert.Equal(t, []byte{0xAA, 0xBB}, rest)
}

func TestReadNameLengthExceedsPayload(t *testing.T) {
	t.Parallel()

	// The length prefix claims 10 bytes, but only 4 follow.
	payload := []byte{0x0a, 0x74, 0x65, 0x73, 0x74}

	_, _, err := readName(payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
}

func TestReadNameZeroLength(t *testing.T) {
	t.Parallel()

	gotName, rest, err := readName([]byte{0x00})
	require.NoError(t, err)
	assert.Empty(t, gotName)
	assert.Empty(t, rest)
}

func TestReadNameMissingLength(t *testing.T) {
	t.Parallel()

	// An empty payload has no length prefix at all.
	_, _, err := readName(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name length")
}

func TestReadNameTruncatedLength(t *testing.T) {
	t.Parallel()

	// 0x80 sets the continuation bit, so the LEB128 length never ends.
	_, _, err := readName([]byte{0x80})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name length")
}

func TestReadNameOverlongLength(t *testing.T) {
	t.Parallel()

	// Six continuation bytes cannot fit in the 32 bits a WASM length allows.
	_, _, err := readName([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflows")
}

func TestReadNameLongerThanTheLimit(t *testing.T) {
	t.Parallel()

	// One byte over maxSectionNameLen is refused before any bytes are copied.
	payload := []byte{0x81, 0x80, 0x04} // 65537 as LEB128

	_, _, err := readName(payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too long")
}

func TestReadNameExactBoundaries(t *testing.T) {
	t.Parallel()

	// The name consumes every remaining byte.
	gotName, rest, err := readName([]byte{0x02, 'h', 'i'})
	require.NoError(t, err)
	assert.Equal(t, "hi", gotName)
	assert.Empty(t, rest)
}

func TestCustomSectionRejectsATruncatedName(t *testing.T) {
	t.Parallel()

	// A custom section (id 0) of 3 bytes whose name claims 5.
	module := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, 0x00, 0x03, 0x05, 'a', 'b'}

	_, err := CustomSection(module, SectionContractSpec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
}
