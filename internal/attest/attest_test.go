package attest_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/attest"
)

var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

func TestCanonicalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{"sorts keys and drops whitespace", `{ "b": 1, "a": [ true, null, "x" ] }`, `{"a":[true,null,"x"],"b":1}`},
		{"sorts nested objects", `{"z":{"b":1,"a":2},"a":{}}`, `{"a":{},"z":{"a":2,"b":1}}`},
		{"escapes only what JSON requires", "{\"k\":\"a\\\"b\\\\c\\n\\t\\u0001\\u001f\"}", "{\"k\":\"a\\\"b\\\\c\\n\\t\\u0001\\u001f\"}"},
		{"leaves HTML and separators alone", `{"k":"<a>& "}`, "{\"k\":\"<a>& \"}"},
		{"writes non-ASCII as UTF-8", `{"k":"héllo 😀"}`, `{"k":"héllo 😀"}`},
		{"orders keys by UTF-16 code unit", "{\"\U0001F600\":1,\"ﬁ\":2}", "{\"\U0001F600\":1,\"ﬁ\":2}"},
		{"keeps integers verbatim", `{"n":-12,"z":0,"big":4294967295}`, `{"big":4294967295,"n":-12,"z":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := attest.Canonicalize([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestCanonicalizeRejects(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]string{
		"fraction":      `{"n":1.5}`,
		"exponent":      `{"n":1e3}`,
		"negative zero": `{"n":-0}`,
		"not json":      `{`,
		"trailing data": `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := attest.Canonicalize([]byte(in))
			assert.Error(t, err)
		})
	}
}

func testSigner(t *testing.T) *attest.Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	signer, err := attest.NewSigner(seed)
	require.NoError(t, err)
	return signer
}

func statement() attest.Statement {
	return attest.Statement{
		Network:    "testnet",
		ContractID: "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
		WasmHash:   "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
		Interface:  json.RawMessage(`{"abi_version":"1","functions":[{"name":"héllo <&>","inputs":[],"outputs":[]}],"events":[]}`),
	}
}

func TestSignVerify(t *testing.T) {
	t.Parallel()

	signer := testSigner(t)
	sig, err := signer.Sign(statement())
	require.NoError(t, err)
	require.NoError(t, attest.Verify(signer.PublicKey(), sig, statement()))

	t.Run("whitespace in the served interface does not matter", func(t *testing.T) {
		st := statement()
		st.Interface = json.RawMessage(` { "events" : [], "functions": [ {"outputs":[],"inputs":[],"name":"héllo <&>"} ], "abi_version":"1" } `)
		assert.NoError(t, attest.Verify(signer.PublicKey(), sig, st))
	})

	t.Run("any changed field fails", func(t *testing.T) {
		for name, mutate := range map[string]func(*attest.Statement){
			"network":     func(s *attest.Statement) { s.Network = "public" },
			"contract":    func(s *attest.Statement) { s.ContractID = "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V" },
			"wasm hash":   func(s *attest.Statement) { s.WasmHash = "00" },
			"interface":   func(s *attest.Statement) { s.Interface = json.RawMessage(`{"abi_version":"2"}`) },
			"no contract": func(s *attest.Statement) { s.ContractID = "" },
		} {
			st := statement()
			mutate(&st)
			assert.ErrorIs(t, attest.Verify(signer.PublicKey(), sig, st), attest.ErrBadSignature, name)
		}
	})

	t.Run("another key fails", func(t *testing.T) {
		other, err := attest.NewSigner(make([]byte, ed25519.SeedSize))
		require.NoError(t, err)
		assert.ErrorIs(t, attest.Verify(other.PublicKey(), sig, statement()), attest.ErrBadSignature)
	})

	t.Run("garbage signature fails", func(t *testing.T) {
		assert.ErrorIs(t, attest.Verify(signer.PublicKey(), "!!!", statement()), attest.ErrBadSignature)
		assert.ErrorIs(t, attest.Verify(signer.PublicKey(), "AAAA", statement()), attest.ErrBadSignature)
	})

	t.Run("an attestation is not a claim", func(t *testing.T) {
		msg, err := statement().Message()
		require.NoError(t, err)
		assert.True(t, len(msg) > len(attest.InterfaceDomain))
		assert.Equal(t, attest.InterfaceDomain, string(msg[:len(attest.InterfaceDomain)]))
		assert.NotEqual(t, attest.InterfaceDomain, attest.ClaimDomain)
	})
}

func TestVerifyResponse(t *testing.T) {
	t.Parallel()

	signer := testSigner(t)
	st := statement()
	sig, err := signer.Sign(st)
	require.NoError(t, err)

	body, err := json.MarshalIndent(map[string]any{
		"contract_id": st.ContractID, "network": st.Network, "wasm_hash": st.WasmHash,
		"first_seen": "2026-01-01T00:00:00Z", "interface": st.Interface,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, attest.VerifyResponse(signer.PublicKey(), sig, body))

	tampered := []byte(`{"contract_id":"` + st.ContractID + `","network":"testnet","wasm_hash":"` + st.WasmHash + `","interface":{"abi_version":"9"}}`)
	assert.ErrorIs(t, attest.VerifyResponse(signer.PublicKey(), sig, tampered), attest.ErrBadSignature)
}

func TestKeyID(t *testing.T) {
	t.Parallel()

	signer := testSigner(t)
	assert.Len(t, signer.KeyID(), 16)
	assert.Equal(t, signer.KeyID(), attest.KeyID(signer.PublicKey()))

	_, err := attest.NewSigner([]byte("short"))
	assert.Error(t, err)
	_, err = attest.ParsePublicKey(base64.StdEncoding.EncodeToString([]byte("short")))
	assert.Error(t, err)
}

func claim() attest.Claim {
	return attest.Claim{
		Network:      "testnet",
		ContractID:   "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
		Name:         "ZK Vote",
		SourceURL:    "https://github.com/example/zkvote",
		SourceCommit: "0123456789abcdef0123456789abcdef01234567",
		IssuedAt:     "2026-08-07T12:00:00Z",
	}
}

func TestClaimSignVerify(t *testing.T) {
	t.Parallel()

	kp := keypair.MustRandom()
	signer, sig, err := attest.SignClaim(kp.Seed(), claim())
	require.NoError(t, err)
	assert.Equal(t, kp.Address(), signer)
	require.NoError(t, attest.VerifyClaim(signer, sig, claim()))

	t.Run("a changed claim fails", func(t *testing.T) {
		c := claim()
		c.SourceURL = "https://github.com/attacker/zkvote"
		assert.ErrorIs(t, attest.VerifyClaim(signer, sig, c), attest.ErrBadSignature)
		c = claim()
		c.IssuedAt = "2026-08-07T12:00:01Z"
		assert.ErrorIs(t, attest.VerifyClaim(signer, sig, c), attest.ErrBadSignature)
	})

	t.Run("another account fails", func(t *testing.T) {
		assert.ErrorIs(t, attest.VerifyClaim(keypair.MustRandom().Address(), sig, claim()), attest.ErrBadSignature)
	})

	t.Run("a bad address or seed is refused", func(t *testing.T) {
		assert.Error(t, attest.VerifyClaim("CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN", sig, claim()))
		_, _, err := attest.SignClaim("not a seed", claim())
		assert.Error(t, err)
	})

	t.Run("optional fields are part of the message", func(t *testing.T) {
		c := claim()
		c.WasmHash = "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781"
		assert.ErrorIs(t, attest.VerifyClaim(signer, sig, c), attest.ErrBadSignature)
	})

	t.Run("a claim is not an attestation", func(t *testing.T) {
		msg, err := claim().Message()
		require.NoError(t, err)
		assert.Equal(t, attest.ClaimDomain, string(msg[:len(attest.ClaimDomain)]))
	})
}

// vectors is the file the TypeScript verifier is tested against. It is
// generated from a fixed key, and the test fails if it drifts from what the
// Go code produces, so the two implementations cannot silently disagree.
type vectors struct {
	Description string       `json:"description"`
	PublicKey   string       `json:"public_key"`
	KeyID       string       `json:"key_id"`
	Cases       []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name string `json:"name"`
	// Body is the response body as served.
	Body json.RawMessage `json:"body"`
	// Canonical is the signed message after the domain line.
	Canonical string `json:"canonical"`
	Signature string `json:"signature"`
	// Valid is false for a case the verifier must reject.
	Valid bool `json:"valid"`
}

func TestVectors(t *testing.T) {
	signer := testSigner(t)

	build := func(name string, body map[string]any, valid bool, sign attest.Statement) vectorCase {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		msg, err := sign.Message()
		require.NoError(t, err)
		sig, err := signer.Sign(sign)
		require.NoError(t, err)
		return vectorCase{
			Name: name, Body: raw, Canonical: string(msg[len(attest.InterfaceDomain):]),
			Signature: sig, Valid: valid,
		}
	}

	id := "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"
	hash := "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781"
	iface := `{"abi_version":"1","meta":{"rsver":"1.89.0"},"functions":[{"name":"héllo <&>  ","doc":"line\nbreak\ttab \"quoted\" \\ slash","inputs":[{"name":"n","type":{"kind":"bytes_n","display":"BytesN<32>","n":32}}],"outputs":[]}],"types":{"structs":[],"unions":[],"enums":[{"name":"E","cases":[{"name":"A","value":4294967295}]}],"error_enums":[]},"events":[]}`

	got := vectors{
		Description: "Detached Ed25519 signatures over sorovault-attestation-v1\\n + canonical JSON. Key seed is bytes 1..32.",
		PublicKey:   base64.StdEncoding.EncodeToString(signer.PublicKey()),
		KeyID:       signer.KeyID(),
		Cases: []vectorCase{
			build("contract interface",
				map[string]any{"contract_id": id, "network": "testnet", "wasm_hash": hash, "name": "ignored extra field", "interface": json.RawMessage(iface)},
				true, attest.Statement{Network: "testnet", ContractID: id, WasmHash: hash, Interface: json.RawMessage(iface)}),
			build("wasm interface has no contract id",
				map[string]any{"network": "public", "wasm_hash": hash, "interface": json.RawMessage(iface)},
				true, attest.Statement{Network: "public", WasmHash: hash, Interface: json.RawMessage(iface)}),
		},
	}
	// A body whose interface was altered after signing.
	tampered := got.Cases[0]
	tampered.Name = "tampered interface"
	tampered.Valid = false
	tampered.Body = json.RawMessage(`{"contract_id":"` + id + `","network":"testnet","wasm_hash":"` + hash + `","interface":{"abi_version":"2"}}`)
	got.Cases = append(got.Cases, tampered)

	const path = "testdata/vectors.json"
	encoded, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	encoded = append(encoded, '\n')

	if *update {
		require.NoError(t, os.WriteFile(path, encoded, 0o644)) //nolint:gosec // test fixture
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run: go test ./internal/attest -update")
	assert.JSONEq(t, string(want), string(encoded), "vectors drifted; regenerate with -update and re-run the TypeScript tests")

	// And the vectors verify (or don't) as they say.
	for _, c := range got.Cases {
		err := attest.VerifyResponse(signer.PublicKey(), c.Signature, c.Body)
		if c.Valid {
			assert.NoError(t, err, c.Name)
		} else {
			assert.ErrorIs(t, err, attest.ErrBadSignature, c.Name)
		}
	}
}
