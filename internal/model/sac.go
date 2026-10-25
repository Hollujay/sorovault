package model

// StellarAssetContractIdentifier is the stable interface identifier used for
// protocol-provided Stellar Asset Contracts, which have no uploaded WASM.
const StellarAssetContractIdentifier = "builtin:stellar-asset-contract:v1"

// StellarAssetContractInterface returns the current documented SAC interface.
// Its source is the official Stellar Developer Documentation:
// https://developers.stellar.org/docs/tokens/stellar-asset-contract
func StellarAssetContractInterface() *Interface {
	address := Scalar(KindAddress)
	muxedAddress := Scalar(KindMuxedAddress)
	i128 := Scalar(KindI128)
	u32 := Scalar(KindU32)
	boolean := Scalar(KindBool)
	str := Scalar(KindString)

	param := func(name string, t Type) Param { return Param{Name: name, Type: t} }
	fn := func(name string, inputs []Param, output *Type) Function {
		f := Function{Name: name, Inputs: inputs, Outputs: []Type{}}
		if output != nil {
			f.Outputs = []Type{*output}
		}
		return f
	}
	out := func(t Type) *Type { return &t }

	functions := []Function{
		fn("allowance", []Param{param("from", address), param("spender", address)}, out(i128)),
		fn("approve", []Param{param("from", address), param("spender", address), param("amount", i128), param("live_until_ledger", u32)}, nil),
		fn("balance", []Param{param("id", address)}, out(i128)),
		fn("transfer", []Param{param("from", address), param("to", muxedAddress), param("amount", i128)}, nil),
		fn("transfer_from", []Param{param("spender", address), param("from", address), param("to", address), param("amount", i128)}, nil),
		fn("burn", []Param{param("from", address), param("amount", i128)}, nil),
		fn("burn_from", []Param{param("spender", address), param("from", address), param("amount", i128)}, nil),
		fn("decimals", nil, out(u32)),
		fn("name", nil, out(str)),
		fn("symbol", nil, out(str)),
		fn("set_admin", []Param{param("new_admin", address)}, nil),
		fn("admin", nil, out(address)),
		fn("set_authorized", []Param{param("id", address), param("authorize", boolean)}, nil),
		fn("authorized", []Param{param("id", address)}, out(boolean)),
		fn("mint", []Param{param("to", address), param("amount", i128)}, nil),
		fn("clawback", []Param{param("from", address), param("amount", i128)}, nil),
		fn("trust", []Param{param("addr", address)}, nil),
	}
	return &Interface{
		ABIVersion: ABIVersion,
		Builtin:    true,
		Source:     "https://developers.stellar.org/docs/tokens/stellar-asset-contract",
		Functions:  functions,
		Types:      Types{Structs: []Struct{}, Unions: []Union{}, Enums: []Enum{}, ErrorEnums: []ErrorEnum{}},
		Events:     []Event{},
	}
}
