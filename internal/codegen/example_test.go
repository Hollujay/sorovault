package codegen_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/model"
)

// zeroAddress is the all-zero public key account address. It is a valid
// spelling of both an Address and a MuxedAddress as their schemas spell them,
// so one placeholder covers both kinds.
const zeroAddress = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

// placeholderBytesN is the zero placeholder of a BytesN of the given length,
// hexadecimal, two characters per byte.
func placeholderBytesN(n uint32) string {
	out := make([]byte, 2*n)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}

// quoted is a JSON string literal.
func quoted(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// TestExampleForEveryScalarKind covers the placeholder table, and validates
// each example against the schema of the same function, so a placeholder that
// does not satisfy its own type is a failing test rather than a docstring
// claim.
func TestExampleForEveryScalarKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		kind     model.Kind
		n        uint32
		thought  string
		expected string
	}{
		{"val", model.KindVal, 0, "anything", "42"},
		{"bool", model.KindBool, 0, "unflipped", "false"},
		{"void", model.KindVoid, 0, "nothing", "null"},
		{"error", model.KindError, 0, "the first code", "0"},
		{"u32", model.KindU32, 0, "a JSON number", "1"},
		{"i32", model.KindI32, 0, "a JSON number", "1"},
		{"u64", model.KindU64, 0, "wider than 53 bits", `"1"`},
		{"i64", model.KindI64, 0, "wider than 53 bits", `"1"`},
		{"u128", model.KindU128, 0, "wider than 53 bits", `"1"`},
		{"i128", model.KindI128, 0, "wider than 53 bits", `"1"`},
		{"u256", model.KindU256, 0, "wider than 53 bits", `"1"`},
		{"i256", model.KindI256, 0, "wider than 53 bits", `"1"`},
		{"timepoint", model.KindTimepoint, 0, "wider than 53 bits", `"1"`},
		{"duration", model.KindDuration, 0, "wider than 53 bits", `"1"`},
		{"bytes", model.KindBytes, 0, "one zero byte", `"00"`},
		{"bytes_n32", model.KindBytesN, 32, "zeroed, hexadecimal", quoted(placeholderBytesN(32))},
		{"string", model.KindString, 0, "simple", `"example"`},
		{"symbol", model.KindSymbol, 0, "letters, digits, underscores", `"name_0"`},
		{"address", model.KindAddress, 0, "the all-zero account", quoted(zeroAddress)},
		{"muxed_address", model.KindMuxedAddress, 0, "the plain-account spelling", quoted(zeroAddress)},
		{"unknown", model.Kind("mystery"), 0, "the schema accepts anything", "null"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			iface := &model.Interface{Functions: []model.Function{
				{Name: "f", Inputs: []model.Param{{Name: "a", Type: scalarType(tc.kind, tc.n)}}},
			}}
			fn, ok := iface.Function("f")
			require.True(t, ok)

			got, err := codegen.Example(iface, fn, src)
			require.NoError(t, err)
			assert.JSONEq(t, `{"a": `+tc.expected+`}`, string(got),
				"the placeholder for %s", tc.name)

			// And the value satisfies the schema of the same function.
			schema, err := codegen.JSONSchema(iface, fn, src)
			require.NoError(t, err)
			assert.NoError(t, validate(t, compileSchema(t, schema), string(got)),
				"the %s example must validate against its own JSON Schema", tc.name)
		})
	}
}

// scalarType is the argument type for a scalar-table entry.
func scalarType(kind model.Kind, n uint32) model.Type {
	if kind == model.KindBytesN {
		return model.Type{Kind: model.KindBytesN, N: n, Display: "BytesN"}
	}
	if _, ok := scalarKinds[kind]; !ok && kind == model.Kind("mystery") {
		// model.Scalar requires a real scalar kind; an unknown kind is a
		// raw Type, which is what an interface carries for a kind this
		// toolchain has never heard of.
		return model.Type{Kind: kind, Display: "mystery"}
	}
	return model.Scalar(kind)
}

// scalarKinds is the set the model's Scalar builder accepts.
var scalarKinds = map[model.Kind]bool{
	model.KindVal:  true,
	model.KindBool: true,
	// The model's scalar set proper; the placeholder table builds one
	// function per entry, so only the members the table exercises matter.
	model.KindVoid: true, model.KindError: true, model.KindU32: true,
	model.KindI32: true, model.KindU64: true, model.KindI64: true,
	model.KindU128: true, model.KindI128: true, model.KindU256: true,
	model.KindI256: true, model.KindTimepoint: true, model.KindDuration: true,
	model.KindBytes: true, model.KindString: true, model.KindSymbol: true,
	model.KindAddress: true, model.KindMuxedAddress: true,
}

// TestExampleForEveryContainerShape covers the types the placeholder table
// cannot express alone: containers, user-defined types, unions, enums, and
// request nesting that passes through a Vec rather than a struct field.
func TestExampleForEveryContainerShape(t *testing.T) {
	t.Parallel()

	// nillable builds each value the map-based placeholder uses, with every
	// arm given.
	iface := &model.Interface{
		Types: model.Types{
			Structs: []model.Struct{
				{Name: "Params", Fields: []model.StructField{
					{Name: "scope", Type: model.Scalar(model.KindU64)},
					{Name: "weight", Type: model.Scalar(model.KindU32)},
					{Name: "slug", Type: model.Scalar(model.KindString)},
				}},
				// A tuple struct is encoded positionally.
				{Name: "Pair", IsTuple: true, Fields: []model.StructField{
					{Name: "0", Type: model.Scalar(model.KindU32)},
					{Name: "1", Type: model.Scalar(model.KindSymbol)},
				}},
				// Nesting that circles back goes through a Vec, so the
				// example comes out with an empty list rather than deep.
				{Name: "Node", Fields: []model.StructField{
					{Name: "cost", Type: model.Scalar(model.KindU128)},
					{Name: "children", Type: model.Type{Kind: model.KindVec,
						Element: ptr(model.Type{Kind: model.KindUDT, Name: "Node"})}},
				}},
			},
			Unions: []model.Union{
				{Name: "Choice", Cases: []model.UnionCase{
					{Name: "NoVotes", Values: nil},
					{Name: "SomeVotes", Values: []model.Type{model.Scalar(model.KindU32)}},
				}},
			},
			Enums:      []model.Enum{{Name: "Status", Cases: []model.EnumCase{{Name: "Draft", Value: 0}, {Name: "Open", Value: 1}}}},
			ErrorEnums: []model.ErrorEnum{{Name: "Error", Cases: []model.EnumCase{{Name: "Denied", Value: 5}}}},
		},
		Functions: []model.Function{{Name: "everything", Inputs: []model.Param{
			{Name: "params", Type: model.Type{Kind: model.KindUDT, Name: "Params"}},
			{Name: "pair", Type: model.Type{Kind: model.KindUDT, Name: "Pair"}},
			{Name: "node", Type: model.Type{Kind: model.KindUDT, Name: "Node"}},
			{Name: "choice", Type: model.Type{Kind: model.KindUDT, Name: "Choice"}},
			{Name: "status", Type: model.Type{Kind: model.KindUDT, Name: "Status"}},
			{Name: "code", Type: model.Type{Kind: model.KindUDT, Name: "Error"}},
			{Name: "maybe", Type: model.Type{Kind: model.KindOption, Inner: ptr(model.Scalar(model.KindString))}},
			{Name: "totals", Type: model.Type{Kind: model.KindMap,
				Key: ptr(model.Scalar(model.KindSymbol)), Value: ptr(model.Scalar(model.KindI128))}},
			{Name: "two", Type: model.Type{Kind: model.KindTuple,
				Elements: []model.Type{model.Scalar(model.KindU32), {Kind: model.KindUDT, Name: "Pair"}}}},
			{Name: "outcome", Type: model.Type{Kind: model.KindResult,
				Ok: ptr(model.Type{Kind: model.KindUDT, Name: "Params"}), Error: ptr(model.Scalar(model.KindError))}},
		}}},
	}

	fn, ok := iface.Function("everything")
	require.True(t, ok, "the interface declares everything")

	got, err := codegen.Example(iface, fn, src)
	require.NoError(t, err)
	body := string(got)
	assert.Equal(t, `{`+
		`"params":{"scope":"1","weight":1,"slug":`+quoted("example")+`},`+
		`"pair":[1,`+quoted("name_0")+`],`+
		`"node":{"cost":"1","children":[]},`+
		`"choice":{"tag":"NoVotes","values":null},`+
		`"status":0,`+
		`"code":5,`+
		`"maybe":null,`+
		`"totals":{},`+
		`"two":[1,`+`[1,`+quoted("name_0")+`]],`+
		`"outcome":{"scope":"1","weight":1,"slug":`+quoted("example")+`}`+
		`}`, string(got), "placeholders follow the type's own encoding, and fields keep their declaration order")

	schema, err := codegen.JSONSchema(iface, fn, src)
	require.NoError(t, err)
	require.NoError(t, validate(t, compileSchema(t, schema), body),
		"a worked example over every shape must still validate against the schema")
}

// TestExampleValidatesForTheRealFixture is the acceptance the issue states.
// Every function of the real testnet contract gets an example, and each
// example is validated against that function's generated JSON Schema with a
// real validator, over the whole interface and not one hand-picked function.
func TestExampleValidatesForTheRealFixture(t *testing.T) {
	t.Parallel()

	iface := zkvoteInterface(t)
	require.NotEmpty(t, iface.Functions)

	for _, fn := range iface.Functions {
		t.Run(fn.Name, func(t *testing.T) {
			t.Parallel()

			got, err := codegen.Example(iface, fn, src)
			require.NoError(t, err)

			schema, err := codegen.JSONSchema(iface, fn, src)
			require.NoError(t, err)
			assert.NoError(t, validate(t, compileSchema(t, schema), string(got)),
				"the example for %s must validate against its schema:\n%s", fn.Name, got)
		})
	}
}

// TestExamplesDocument checks the all-functions document: one key per
// function, values that validate, keys streamed as sorted to a map's
// marshalling so the output is stable.
func TestExamplesDocument(t *testing.T) {
	t.Parallel()

	iface := zkvoteInterface(t)

	got, err := codegen.Examples(iface, src)
	require.NoError(t, err)

	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got, &doc))
	require.Len(t, doc, len(iface.Functions), "one entry per function")

	for _, fn := range iface.Functions {
		entry, ok := doc[fn.Name]
		require.True(t, ok, "the document covers %s", fn.Name)

		schema, err := codegen.JSONSchema(iface, fn, src)
		require.NoError(t, err)
		assert.NoError(t, validate(t, compileSchema(t, schema), string(entry)),
			"%s's entry validates", fn.Name)
	}

	// And the document is stable byte for byte, so it can be diffed.
	again, err := codegen.Examples(iface, src)
	require.NoError(t, err)
	assert.Equal(t, string(got), string(again), "examples are deterministic")
}

// TestExampleRejectsAGenuinelyRecursiveType is the one input no valid example
// exists for: a struct whose field is the struct itself, which the schema of
// requires all fields on every level of nesting.
func TestExampleRejectsAGenuinelyRecursiveType(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{
		Types: model.Types{Structs: []model.Struct{
			{Name: "Loop", Fields: []model.StructField{
				{Name: "next", Type: model.Type{Kind: model.KindUDT, Name: "Loop"}},
			}},
		}},
		Functions: []model.Function{{Name: "do", Inputs: []model.Param{
			{Name: "loop", Type: model.Type{Kind: model.KindUDT, Name: "Loop"}},
		}}},
	}

	fn, ok := iface.Function("do")
	require.True(t, ok)

	_, err := codegen.Example(iface, fn, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains itself")
	assert.Contains(t, err.Error(), "Loop")
}

// TestExampleReportsEmptyUserTypes covers the definitions a real toolchain
// does not usually emit but the ABI model allows: a union or an enum with no
// cases can never be satisfied.
func TestExampleReportsEmptyUserTypes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		iface  *model.Interface
		reason string
	}{
		{
			"union with no cases",
			&model.Interface{
				Types: model.Types{Unions: []model.Union{{Name: "Nothing"}}},
				Functions: []model.Function{{Name: "do", Inputs: []model.Param{
					{Name: "x", Type: model.Type{Kind: model.KindUDT, Name: "Nothing"}},
				}}},
			},
			"has no cases",
		},
		{
			"enum with no cases",
			&model.Interface{
				Types: model.Types{Enums: []model.Enum{{Name: "Nothing"}}},
				Functions: []model.Function{{Name: "do", Inputs: []model.Param{
					{Name: "x", Type: model.Type{Kind: model.KindUDT, Name: "Nothing"}},
				}}},
			},
			"has no cases",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fn, ok := tc.iface.Function("do")
			require.True(t, ok)

			_, err := codegen.Example(tc.iface, fn, src)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

// TestExampleNeedsAnInterface is the same guard the schema generator keeps.
func TestExampleNeedsAnInterface(t *testing.T) {
	t.Parallel()

	_, err := codegen.Example(nil, model.Function{Name: "f"}, src)
	assert.ErrorContains(t, err, "no interface")

	_, err = codegen.Examples(nil, src)
	assert.ErrorContains(t, err, "no interface")
}

// TestExampleArgumentsKeepTheirDeclarationOrder checks that arguments appear
// in the order the signature declares them, which is the order a reader
// compares the example against.
func TestExampleArgumentsKeepTheirDeclarationOrder(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{Functions: []model.Function{{Name: "f", Inputs: []model.Param{
		{Name: "beta", Type: model.Scalar(model.KindU32)},
		{Name: "alpha", Type: model.Scalar(model.KindU64)},
	}}}}

	fn, ok := iface.Function("f")
	require.True(t, ok)

	got, err := codegen.Example(iface, fn, src)
	require.NoError(t, err)

	assert.Equal(t, `{"beta":1,"alpha":"1"}`, string(got),
		"declaration order, not the alphabetical order a map would marshal into")
	assert.True(t, strings.HasPrefix(string(got), "{"), "the document is an object")
}
