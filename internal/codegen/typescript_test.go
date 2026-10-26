package codegen_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/model"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func ptr(t model.Type) *model.Type { return &t }

var src = codegen.Source{
	ContractID: "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
	Network:    "testnet",
	WasmHash:   "abc123",
}

// TestDeprecationHeader proves a deprecation mark reaches the generated
// file as a tool-readable note in the header, and that the variants — a
// note without a successor, a successor without a note, neither — stay
// readable instead of printing empty punctuation.
func TestDeprecationHeader(t *testing.T) {
	t.Parallel()

	newSource := func(deprecation *codegen.Deprecation) codegen.Source {
		s := src
		s.Deprecation = deprecation
		return s
	}

	generate := func(t *testing.T, s codegen.Source) string {
		t.Helper()
		iface := &model.Interface{
			ABIVersion: model.ABIVersion,
			Types:      model.Types{ErrorEnums: []model.ErrorEnum{}},
			Events:     []model.Event{},
		}
		got, err := codegen.TypeScript(iface, s)
		require.NoError(t, err)
		return got
	}

	got := generate(t, newSource(&codegen.Deprecation{
		Since:     "2026-10-26T09:31:02Z",
		Successor: "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V",
		Note:      "Move voting to the replacement.",
	}))
	assert.Contains(t, got, "//\n// @deprecated since 2026-10-26T09:31:02Z\n")
	assert.Contains(t, got, "// Move voting to the replacement. Use contract CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V instead.\n")

	got = generate(t, newSource(&codegen.Deprecation{Since: "2026-10-26T09:31:02Z"}))
	assert.Contains(t, got, "// @deprecated since 2026-10-26T09:31:02Z\n")
	assert.NotContains(t, got, "Use contract")

	got = generate(t, newSource(nil))
	assert.NotContains(t, got, "@deprecated",
		"an active contract generates no deprecation remark")
}

// TestGoldenZKVote generates a client for the real testnet contract whose
// decoded ABI the spec package pins, and compares it with a checked-in file.
// CI type-checks that file against @stellar/stellar-sdk, so the golden is
// known to compile, not just to be stable.
func TestGoldenZKVote(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../spec/testdata/zkvote.golden.json")
	require.NoError(t, err)
	var iface model.Interface
	require.NoError(t, json.Unmarshal(raw, &iface))

	got, err := codegen.TypeScript(&iface, src)
	require.NoError(t, err)

	golden := filepath.Join("testdata", "zkvote.client.ts")
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		t.Logf("wrote %s", golden)
	}
	want, err := os.ReadFile(golden) //nolint:gosec // test-owned golden path
	require.NoError(t, err, "golden file missing; regenerate with -update")
	assert.Equal(t, string(want), got, "generated client changed; if intended, rerun with -update")
}

// TestZodFlavour proves the only difference between the plain client and
// the schema-carrying one is what runtime validation needs: the zod import
// in the header, the schemas section at the end, and the staleness guard
// both flavours share.
func TestZodFlavour(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../spec/testdata/zkvote.golden.json")
	require.NoError(t, err)
	var iface model.Interface
	require.NoError(t, json.Unmarshal(raw, &iface))

	plain, err := codegen.TypeScript(&iface, src)
	require.NoError(t, err)
	zodSrc := src
	zodSrc.WithZod = true
	got, err := codegen.TypeScript(&iface, zodSrc)
	require.NoError(t, err)

	assert.Contains(t, got, `import { z } from "zod";`)
	assert.NotContains(t, plain, `import { z } from "zod";`, "the plain flavour stays zod-free")
	assert.Contains(t, got, "export const ProposalSchema = z.object({")
	assert.Contains(t, got, "vote_params: z.lazy(() => VoteParamsSchema),", "cross-references are deferred so recursive interfaces stay legal")
	assert.Contains(t, got, "export const voteArgsSchema = z.object({")
	assert.NotContains(t, got, "ErrorSchema", "an error enum is its numeric code, not a schema")
	assert.Contains(t, got, "export function assertSameWasm(liveHash: string): boolean {", "both flavours carry the staleness guard")
}

// TestZodMappings pins the schema expression for every scalar kind, so the
// widths cannot silently drift from the TypeScript types the same
// generator emits.
func TestZodMappings(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind model.Type
		want string
	}{
		{model.Type{Kind: model.KindU32}, "z.number().int().min(0).max(4294967295)"},
		{model.Type{Kind: model.KindI32}, "z.number().int().min(-2147483648).max(2147483647)"},
		{model.Type{Kind: model.KindU64}, "z.bigint().min(0n).max((1n << 64n) - 1n)"},
		{model.Type{Kind: model.KindI64}, "z.bigint().min(-(1n << 63n)).max((1n << 63n) - 1n)"},
		{model.Type{Kind: model.KindU128}, "z.bigint().min(0n).max((1n << 128n) - 1n)"},
		{model.Type{Kind: model.KindI256}, "z.bigint().min(-(1n << 255n)).max((1n << 255n) - 1n)"},
		{model.Type{Kind: model.KindTimepoint}, "z.bigint().min(0n).max((1n << 64n) - 1n)"},
		{model.Type{Kind: model.KindBytesN, N: 32}, "z.custom<Buffer>((v) => Buffer.isBuffer(v) && v.length === 32)"},
		{model.Type{Kind: model.KindBytes}, "z.custom<Buffer>((v) => Buffer.isBuffer(v))"},
		{model.Type{Kind: model.KindAddress}, "z.string().regex(/^[CG][A-Z2-7]{55}$/, \"not a Stellar address\")"},
		{model.Type{Kind: model.KindMuxedAddress}, "z.string().regex(/^[CM][A-Z2-7]{55}$/, \"not a Stellar muxed address\")"},
	} {
		got, err := codegen.TypeScript(&model.Interface{
			Functions: []model.Function{{
				Name:   "probe",
				Inputs: []model.Param{{Name: "v", Type: tc.kind}},
			}},
		}, codegen.Source{WithZod: true})
		require.NoError(t, err)
		assert.Contains(t, got, "v: "+tc.want+",", tc.kind.Display)
	}
}

// TestZodShapes pins the section's decisions beyond plain objects: how a
// union, an enum and a recursive reference render, and which name yields
// when a function and a type want the same schema constant.
func TestZodShapes(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{
		Types: model.Types{
			Structs: []model.Struct{
				{Name: "voteArgs", Fields: []model.StructField{{Name: "n", Type: model.Type{Kind: model.KindU32}}}},
				{
					Name:   "forest",
					Fields: []model.StructField{{Name: "children", Type: model.Type{Kind: model.KindVec, Element: ptr(model.Type{Kind: model.KindUDT, Name: "forest"})}}},
				},
			},
			Unions: []model.Union{
				{
					Name: "shape",
					Cases: []model.UnionCase{
						{Name: "unit"},
						{Name: "sized", Values: []model.Type{{Kind: model.KindU32}}},
					},
				},
				{Name: "empty"},
			},
			Enums: []model.Enum{
				{Name: "phase", Cases: []model.EnumCase{{Name: "open", Value: 1}, {Name: "closed", Value: 2}}},
			},
			ErrorEnums: []model.ErrorEnum{
				{Name: "fault", Cases: []model.EnumCase{{Name: "no", Value: 1}}},
			},
		},
		Functions: []model.Function{{Name: "vote", Inputs: []model.Param{{Name: "x", Type: model.Type{Kind: model.KindU32}}}}},
	}
	got, err := codegen.TypeScript(iface, codegen.Source{WithZod: true})
	require.NoError(t, err)

	assert.Contains(t, got, "export const phaseSchema = z.nativeEnum(phase);")
	assert.Contains(t, got, "export const shapeSchema = z.union([")
	assert.Contains(t, got, `  z.object({ tag: z.literal("unit"), values: z.undefined() }),`)
	assert.Contains(t, got, `  z.object({ tag: z.literal("sized"), values: z.tuple([z.number().int().min(0).max(4294967295)]) }),`)
	assert.Contains(t, got, "export const emptySchema = z.never();")
	// A recursive interface keeps working: the self-reference is deferred
	// to validation time.
	assert.Contains(t, got, "children: z.array(z.lazy(() => forestSchema)),")
	// The struct claimed voteArgsSchema first, so the function's constant
	// yields — the same convention the type declarations use.
	assert.Contains(t, got, "export const voteArgsSchema_ = z.object({")
	assert.NotContains(t, got, "faultSchema", "an error enum is its numeric code, not a schema")
}

// TestGoldenPackage generates the npm package built around the golden client
// and compares it with the checked-in files. CI runs npm install and npm run
// build in that directory, so the package is known to build from scratch,
// not merely to be stable.
func TestGoldenPackage(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../spec/testdata/zkvote.golden.json")
	require.NoError(t, err)
	var iface model.Interface
	require.NoError(t, json.Unmarshal(raw, &iface))

	files, err := codegen.Package(&iface, src, codegen.PackageOptions{Name: "zkvote-client", WithZod: true})
	require.NoError(t, err)

	dir := filepath.Join("testdata", "zkvote-pkg")
	if *update {
		for _, file := range files {
			path := filepath.Join(dir, filepath.FromSlash(file.Path))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
			require.NoError(t, os.WriteFile(path, file.Data, 0o600))
		}
	}

	for _, file := range files {
		want, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.Path))) //nolint:gosec // paths come from the generator, not the caller
		require.NoErrorf(t, err, "%s is missing; regenerate with -update", file.Path)
		assert.Equal(t, string(want), string(file.Data),
			"%s changed; if intended, rerun with -update", file.Path)
	}
}

func TestTypeMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   model.Type
		want string
	}{
		{model.Scalar(model.KindU32), "sdk.u32"},
		{model.Scalar(model.KindI128), "sdk.i128"},
		{model.Scalar(model.KindTimepoint), "sdk.Timepoint"},
		{model.Scalar(model.KindBool), "boolean"},
		{model.Scalar(model.KindAddress), "string"},
		{model.Scalar(model.KindSymbol), "string"},
		{model.Scalar(model.KindBytes), "Buffer"},
		{model.Type{Kind: model.KindBytesN, N: 32}, "Buffer"},
		{model.Type{Kind: model.KindOption, Inner: ptr(model.Scalar(model.KindU64))}, "sdk.Option<sdk.u64>"},
		{model.Type{Kind: model.KindVec, Element: ptr(model.Scalar(model.KindString))}, "Array<string>"},
		{model.Type{Kind: model.KindMap, Key: ptr(model.Scalar(model.KindSymbol)), Value: ptr(model.Scalar(model.KindI128))}, "Map<string, sdk.i128>"},
		{model.Type{Kind: model.KindTuple, Elements: []model.Type{model.Scalar(model.KindU32), model.Scalar(model.KindBool)}}, "readonly [sdk.u32, boolean]"},
		{model.Type{Kind: model.KindResult, Ok: ptr(model.Scalar(model.KindVoid)), Error: ptr(model.Type{Kind: model.KindUDT, Name: "Error"})}, "sdk.Result<void>"},
		{model.Type{Kind: model.KindUDT, Name: "Proposal"}, "Proposal"},
		{model.Type{Kind: model.KindUDT, Name: "Map"}, "Map_"},
	}

	for _, tt := range tests {
		iface := &model.Interface{Functions: []model.Function{{Name: "f", Outputs: []model.Type{tt.in}}}}
		got, err := codegen.TypeScript(iface, src)
		require.NoError(t, err, tt.want)
		assert.Contains(t, got, "f(options?: sdk.MethodOptions): Promise<sdk.AssembledTransaction<"+tt.want+">>;", tt.want)
	}
}

func TestErrorEnumReferenceIsItsCode(t *testing.T) {
	t.Parallel()
	iface := &model.Interface{
		Types: model.Types{ErrorEnums: []model.ErrorEnum{{Name: "Error", Cases: []model.EnumCase{{Name: "NotFound", Value: 3}}}}},
		Functions: []model.Function{{Name: "last_error",
			Outputs: []model.Type{{Kind: model.KindUDT, Name: "Error"}}}},
	}
	got, err := codegen.TypeScript(iface, src)
	require.NoError(t, err)
	assert.Contains(t, got, "Promise<sdk.AssembledTransaction<sdk.u32>>")
	assert.Contains(t, got, `3: { message: "NotFound" },`)
}

func TestDeclarations(t *testing.T) {
	t.Parallel()
	iface := &model.Interface{
		Types: model.Types{
			Structs: []model.Struct{
				{Name: "Point", IsTuple: true, Fields: []model.StructField{
					{Name: "0", Type: model.Scalar(model.KindI32)}, {Name: "1", Type: model.Scalar(model.KindI32)}}},
				{Name: "Config", Doc: "Ends early? */ no", Fields: []model.StructField{
					{Name: "admin", Type: model.Scalar(model.KindAddress)},
					{Name: "fee-bps", Type: model.Scalar(model.KindU32)}}},
			},
			Unions: []model.Union{{Name: "DataKey", Cases: []model.UnionCase{
				{Name: "Admin"},
				{Name: "Balance", Values: []model.Type{model.Scalar(model.KindAddress)}}}}},
			Enums: []model.Enum{{Name: "Status", Cases: []model.EnumCase{{Name: "Active", Value: 0}, {Name: "Closed", Value: 2}}}},
		},
		Functions: []model.Function{{Name: "transfer", Inputs: []model.Param{
			{Name: "from", Type: model.Scalar(model.KindAddress)},
			{Name: "amount", Type: model.Scalar(model.KindI128)}}}},
	}

	got, err := codegen.TypeScript(iface, src)
	require.NoError(t, err)

	for _, want := range []string{
		"export type Point = readonly [sdk.i32, sdk.i32];",
		"export interface Config {\n  admin: string;\n  \"fee-bps\": sdk.u32;\n}",
		"Ends early? *\\/ no",
		"export type DataKey =\n  | { tag: \"Admin\"; values: void }\n  | { tag: \"Balance\"; values: readonly [string] };",
		"export enum Status {\n  Active = 0,\n  Closed = 2,\n}",
		"transfer(args: { from: string; amount: sdk.i128 }, options?: sdk.MethodOptions): Promise<sdk.AssembledTransaction<null>>;",
		`export const contractId = "` + src.ContractID + `";`,
	} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "*/ no", "a doc string must not close its comment")
}

func TestRejectsConflictingErrorCodes(t *testing.T) {
	t.Parallel()
	iface := &model.Interface{Types: model.Types{ErrorEnums: []model.ErrorEnum{
		{Name: "A", Cases: []model.EnumCase{{Name: "Busy", Value: 1}}},
		{Name: "B", Cases: []model.EnumCase{{Name: "Gone", Value: 1}}},
	}}}
	_, err := codegen.TypeScript(iface, src)
	assert.ErrorContains(t, err, "error code 1")
}

func TestQuotesInvalidFunctionName(t *testing.T) {
	t.Parallel()
	iface := &model.Interface{Functions: []model.Function{{Name: "not-valid"}}}
	got, err := codegen.TypeScript(iface, src)
	require.NoError(t, err)
	assert.Contains(t, got, `  "not-valid"(options?: sdk.MethodOptions):`)
}

func FuzzTypeScript(f *testing.F) {
	f.Add("Config", "fee-bps", "doc with */ terminator", []byte{0, 1, 2})
	f.Add("class", "quoted\"name", "multiline\ndoc", []byte{3, 4, 5})
	f.Add("bad-name", "", "", []byte{})

	f.Fuzz(func(t *testing.T, name, fieldName, doc string, shapes []byte) {
		types := []model.Type{
			model.Scalar(model.KindBool),
			model.Scalar(model.KindU32),
			model.Scalar(model.KindString),
			{Kind: model.KindOption, Inner: ptr(model.Scalar(model.KindI64))},
			{Kind: model.KindVec, Element: ptr(model.Scalar(model.KindAddress))},
			{Kind: model.KindMap, Key: ptr(model.Scalar(model.KindSymbol)), Value: ptr(model.Scalar(model.KindU128))},
			{Kind: model.KindTuple, Elements: []model.Type{model.Scalar(model.KindBool), model.Scalar(model.KindBytes)}},
		}
		shapeAt := func(index int) model.Type {
			if len(shapes) == 0 {
				return types[0]
			}
			return types[int(shapes[index%len(shapes)])%len(types)]
		}

		iface := &model.Interface{
			Types: model.Types{Structs: []model.Struct{{Name: name, Doc: doc, Fields: []model.StructField{{
				Name: fieldName, Doc: doc, Type: shapeAt(0),
			}}}}},
			Functions: []model.Function{{Name: name, Doc: doc, Inputs: []model.Param{{
				Name: fieldName, Type: shapeAt(1),
			}}}},
		}

		got, err := codegen.TypeScript(iface, src)
		require.NoError(t, err)

		declaredNames := regexp.MustCompile(`(?m)^export (?:interface|type|enum) ([^\s{=]+)`)
		for _, match := range declaredNames.FindAllStringSubmatch(got, -1) {
			assert.Regexp(t, `^[A-Za-z_$][A-Za-z0-9_$]*$`, match[1])
		}

		inDoc := false
		for _, line := range strings.Split(got, "\n") {
			if strings.Contains(line, "/**") {
				inDoc = true
				continue
			}
			if inDoc {
				if strings.TrimSpace(line) == "*/" {
					inDoc = false
					continue
				}
				assert.NotContains(t, line, "*/")
			}
		}
	})
}

func TestOutputIsDeterministic(t *testing.T) {
	t.Parallel()
	iface := &model.Interface{Types: model.Types{Structs: []model.Struct{{Name: "Zed"}, {Name: "Alpha"}}}}
	a, err := codegen.TypeScript(iface, src)
	require.NoError(t, err)
	assert.Less(t, strings.Index(a, "interface Alpha"), strings.Index(a, "interface Zed"))
}

func TestMarkdownDocumentsInterface(t *testing.T) {
	t.Parallel()
	iface := &model.Interface{
		Functions: []model.Function{{
			Name:    "transfer",
			Doc:     "Move tokens to an address.",
			Inputs:  []model.Param{{Name: "to", Doc: "Recipient.", Type: model.Scalar(model.KindAddress)}},
			Outputs: []model.Type{model.Scalar(model.KindBool)},
		}},
		Types: model.Types{
			Structs:    []model.Struct{{Name: "Config", Doc: "Contract settings.", Fields: []model.StructField{{Name: "fee", Doc: "Fee in bps.", Type: model.Scalar(model.KindU32)}}}},
			Unions:     []model.Union{{Name: "Action", Doc: "Supported actions.", Cases: []model.UnionCase{{Name: "Pause", Doc: "Pause transfers."}}}},
			Enums:      []model.Enum{{Name: "Status", Doc: "Current status.", Cases: []model.EnumCase{{Name: "Active", Value: 1}}}},
			ErrorEnums: []model.ErrorEnum{{Name: "ContractError", Cases: []model.EnumCase{{Name: "Unauthorized", Value: 4}}}},
		},
		Events: []model.Event{{Name: "Transfer", Doc: "Emitted after a transfer.", Params: []model.EventParam{{Name: "to", Type: model.Scalar(model.KindAddress), Location: "topic-list"}}, DataFormat: "single-value"}},
	}

	got, err := codegen.Markdown(iface, src)
	require.NoError(t, err)
	for _, want := range []string{
		"# Contract `" + src.ContractID + "`",
		"WASM hash: `" + src.WasmHash + "`",
		"### `fn transfer(to: Address) -> bool`",
		"Move tokens to an address.",
		"Recipient.",
		"## Types",
		"### `struct Config`",
		"Fee in bps.",
		"### `enum Action`",
		"Pause transfers.",
		"### `enum Status`",
		"### `error ContractError`",
		"## Events",
		"### `Transfer`",
		"Emitted after a transfer.",
	} {
		assert.Contains(t, got, want)
	}
}

func TestTypeScriptParameterDocumentation(t *testing.T) {
	t.Parallel()
	iface := &model.Interface{
		Types: model.Types{Structs: []model.Struct{{
			Name: "Config",
			Fields: []model.StructField{{
				Name: "threshold", Doc: "Minimum required */ threshold.", Type: model.Scalar(model.KindU32),
			}},
		}}},
		Functions: []model.Function{
			{Name: "transfer", Doc: "Transfer tokens.", Inputs: []model.Param{
				{Name: "to", Doc: "Recipient address.", Type: model.Scalar(model.KindAddress)},
				{Name: "amount", Doc: "Amount in stroops. */ Keep exact.\nSecond line.", Type: model.Scalar(model.KindI128)},
				{Name: "memo", Type: model.Scalar(model.KindString)},
			}},
			{Name: "set_threshold", Inputs: []model.Param{
				{Name: "threshold", Doc: "Minimum required.", Type: model.Scalar(model.KindU32)},
			}},
		},
	}
	got, err := codegen.TypeScript(iface, src)
	require.NoError(t, err)
	for _, want := range []string{
		" * Transfer tokens.",
		" * @param args.to Recipient address.",
		" * @param args.amount Amount in stroops. *\\/ Keep exact.",
		" * Second line.",
		" * @param args.threshold Minimum required.",
		"  /**\n   * Minimum required *\\/ threshold.\n   */\n  threshold: sdk.u32;",
	} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "@param args.memo", "undocumented parameters should not emit tags")
	assert.NotContains(t, got, " * @param args.amount Amount in stroops. */", "parameter docs must escape comment terminators")
}
