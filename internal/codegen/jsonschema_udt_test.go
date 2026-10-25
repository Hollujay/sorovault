package codegen_test

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/model"
)

// generatedSchema is the part of a function's schema these tests inspect:
// the argument properties and the definitions they refer to.
type generatedSchema struct {
	Properties map[string]schemaReference `json:"properties"`
	Defs       map[string]json.RawMessage `json:"$defs"`
}

// schemaReference is an argument property that names a definition.
type schemaReference struct {
	Ref string `json:"$ref"`
}

// schemaFor builds the schema of a named function and decodes its
// definitions, so a test can inspect them without re-decoding the whole
// document in every case.
func schemaFor(t *testing.T, iface *model.Interface, name string) ([]byte, generatedSchema) {
	t.Helper()

	fn, ok := iface.Function(name)
	require.True(t, ok, "function %q is not declared", name)

	schema, err := codegen.JSONSchema(iface, fn, src)
	require.NoError(t, err)

	var doc generatedSchema
	require.NoError(t, json.Unmarshal(schema, &doc))
	return schema, doc
}

// TestJSONSchemaUnionDefinition checks the union encoding clients are
// documented to send: a tagged object whose values are null for a unit case
// and a positional array for a case with payloads.
func TestJSONSchemaUnionDefinition(t *testing.T) {
	t.Parallel()

	const address = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

	iface := &model.Interface{
		Types: model.Types{Unions: []model.Union{{Name: "DataKey", Cases: []model.UnionCase{
			{Name: "Admin"},
			{Name: "Balance", Values: []model.Type{
				model.Scalar(model.KindAddress),
				model.Scalar(model.KindI128),
			}},
			{Name: "Pair", Values: []model.Type{{
				Kind: model.KindTuple,
				Elements: []model.Type{
					model.Scalar(model.KindU32),
					model.Scalar(model.KindBool),
				},
			}}},
		}}}},
		Functions: []model.Function{{Name: "store", Inputs: []model.Param{{
			Name: "key",
			Type: model.Type{Kind: model.KindUDT, Name: "DataKey"},
		}}}},
	}

	schema, doc := schemaFor(t, iface, "store")
	assert.Equal(t, "#/$defs/DataKey", doc.Properties["key"].Ref)

	var union struct {
		OneOf []struct {
			Type       string `json:"type"`
			Properties struct {
				Tag struct {
					Const string `json:"const"`
				} `json:"tag"`
				Values json.RawMessage `json:"values"`
			} `json:"properties"`
			Required             []string `json:"required"`
			AdditionalProperties *bool    `json:"additionalProperties"`
		} `json:"oneOf"`
	}
	require.NoError(t, json.Unmarshal(doc.Defs["DataKey"], &union))
	require.Len(t, union.OneOf, 3)

	// Every alternative is a closed object with the same two keys, and the
	// tag is a constant rather than a free string.
	for _, branch := range union.OneOf {
		assert.Equal(t, "object", branch.Type)
		assert.Equal(t, []string{"tag", "values"}, branch.Required)
		require.NotNil(t, branch.AdditionalProperties)
		assert.False(t, *branch.AdditionalProperties)
	}
	assert.Equal(t, "Admin", union.OneOf[0].Properties.Tag.Const)
	assert.Equal(t, "Balance", union.OneOf[1].Properties.Tag.Const)
	assert.Equal(t, "Pair", union.OneOf[2].Properties.Tag.Const)

	// A unit case carries null, not an omitted or empty values field.
	var unitValues struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(union.OneOf[0].Properties.Values, &unitValues))
	assert.Equal(t, "null", unitValues.Type)

	// A payload case carries a fixed-length array, in declaration order.
	var balanceValues struct {
		Type        string            `json:"type"`
		PrefixItems []json.RawMessage `json:"prefixItems"`
		MinItems    int               `json:"minItems"`
		MaxItems    int               `json:"maxItems"`
	}
	require.NoError(t, json.Unmarshal(union.OneOf[1].Properties.Values, &balanceValues))
	assert.Equal(t, "array", balanceValues.Type)
	require.Len(t, balanceValues.PrefixItems, 2)
	assert.Equal(t, 2, balanceValues.MinItems)
	assert.Equal(t, 2, balanceValues.MaxItems)
	assert.JSONEq(t, `{"type":"string","pattern":"^(?:C|G)[A-Z2-7]{55}$"}`, string(balanceValues.PrefixItems[0]))
	assert.JSONEq(t, `{"type":"string","pattern":"^-?[0-9]+$"}`, string(balanceValues.PrefixItems[1]))

	// A tuple payload stays one array element: values is a tuple of the
	// case's arguments, so it is not flattened.
	var pairValues struct {
		PrefixItems []json.RawMessage `json:"prefixItems"`
		MinItems    int               `json:"minItems"`
		MaxItems    int               `json:"maxItems"`
	}
	require.NoError(t, json.Unmarshal(union.OneOf[2].Properties.Values, &pairValues))
	require.Len(t, pairValues.PrefixItems, 1)
	assert.Equal(t, 1, pairValues.MinItems)
	assert.Equal(t, 1, pairValues.MaxItems)
	assert.JSONEq(t, `{
		"type": "array",
		"prefixItems": [
			{"type": "integer", "minimum": 0, "maximum": 4294967295},
			{"type": "boolean"}
		],
		"minItems": 2,
		"maxItems": 2
	}`, string(pairValues.PrefixItems[0]))

	sch := compileSchema(t, schema)
	valid := []struct {
		name     string
		instance string
	}{
		{"a unit case", `{"key":{"tag":"Admin","values":null}}`},
		{"a payload case", `{"key":{"tag":"Balance","values":["` + address + `","-170141183460469231731687303715884105728"]}}`},
		{"a tuple payload", `{"key":{"tag":"Pair","values":[[4294967295,true]]}}`},
		{"a tuple payload of zeroes", `{"key":{"tag":"Pair","values":[[0,false]]}}`},
	}
	for _, tc := range valid {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			assert.NoError(t, validate(t, sch, tc.instance))
		})
	}
	invalid := []struct {
		name     string
		instance string
	}{
		{"a unit case with an empty array", `{"key":{"tag":"Admin","values":[]}}`},
		{"a payload case with one value", `{"key":{"tag":"Balance","values":["` + address + `"]}}`},
		{"a payload case with three values", `{"key":{"tag":"Balance","values":["` + address + `","1","2"]}}`},
		{"an unknown tag", `{"key":{"tag":"Owner","values":null}}`},
		{"a missing values field", `{"key":{"tag":"Admin"}}`},
		{"a missing tag field", `{"key":{"values":null}}`},
		{"an extra field beside tag/values", `{"key":{"tag":"Admin","values":null,"data":1}}`},
		{"a tuple payload that is not an array", `{"key":{"tag":"Pair","values":[4294967295,true]}}`},
		{"a missing argument", `{}`},
	}
	for _, tc := range invalid {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			assert.Error(t, validate(t, sch, tc.instance))
		})
	}
}

// TestJSONSchemaEnumDefinitions checks that an enum and an error enum are
// both integer enums listing the values the interface declares.
func TestJSONSchemaEnumDefinitions(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{
		Types: model.Types{
			Enums: []model.Enum{{Name: "Status", Cases: []model.EnumCase{
				{Name: "Active", Value: 0},
				{Name: "Paused", Value: 2},
				{Name: "Closed", Value: 4294967295},
			}}},
			ErrorEnums: []model.ErrorEnum{{Name: "Error", Cases: []model.EnumCase{
				{Name: "NotInitialized", Value: 2},
				{Name: "Unauthorized", Value: 4},
			}}},
		},
		Functions: []model.Function{
			{Name: "set_status", Inputs: []model.Param{{
				Name: "status",
				Type: model.Type{Kind: model.KindUDT, Name: "Status"},
			}}},
			{Name: "last_error", Inputs: []model.Param{{
				Name: "error",
				Type: model.Type{Kind: model.KindUDT, Name: "Error"},
			}}},
		},
	}

	tests := []struct {
		name    string
		fn      string
		arg     string
		def     string
		want    string
		valid   []string
		invalid []string
	}{
		{
			name:  "enum",
			fn:    "set_status",
			arg:   "status",
			def:   "Status",
			want:  `{"type":"integer","enum":[0,2,4294967295]}`,
			valid: []string{`{"status":0}`, `{"status":2}`, `{"status":4294967295}`},
			invalid: []string{
				`{"status":1}`,
				`{"status":-1}`,
				`{"status":"2"}`,
				`{"status":null}`,
			},
		},
		{
			name:    "error enum",
			fn:      "last_error",
			arg:     "error",
			def:     "Error",
			want:    `{"type":"integer","enum":[2,4]}`,
			valid:   []string{`{"error":2}`, `{"error":4}`},
			invalid: []string{`{"error":1}`, `{"error":3}`, `{"error":"4"}`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			schema, doc := schemaFor(t, iface, tt.fn)
			assert.Equal(t, "#/$defs/"+tt.def, doc.Properties[tt.arg].Ref)
			assert.JSONEq(t, tt.want, string(doc.Defs[tt.def]))

			sch := compileSchema(t, schema)
			for _, instance := range tt.valid {
				assert.NoError(t, validate(t, sch, instance), instance)
			}
			for _, instance := range tt.invalid {
				assert.Error(t, validate(t, sch, instance), instance)
			}
		})
	}
}

// TestJSONSchemaNestedDefinitions checks that a user-defined type used by
// another is a $ref to a single definition rather than an inlined copy, and
// that a type which refers to itself, directly or through another type,
// terminates.
func TestJSONSchemaNestedDefinitions(t *testing.T) {
	t.Parallel()

	const address = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

	udt := func(name string) model.Type { return model.Type{Kind: model.KindUDT, Name: name} }
	option := func(inner model.Type) model.Type {
		return model.Type{Kind: model.KindOption, Inner: ptr(inner)}
	}

	iface := &model.Interface{
		Types: model.Types{Structs: []model.Struct{
			{Name: "Config", Fields: []model.StructField{
				{Name: "name", Type: model.Scalar(model.KindString)},
				{Name: "owner", Type: model.Scalar(model.KindAddress)},
			}},
			{Name: "Market", Fields: []model.StructField{
				{Name: "id", Type: model.Scalar(model.KindU32)},
				{Name: "config", Type: option(udt("Config"))},
			}},
			{Name: "Node", Fields: []model.StructField{
				{Name: "value", Type: model.Scalar(model.KindI32)},
				{Name: "next", Type: option(udt("Node"))},
			}},
			{Name: "Left", Fields: []model.StructField{
				{Name: "right", Type: option(udt("Right"))},
			}},
			{Name: "Right", Fields: []model.StructField{
				{Name: "left", Type: option(udt("Left"))},
			}},
		}},
		Functions: []model.Function{{Name: "inspect", Inputs: []model.Param{
			{Name: "market", Type: udt("Market")},
			{Name: "node", Type: option(udt("Node"))},
			{Name: "left", Type: option(udt("Left"))},
		}}},
	}

	schema, doc := schemaFor(t, iface, "inspect")
	assert.Equal(t, "#/$defs/Market", doc.Properties["market"].Ref)

	defs := make([]string, 0, len(doc.Defs))
	for name := range doc.Defs {
		defs = append(defs, name)
	}
	sort.Strings(defs)
	assert.Equal(t, []string{"Config", "Left", "Market", "Node", "Right"}, defs,
		"every type is defined once, and no others are invented")

	// The option's second alternative is a reference, not a copy of Config.
	var market struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			OneOf []json.RawMessage `json:"oneOf"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(doc.Defs["Market"], &market))
	assert.Equal(t, "object", market.Type)
	require.Len(t, market.Properties["config"].OneOf, 2)
	assert.JSONEq(t, `{"type":"null"}`, string(market.Properties["config"].OneOf[0]))
	assert.JSONEq(t, `{"$ref":"#/$defs/Config"}`, string(market.Properties["config"].OneOf[1]))
	assert.NotContains(t, string(doc.Defs["Market"]), `"owner"`,
		"the nested struct must be referenced, not inlined")

	// A self-referential type points back at its own definition.
	var node struct {
		Properties map[string]struct {
			OneOf []json.RawMessage `json:"oneOf"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(doc.Defs["Node"], &node))
	require.Len(t, node.Properties["next"].OneOf, 2)
	assert.JSONEq(t, `{"$ref":"#/$defs/Node"}`, string(node.Properties["next"].OneOf[1]))

	// Two types that refer to each other stop at the definitions too.
	for _, tc := range []struct {
		name  string
		field string
		want  string
	}{
		{"Left", "right", "#/$defs/Right"},
		{"Right", "left", "#/$defs/Left"},
	} {
		var def struct {
			Properties map[string]struct {
				OneOf []json.RawMessage `json:"oneOf"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(doc.Defs[tc.name], &def))
		require.Len(t, def.Properties[tc.field].OneOf, 2)
		assert.JSONEq(t, `{"$ref":"`+tc.want+`"}`,
			string(def.Properties[tc.field].OneOf[1]))
	}

	sch := compileSchema(t, schema)
	valid := []struct {
		name     string
		instance string
	}{
		{"nested values", `{
			"market": {"id": 7, "config": {"name": "soro", "owner": "` + address + `"}},
			"node": {"value": -1, "next": {"value": 2, "next": null}},
			"left": {"right": {"left": {"right": null}}}
		}`},
		{"null options", `{"market": {"id": 0, "config": null}, "node": null, "left": null}`},
	}
	for _, tc := range valid {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			assert.NoError(t, validate(t, sch, tc.instance))
		})
	}

	invalid := []struct {
		name     string
		instance string
	}{
		{"a nested struct missing a required field", `{
			"market": {"id": 7, "config": {"name": "soro"}},
			"node": null, "left": null
		}`},
		{"a nested struct with an extra field", `{
			"market": {"id": 7, "config": {"name": "soro", "owner": "` + address + `", "symbol": "x"}},
			"node": null, "left": null
		}`},
		{"a recursive field of the wrong type", `{
			"market": {"id": 7, "config": null},
			"node": {"value": 1, "next": 5},
			"left": null
		}`},
		{"a mutually recursive field of the wrong type", `{
			"market": {"id": 7, "config": null},
			"node": null,
			"left": {"right": {"left": 5}}
		}`},
	}
	for _, tc := range invalid {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			assert.Error(t, validate(t, sch, tc.instance))
		})
	}
}

// TestJSONSchemaUndefinedUDT checks the fallback for a type the interface
// names but does not define: the reference resolves to a schema that accepts
// anything, rather than to a broken or missing definition.
func TestJSONSchemaUndefinedUDT(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{Functions: []model.Function{{Name: "use", Inputs: []model.Param{{
		Name: "value",
		Type: model.Type{Kind: model.KindUDT, Name: "External"},
	}}}}}

	schema, doc := schemaFor(t, iface, "use")
	assert.Equal(t, "#/$defs/External", doc.Properties["value"].Ref)
	assert.JSONEq(t, `{}`, string(doc.Defs["External"]))

	sch := compileSchema(t, schema)
	assert.NoError(t, validate(t, sch, `{"value":{"nested":true}}`))
}
