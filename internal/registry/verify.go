package registry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// Windows around a claim's issued_at. A claim older than ClaimMaxAge is
// stale, which bounds how long a captured claim can be replayed;
// ClaimMaxFuture tolerates a publisher's clock running a little ahead.
const (
	ClaimMaxAge    = 15 * time.Minute
	ClaimMaxFuture = 2 * time.Minute
)

// Limits on the free-text parts of a claim.
const (
	maxClaimNameLength = 100
	maxClaimURLLength  = 2048
)

// Reasons a claim is refused. Each wraps into a message that says why, and
// the API maps them onto distinct status codes.
var (
	// ErrInvalidClaim reports a claim that is malformed or whose signature
	// does not verify.
	ErrInvalidClaim = errors.New("registry: invalid publisher claim")
	// ErrStaleClaim reports a claim that is out of date: issued too long
	// ago (or in the future), or made for a version the contract no longer
	// runs.
	ErrStaleClaim = errors.New("registry: stale publisher claim")
	// ErrNotDeployer reports a validly signed claim from an account that
	// did not create the contract.
	ErrNotDeployer = errors.New("registry: claim not signed by the contract's deployer")
	// ErrDeployerUnknown reports that the registry cannot tell who created
	// the contract, so it cannot check who signed.
	ErrDeployerUnknown = errors.New("registry: contract's deployer is not known")
)

var claimCommit = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// PublisherClaim is a claim together with the proof of who made it.
type PublisherClaim struct {
	Claim attest.Claim
	// Signer is the Stellar account address ("G…") that signed.
	Signer string
	// Signature is the base64 Ed25519 signature of Claim.Message().
	Signature string
}

// VerifyPublisher checks a signed claim and, if it holds, marks the contract
// verified and records the name and source the claim carries.
//
// A claim holds when all of these are true:
//
//   - it is well formed and names this registry's network;
//   - it was issued within the last ClaimMaxAge, so it cannot be replayed
//     later, and if it pins a WASM hash, the contract still runs it;
//   - its signature verifies against the signing account;
//   - the signing account is the one that created the contract.
//
// The deployer is the only relationship accepted. A contract's admin is a
// convention of each contract, not something the network records, so
// accepting "the admin" would mean guessing at storage layouts; a publisher
// whose deploying account is not the one they sign with can sign with the
// deployer.
func (r *Registry) VerifyPublisher(ctx context.Context, req PublisherClaim) (*store.Contract, error) {
	claim := req.Claim
	if err := stellar.ValidateContractID(claim.ContractID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidClaim, err)
	}

	network, err := r.Network(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateClaim(claim, network); err != nil {
		return nil, err
	}

	issued, err := claim.IssuedTime()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidClaim, err)
	}
	now := time.Now().UTC()
	switch {
	case now.Sub(issued) > ClaimMaxAge:
		return nil, fmt.Errorf("%w: issued at %s, more than %s ago",
			ErrStaleClaim, claim.IssuedAt, ClaimMaxAge)
	case issued.Sub(now) > ClaimMaxFuture:
		return nil, fmt.Errorf("%w: issued at %s, which is in the future", ErrStaleClaim, claim.IssuedAt)
	}

	// The signature is checked before the registry is consulted, so an
	// unauthenticated caller cannot use this to probe what is registered.
	if err := attest.VerifyClaim(req.Signer, req.Signature, claim); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidClaim, err)
	}

	contract, err := r.store.GetContract(ctx, network, claim.ContractID)
	if err != nil {
		return nil, err
	}
	if claim.WasmHash != "" && claim.WasmHash != contract.CurrentWasmHash {
		return nil, fmt.Errorf("%w: made for wasm %s, but the contract now runs %s",
			ErrStaleClaim, claim.WasmHash, contract.CurrentWasmHash)
	}

	if contract.Creator == "" {
		// The deployer may simply not have been looked up yet.
		r.recordOrigin(ctx, claim.ContractID)
		if contract, err = r.store.GetContract(ctx, network, claim.ContractID); err != nil {
			return nil, err
		}
	}
	if contract.Creator == "" {
		return nil, fmt.Errorf("%w: it was not found in the reachable history of %s",
			ErrDeployerUnknown, network)
	}
	if req.Signer != contract.Creator {
		return nil, fmt.Errorf("%w: signed by %s, deployed by %s",
			ErrNotDeployer, req.Signer, contract.Creator)
	}

	err = r.store.SetVerified(ctx, network, claim.ContractID,
		strings.TrimSpace(claim.Name), claim.SourceURL, claim.SourceCommit,
		store.Verification{Publisher: req.Signer, VerifiedAt: now, WasmHash: contract.CurrentWasmHash})
	if err != nil {
		return nil, err
	}

	verified, err := r.store.GetContract(ctx, network, claim.ContractID)
	if err != nil {
		return nil, err
	}
	r.log.InfoContext(ctx, "publisher verified",
		"network", stellar.NetworkLogLabel(network))
	return verified, nil
}

// validateClaim checks the parts of a claim that do not depend on the
// registry's contents.
func validateClaim(c attest.Claim, network string) error {
	if c.Network != network {
		return fmt.Errorf("%w: claim is for network %q, this registry serves %q",
			ErrInvalidClaim, c.Network, network)
	}

	name := strings.TrimSpace(c.Name)
	switch {
	case name == "":
		return fmt.Errorf("%w: name is required", ErrInvalidClaim)
	case utf8.RuneCountInString(name) > maxClaimNameLength:
		return fmt.Errorf("%w: name is longer than %d characters", ErrInvalidClaim, maxClaimNameLength)
	case strings.ContainsFunc(name, unicode.IsControl):
		return fmt.Errorf("%w: name contains control characters", ErrInvalidClaim)
	}

	parsed, err := url.Parse(c.SourceURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		len(c.SourceURL) > maxClaimURLLength {
		return fmt.Errorf("%w: source_url must be an absolute HTTP or HTTPS URL", ErrInvalidClaim)
	}
	if c.SourceCommit != "" && !claimCommit.MatchString(c.SourceCommit) {
		return fmt.Errorf("%w: source_commit must be a 40- or 64-character hexadecimal commit hash", ErrInvalidClaim)
	}
	if c.WasmHash != "" {
		if _, err := parseHash(c.WasmHash); err != nil {
			return fmt.Errorf("%w: wasm_hash is not a 32-byte hexadecimal hash", ErrInvalidClaim)
		}
	}
	return nil
}
