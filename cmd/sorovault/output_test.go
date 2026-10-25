package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/store"
)

func TestPrintInterface(t *testing.T) {
	t.Parallel()

	spec := &store.Spec{
		ContractID: "C123",
		Network:    "testnet",
		WasmHash:   "abc123",
		Interface: &model.Interface{
			Functions: []model.Function{{
				Name: "transfer",
				Inputs: []model.Param{
					{Name: "from", Type: model.Scalar(model.KindAddress)},
					{Name: "amount", Type: model.Scalar(model.KindI128)},
				},
				Outputs: []model.Type{model.Scalar(model.KindBool)},
			}},
			Types: model.Types{
				Structs: []model.Struct{{Name: "Balance", Fields: []model.StructField{{}, {}}}},
				Unions:  []model.Union{{Name: "DataKey", Cases: []model.UnionCase{{}, {}, {}}}},
				Enums:   []model.Enum{{Name: "Status", Cases: []model.EnumCase{{}, {}}}},
				ErrorEnums: []model.ErrorEnum{{
					Name: "Error", Cases: []model.EnumCase{{}},
				}},
			},
			Events: []model.Event{{Name: "Transfer", Params: []model.EventParam{{}, {}}}},
		},
	}

	var got bytes.Buffer
	printInterface(&got, spec)
	assert.Equal(t, "C123  (testnet)\n"+
		"wasm abc123\n\n"+
		"  fn transfer(from: Address, amount: i128) -> bool\n\n"+
		"types:\n"+
		"  struct Balance (2 fields)\n"+
		"  enum   DataKey (3 cases)\n"+
		"  enum   Status (2 cases, u32)\n"+
		"  error  Error (1 cases)\n\n"+
		"events:\n"+
		"  Transfer (2 params)\n", got.String())
}

func TestPrintInterfaceWithoutFunctions(t *testing.T) {
	t.Parallel()

	var got bytes.Buffer
	printInterface(&got, &store.Spec{
		ContractID: "C123",
		Network:    "testnet",
		WasmHash:   "abc123",
		Interface:  &model.Interface{},
	})

	assert.Equal(t, "C123  (testnet)\nwasm abc123\n\nno functions\n", got.String())
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	var got bytes.Buffer
	err := writeJSON(&got, struct {
		ContractID string `json:"contract_id"`
		Changed    bool   `json:"changed"`
	}{ContractID: "C123", Changed: true})

	assert.NoError(t, err)
	assert.Equal(t, "{\n  \"contract_id\": \"C123\",\n  \"changed\": true\n}\n", got.String())
}
