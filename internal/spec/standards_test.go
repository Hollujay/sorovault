package spec_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/spec"
)

func testSEP41Interface() *model.Interface {
	functions := make([]model.Function, 0, len(spec.SEP41.Functions))
	for _, required := range spec.SEP41.Functions {
		inputs := make([]model.Param, 0, len(required.Inputs))
		for i, typ := range required.Inputs {
			inputs = append(inputs, model.Param{Name: string(rune('a' + i)), Type: typ})
		}
		outputs := append([]model.Type(nil), required.Outputs...)
		functions = append(functions, model.Function{Name: required.Name, Inputs: inputs, Outputs: outputs})
	}
	return &model.Interface{ABIVersion: model.ABIVersion, Functions: functions}
}

func TestImplementedStandardsMatchesSEP41AndRejectsNearMiss(t *testing.T) {
	iface := testSEP41Interface()
	require.Equal(t, []string{"sep41"}, spec.ImplementedStandards(iface))

	for i := range iface.Functions {
		if iface.Functions[i].Name == "transfer" {
			iface.Functions[i].Inputs[2].Type = model.Scalar(model.KindU32)
		}
	}
	assert.Empty(t, spec.ImplementedStandards(iface), "a differing argument type must not match SEP-41")
}

func TestImplementedStandardsRequiresEveryFunction(t *testing.T) {
	iface := testSEP41Interface()
	iface.Functions = iface.Functions[:len(iface.Functions)-1]
	assert.Empty(t, spec.ImplementedStandards(iface))
}
