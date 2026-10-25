package spec

import "github.com/soroworks/sorovault/internal/model"

// StandardSEP41Version pins the SEP-41 version used for classification.
const StandardSEP41Version = "0.5.2"

// Standard describes a versioned contract interface. Parameter names and
// documentation are not part of a function signature; names, ordered
// argument types and return types are.
type Standard struct {
	ID        string
	Version   string
	Functions []FunctionSignature
}

// FunctionSignature is the callable surface required by a standard.
type FunctionSignature struct {
	Name    string
	Inputs  []model.Type
	Outputs []model.Type
}

func sep41Standard() Standard {
	address := model.Scalar(model.KindAddress)
	i128 := model.Scalar(model.KindI128)
	u32 := model.Scalar(model.KindU32)
	stringType := model.Scalar(model.KindString)
	return Standard{
		ID: "sep41", Version: StandardSEP41Version,
		Functions: []FunctionSignature{
			{Name: "allowance", Inputs: []model.Type{address, address}, Outputs: []model.Type{i128}},
			{Name: "approve", Inputs: []model.Type{address, address, i128, u32}},
			{Name: "balance", Inputs: []model.Type{address}, Outputs: []model.Type{i128}},
			{Name: "transfer", Inputs: []model.Type{address, address, i128}},
			{Name: "transfer_from", Inputs: []model.Type{address, address, address, i128}},
			{Name: "burn", Inputs: []model.Type{address, i128}},
			{Name: "burn_from", Inputs: []model.Type{address, address, i128}},
			{Name: "decimals", Outputs: []model.Type{u32}},
			{Name: "name", Outputs: []model.Type{stringType}},
			{Name: "symbol", Outputs: []model.Type{stringType}},
		},
	}
}

// SEP41 is the SEP-41 Soroban Token Interface definition, pinned to draft
// version 0.5.2 from stellar/stellar-protocol/ecosystem/sep-0041.md.
var SEP41 = sep41Standard()

var standardDefinitions = []Standard{SEP41}

// ImplementedStandards returns the IDs of standards whose required function
// signatures are all present in iface. Function names alone are insufficient.
func ImplementedStandards(iface *model.Interface) []string {
	implemented := make([]string, 0, len(standardDefinitions))
	if iface == nil {
		return implemented
	}
	for _, standard := range standardDefinitions {
		if implementsStandard(iface, standard) {
			implemented = append(implemented, standard.ID)
		}
	}
	return implemented
}

func implementsStandard(iface *model.Interface, standard Standard) bool {
	for _, required := range standard.Functions {
		found := false
		for _, actual := range iface.Functions {
			if matchesFunction(actual, required) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchesFunction(actual model.Function, required FunctionSignature) bool {
	if actual.Name != required.Name || len(actual.Inputs) != len(required.Inputs) || len(actual.Outputs) != len(required.Outputs) {
		return false
	}
	for i := range required.Inputs {
		if !sameType(actual.Inputs[i].Type, required.Inputs[i]) {
			return false
		}
	}
	for i := range required.Outputs {
		if !sameType(actual.Outputs[i], required.Outputs[i]) {
			return false
		}
	}
	return true
}

func sameType(a, b model.Type) bool {
	return a.Kind == b.Kind && model.Render(a) == model.Render(b)
}
