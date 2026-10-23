package attest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Domain separators are the first line of every signed message, so a
// signature made for one purpose can never be replayed as another.
const (
	// InterfaceDomain prefixes the message of an interface attestation.
	InterfaceDomain = "sorovault-attestation-v1\n"
	// ClaimDomain prefixes the message of a publisher claim.
	ClaimDomain = "sorovault-claim-v1\n"
)

// Response headers carrying an attestation.
const (
	SignatureHeader = "X-SoroVault-Attestation"
	KeyIDHeader     = "X-SoroVault-Key-Id"
)

// ErrBadSignature reports a signature that does not verify.
var ErrBadSignature = errors.New("attest: signature does not verify")

// Statement is what an interface attestation vouches for: that this
// registry decoded this interface from this WASM, for this contract on this
// network.
//
// The statement is built from the response body a client already has, so a
// client rebuilds it with no further requests. ContractID is empty for an
// interface fetched by WASM hash, and is then left out of the encoding.
type Statement struct {
	Network    string
	ContractID string
	WasmHash   string
	// Interface is the decoded interface exactly as served.
	Interface json.RawMessage
}

// Message returns the bytes that are signed: the domain separator followed
// by the canonical encoding of
//
//	{"contract_id":…,"interface":…,"network":…,"v":1,"wasm_hash":…}
func (s Statement) Message() ([]byte, error) {
	if len(s.Interface) == 0 {
		return nil, errors.New("attest: statement has no interface")
	}
	doc := map[string]any{
		"v":         1,
		"network":   s.Network,
		"wasm_hash": s.WasmHash,
		"interface": s.Interface,
	}
	if s.ContractID != "" {
		doc["contract_id"] = s.ContractID
	}
	canonical, err := CanonicalizeValue(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(InterfaceDomain), canonical...), nil
}

// Signer signs attestations with one Ed25519 key.
type Signer struct {
	priv ed25519.PrivateKey
	id   string
}

// NewSigner builds a Signer from a 32-byte Ed25519 seed.
func NewSigner(seed []byte) (*Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("attest: signing key must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Signer{priv: priv, id: KeyID(priv.Public().(ed25519.PublicKey))}, nil
}

// ParseSeed decodes a base64 (standard or URL-safe, padded or not) seed.
func ParseSeed(encoded string) ([]byte, error) {
	seed, err := decodeBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("attest: signing key is not valid base64: %w", err)
	}
	return seed, nil
}

// GenerateSeed returns a fresh random seed from the given source.
func GenerateSeed(random interface{ Read([]byte) (int, error) }) ([]byte, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := random.Read(seed); err != nil {
		return nil, fmt.Errorf("attest: generating key: %w", err)
	}
	return seed, nil
}

// KeyID is the identifier of a public key: the first 8 bytes of its SHA-256
// digest, in hex. It is short enough to read in a header and long enough
// that two keys of one registry will not collide.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// KeyID is the identifier clients look the public key up by.
func (s *Signer) KeyID() string { return s.id }

// PublicKey returns the key that verifies this signer's signatures.
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.priv.Public().(ed25519.PublicKey)
}

// Sign returns the base64 detached signature of the statement.
func (s *Signer) Sign(st Statement) (string, error) {
	msg, err := st.Message()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, msg)), nil
}

// Verify checks a base64 detached signature over a statement.
func Verify(pub ed25519.PublicKey, signature string, st Statement) error {
	sig, err := decodeBase64(signature)
	if err != nil {
		return fmt.Errorf("%w: signature is not valid base64", ErrBadSignature)
	}
	msg, err := st.Message()
	if err != nil {
		return err
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, msg, sig) {
		return ErrBadSignature
	}
	return nil
}

// Key is a public verification key as published at GET /api/keys.
type Key struct {
	// ID is KeyID of the public key.
	ID string `json:"id"`
	// Algorithm is always "ed25519".
	Algorithm string `json:"algorithm"`
	// PublicKey is the 32-byte key, base64 encoded.
	PublicKey string `json:"public_key"`
	// Current is true for the key that signs new responses, and false for a
	// retired key published so older signatures stay verifiable.
	Current bool `json:"current"`
}

// NewKey describes a public key for publication.
func NewKey(pub ed25519.PublicKey, current bool) Key {
	return Key{
		ID:        KeyID(pub),
		Algorithm: "ed25519",
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Current:   current,
	}
}

// ParsePublicKey decodes a base64 Ed25519 public key.
func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := decodeBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("attest: public key is not valid base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("attest: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// decodeBase64 accepts the standard and URL-safe alphabets, with or without
// padding, since operators paste keys from several tools.
func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

// VerifyResponse verifies a served interface response against a key.
//
// body is the JSON of GET /api/contracts/{id} or GET /api/wasm/{hash} as
// received; signature is the value of the X-SoroVault-Attestation header.
func VerifyResponse(pub ed25519.PublicKey, signature string, body []byte) error {
	var doc struct {
		Network    string          `json:"network"`
		ContractID string          `json:"contract_id"`
		WasmHash   string          `json:"wasm_hash"`
		Interface  json.RawMessage `json:"interface"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("attest: decoding response: %w", err)
	}
	return Verify(pub, signature, Statement{
		Network: doc.Network, ContractID: doc.ContractID, WasmHash: doc.WasmHash, Interface: doc.Interface,
	})
}
