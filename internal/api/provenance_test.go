package api_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
)

func newSigner(t *testing.T, fill byte) *attest.Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = fill
	}
	signer, err := attest.NewSigner(seed)
	require.NoError(t, err)
	return signer
}

func TestInterfaceResponsesAreSigned(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	signer := newSigner(t, 1)
	retired := newSigner(t, 2)
	h.api.SetSigner(signer, retired.PublicKey(), signer.PublicKey())

	module := moduleWith(t, "hello")
	h.fake.Deploy(idA, module)
	resp, _ := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	t.Run("a contract interface verifies with the published key", func(t *testing.T) {
		resp, body := h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, signer.KeyID(), resp.Header.Get(attest.KeyIDHeader))

		require.NoError(t, attest.VerifyResponse(signer.PublicKey(), resp.Header.Get(attest.SignatureHeader), body))
		assert.ErrorIs(t, attest.VerifyResponse(retired.PublicKey(), resp.Header.Get(attest.SignatureHeader), body), attest.ErrBadSignature)
	})

	t.Run("a tampered body does not verify", func(t *testing.T) {
		resp, body := h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
		tampered := strings.Replace(string(body), "hello", "steal", 1)
		require.NotEqual(t, string(body), tampered)
		assert.ErrorIs(t, attest.VerifyResponse(signer.PublicKey(), resp.Header.Get(attest.SignatureHeader), []byte(tampered)), attest.ErrBadSignature)
	})

	t.Run("a superseded version is signed for its own hash", func(t *testing.T) {
		h.fake.Upgrade(idA, moduleWith(t, "goodbye"))
		resp, _ := h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")
		require.Equal(t, http.StatusOK, resp.StatusCode)

		old := "?wasm_hash=" + stellartest.HashOf(module)
		resp, body := h.do(t, http.MethodGet, "/api/contracts/"+idA+old, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, attest.VerifyResponse(signer.PublicKey(), resp.Header.Get(attest.SignatureHeader), body))
		assert.Contains(t, string(body), stellartest.HashOf(module))
	})

	t.Run("a catalogued wasm interface is signed without a contract id", func(t *testing.T) {
		hash := h.fake.UploadWasm(moduleWith(t, "proposed"))
		resp, _ := h.do(t, http.MethodPost, "/api/wasm/"+hash, "")
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		resp, body := h.do(t, http.MethodGet, "/api/wasm/"+hash, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, attest.VerifyResponse(signer.PublicKey(), resp.Header.Get(attest.SignatureHeader), body))
		assert.Equal(t, signer.KeyID(), resp.Header.Get(attest.KeyIDHeader))
	})

	t.Run("responses that are not interfaces are not signed", func(t *testing.T) {
		resp, _ := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/versions", "")
		assert.Empty(t, resp.Header.Get(attest.SignatureHeader))
		resp, _ = h.do(t, http.MethodGet, "/api/contracts/"+idB, "")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Empty(t, resp.Header.Get(attest.SignatureHeader))
	})

	t.Run("keys lists the current key first, then retired ones", func(t *testing.T) {
		resp, body := h.do(t, http.MethodGet, "/api/keys", "")
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Keys []attest.Key `json:"keys"`
		}
		require.NoError(t, json.Unmarshal(body, &got))
		require.Len(t, got.Keys, 2, "the signing key is not repeated as a retired key")
		assert.Equal(t, attest.NewKey(signer.PublicKey(), true), got.Keys[0])
		assert.Equal(t, attest.NewKey(retired.PublicKey(), false), got.Keys[1])
		assert.Equal(t, base64.StdEncoding.EncodeToString(signer.PublicKey()), got.Keys[0].PublicKey)
	})
}

func TestUnsignedRegistryPublishesNoKeys(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "hello"))
	h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)

	resp, body := h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
	assert.Empty(t, resp.Header.Get(attest.SignatureHeader))
	assert.Empty(t, resp.Header.Get(attest.KeyIDHeader))
	assert.NotEmpty(t, body)

	resp, body = h.do(t, http.MethodGet, "/api/keys", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"keys":[]}`, string(body))
}

func TestContractReportsItsOrigin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "hello"))
	h.fake.Deploy(idB, moduleWith(t, "world"))
	h.fake.SetOrigin(idA, "GCREATOR", 4242)
	h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idB+`"}`)

	_, body := h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
	got := h.decode(t, body)
	assert.Equal(t, map[string]any{"known": true, "creator": "GCREATOR", "created_ledger": float64(4242)}, got["origin"])
	assert.NotContains(t, got, "verified")

	_, body = h.do(t, http.MethodGet, "/api/contracts/"+idB, "")
	got = h.decode(t, body)
	assert.Equal(t, map[string]any{"known": false}, got["origin"], "an unknown origin is labelled, not left out")

	_, body = h.do(t, http.MethodGet, "/api/contracts", "")
	listed := h.decode(t, body)["contracts"].([]any)
	byID := map[string]map[string]any{}
	for _, c := range listed {
		m := c.(map[string]any)
		byID[m["contract_id"].(string)] = m
	}
	assert.Equal(t, "GCREATOR", byID[idA]["creator"])
	assert.NotContains(t, byID[idB], "creator")
}

func claimBody(t *testing.T, key *keypair.Full, c attest.Claim) string {
	t.Helper()
	signer, sig, err := attest.SignClaim(key.Seed(), c)
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{"claim": c, "signer": signer, "signature": sig})
	require.NoError(t, err)
	return string(raw)
}

func TestVerifyPublisherEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deployer := keypair.MustRandom()
	h.fake.Deploy(idA, moduleWith(t, "hello"))
	h.fake.SetOrigin(idA, deployer.Address(), 100)
	h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)

	fresh := func() attest.Claim {
		return attest.Claim{
			Network: "testnet", ContractID: idA, Name: "Hello Vault",
			SourceURL: "https://github.com/example/hello", IssuedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
	post := func(body string) (*response, map[string]any) {
		resp, raw := h.do(t, http.MethodPost, "/api/contracts/"+idA+"/verify", body)
		return resp, h.decode(t, raw)
	}

	t.Run("a stranger's claim is forbidden", func(t *testing.T) {
		resp, body := post(claimBody(t, keypair.MustRandom(), fresh()))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, body["error"], deployer.Address())
	})

	t.Run("a forged signature is a bad request", func(t *testing.T) {
		c := fresh()
		raw := claimBody(t, deployer, c)
		raw = strings.Replace(raw, "Hello Vault", "Evil Vault", 1)
		resp, body := post(raw)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Contains(t, body["error"], "signature")
	})

	t.Run("a stale claim is unprocessable", func(t *testing.T) {
		c := fresh()
		c.IssuedAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		resp, body := post(claimBody(t, deployer, c))
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		assert.Contains(t, body["error"], "stale")
	})

	t.Run("a claim for another contract than the URL's is refused", func(t *testing.T) {
		c := fresh()
		c.ContractID = idB
		resp, _ := post(claimBody(t, deployer, c))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("unknown fields are refused", func(t *testing.T) {
		resp, _ := post(`{"claim":{},"signer":"x","signature":"y","extra":1}`)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("a missing contract is not found", func(t *testing.T) {
		c := fresh()
		c.ContractID = idB
		resp, _ := h.do(t, http.MethodPost, "/api/contracts/"+idB+"/verify", claimBody(t, deployer, c))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	// None of the refusals above changed anything.
	_, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
	assert.NotContains(t, h.decode(t, raw), "verified")

	t.Run("the deployer's claim verifies and shows up everywhere", func(t *testing.T) {
		resp, body := post(claimBody(t, deployer, fresh()))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, deployer.Address(), body["verified_by"])

		_, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
		got := h.decode(t, raw)
		verified := got["verified"].(map[string]any)
		assert.Equal(t, deployer.Address(), verified["publisher"])
		assert.Equal(t, true, verified["current"])
		assert.Equal(t, "Hello Vault", got["name"])
		assert.Equal(t, "https://github.com/example/hello", got["source_url"])

		_, raw = h.do(t, http.MethodGet, "/api/contracts", "")
		listed := h.decode(t, raw)["contracts"].([]any)[0].(map[string]any)
		assert.Equal(t, deployer.Address(), listed["verified_by"])
	})

	t.Run("an upgrade marks the verification as being for an earlier version", func(t *testing.T) {
		h.fake.Upgrade(idA, moduleWith(t, "hello_v2"))
		h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")

		_, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
		verified := h.decode(t, raw)["verified"].(map[string]any)
		assert.Equal(t, false, verified["current"])
	})
}
