package wasm_test

import (
	"testing"

	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// FuzzCustomSections ensures the WASM custom-section reader never panics on
// untrusted input. The seeds cover the shapes that reach each branch of the
// reader; the fuzzer mutates the raw module bytes from there.
func FuzzCustomSections(f *testing.F) {
	header := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}

	f.Add([]byte{})                                        // empty module
	f.Add([]byte{0x00, 0x61, 0x73, 0x6D, 0x01})            // short module
	f.Add(header)                                          // only the header
	f.Add(append(append([]byte{}, header...), 0xff))       // trailing garbage
	f.Add(append(append([]byte{}, header...), 0x00, 0x10)) // section length overruns
	f.Add(wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: []byte{0x01}}))
	f.Add(wasmtest.Module(
		wasmtest.Section{Name: wasm.SectionContractSpec, Body: []byte{0x01}},
		wasmtest.Section{Name: wasm.SectionContractMeta, Body: []byte{0x02}},
	))
	f.Add(wasmtest.ModuleWithNonCustom(1, []byte{0x00}, wasmtest.Section{Name: wasm.SectionContractEnvMeta}))
	// A custom section whose name claims more bytes than the section holds.
	f.Add(append(append([]byte{}, header...), 0x00, 0x03, 0x05, 'a', 'b'))

	f.Fuzz(func(t *testing.T, module []byte) {
		// Either result is acceptable; a panic or hang is not.
		_, _ = wasm.CustomSection(module, wasm.SectionContractSpec)
		_, _ = wasm.CustomSectionNames(module)
	})
}
