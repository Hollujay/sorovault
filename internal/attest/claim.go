package attest

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// Claim is a publisher's statement about one contract: who they say it is,
// and where its source lives. Signed with the key of the account that
// deployed the contract, it is proof the deployer made the statement.
type Claim struct {
	Network    string `json:"network"`
	ContractID string `json:"contract_id"`
	// Name is the publisher's name for the contract.
	Name string `json:"name"`
	// SourceURL is the repository the contract was built from.
	SourceURL    string `json:"source_url"`
	SourceCommit string `json:"source_commit,omitempty"`
	// WasmHash, when set, pins the claim to one version of the contract: it
	// is rejected if the contract has been upgraded since.
	WasmHash string `json:"wasm_hash,omitempty"`
	// IssuedAt is an RFC 3339 timestamp. A claim older than the registry's
	// window is stale, which is what stops a captured claim being replayed.
	IssuedAt string `json:"issued_at"`
}

// Message returns the bytes a publisher signs: the claim domain separator
// followed by the canonical encoding of the claim plus a version number.
func (c Claim) Message() ([]byte, error) {
	doc := map[string]any{
		"v":           1,
		"network":     c.Network,
		"contract_id": c.ContractID,
		"name":        c.Name,
		"source_url":  c.SourceURL,
		"issued_at":   c.IssuedAt,
	}
	if c.SourceCommit != "" {
		doc["source_commit"] = c.SourceCommit
	}
	if c.WasmHash != "" {
		doc["wasm_hash"] = c.WasmHash
	}
	canonical, err := CanonicalizeValue(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(ClaimDomain), canonical...), nil
}

// IssuedTime parses IssuedAt.
func (c Claim) IssuedTime() (time.Time, error) {
	t, err := time.Parse(time.RFC3339, c.IssuedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("attest: issued_at %q is not an RFC 3339 timestamp", c.IssuedAt)
	}
	return t.UTC(), nil
}

// SignClaim signs a claim with a Stellar secret seed ("S…"), returning the
// signer's account address and the base64 signature.
func SignClaim(secretSeed string, c Claim) (signer, signature string, err error) {
	kp, err := keypair.ParseFull(secretSeed)
	if err != nil {
		return "", "", errors.New("attest: not a valid Stellar secret seed")
	}
	msg, err := c.Message()
	if err != nil {
		return "", "", err
	}
	sig, err := kp.Sign(msg)
	if err != nil {
		return "", "", fmt.Errorf("attest: signing claim: %w", err)
	}
	return kp.Address(), base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyClaim checks that signature is a valid signature of the claim by the
// Stellar account at address. It says nothing about whether that account has
// any relationship to the contract; the registry decides that.
func VerifyClaim(address, signature string, c Claim) error {
	raw, err := strkey.Decode(strkey.VersionByteAccountID, address)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("attest: %q is not a Stellar account address", address)
	}
	sig, err := decodeBase64(signature)
	if err != nil {
		return fmt.Errorf("%w: signature is not valid base64", ErrBadSignature)
	}
	msg, err := c.Message()
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(raw), msg, sig) {
		return ErrBadSignature
	}
	return nil
}
