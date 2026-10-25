package spec_test

import (
	"testing"

	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// FuzzDecodeSpec ensures the spec decoder never panics on untrusted input.
// The fuzzer mutates the body of a module's contractspecv0 section, which is
// the part of an uploaded contract the decoder parses as XDR.
func FuzzDecodeSpec(f *testing.F) {
	f.Add([]byte{})                                               // empty spec section
	f.Add([]byte{0x00, 0x01, 0x02, 0x03})                         // too short for an entry
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})                         // unknown entry kind
	f.Add([]byte{0x00, 0x00, 0x00, 0x00})                         // function kind, then nothing
	f.Add(make([]byte, 10000))                                    // a large run of zeroes
	f.Add([]byte{0x00, 0x00, 0x00, 0x00, 0x7F, 0xFF, 0xFF, 0xFF}) // huge doc length

	f.Fuzz(func(t *testing.T, body []byte) {
		module := wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: body})

		// Either result is acceptable; a panic or hang is not.
		_, _ = spec.XDRDecoder{}.Decode(module)
		_, _ = spec.ReadEntries(body)
	})
}
