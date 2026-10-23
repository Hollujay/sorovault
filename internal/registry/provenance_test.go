package registry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/registry"
)

func TestRegisterRecordsOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a traceable contract gets its creator and ledger", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "hello"))
		fake.SetOrigin(idA, "GCREATOR", 4321)

		result, err := reg.Register(ctx, idA)
		require.NoError(t, err)
		assert.Equal(t, "GCREATOR", result.Contract.Creator)
		assert.Equal(t, uint32(4321), result.Contract.CreatedLedger)
	})

	t.Run("an untraceable contract still registers, with no creator", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "hello"))

		result, err := reg.Register(ctx, idA)
		require.NoError(t, err)
		assert.Empty(t, result.Contract.Creator)
	})

	t.Run("a failing lookup does not fail the registration", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "hello"))
		fake.OriginErr = errors.New("rpc down")

		result, err := reg.Register(ctx, idA)
		require.NoError(t, err)
		assert.True(t, result.Created)
		assert.Empty(t, result.Contract.Creator)
	})

	t.Run("a known origin is not looked up again", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "hello"))
		fake.SetOrigin(idA, "GCREATOR", 1)

		_, err := reg.Register(ctx, idA)
		require.NoError(t, err)
		_, err = reg.Register(ctx, idA)
		require.NoError(t, err)
		assert.Equal(t, 1, fake.Calls["FindOrigin"])
	})

	t.Run("an upgrade keeps the origin", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "v1"))
		fake.SetOrigin(idA, "GCREATOR", 9)
		_, err := reg.Register(ctx, idA)
		require.NoError(t, err)

		fake.Upgrade(idA, moduleWith(t, "v2"))
		result, err := reg.Refresh(ctx, idA)
		require.NoError(t, err)
		assert.True(t, result.Changed)
		assert.Equal(t, "GCREATOR", result.Contract.Creator)
	})
}

func TestBackfillOrigins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reg, fake, db := newRegistry(t)
	fake.Deploy(idA, moduleWith(t, "a"))
	fake.Deploy(idB, moduleWith(t, "b"))
	_, err := reg.Register(ctx, idA)
	require.NoError(t, err)
	_, err = reg.Register(ctx, idB)
	require.NoError(t, err)
	require.NoError(t, db.SetOrigin(ctx, "testnet", idB, "GALREADY", 5))

	fake.SetOrigin(idA, "GFOUND", 77)
	fake.Calls["FindOrigin"] = 0

	sum, err := reg.BackfillOrigins(ctx)
	require.NoError(t, err)
	assert.Equal(t, registry.BackfillSummary{Checked: 1, Found: 1}, sum, "only contracts lacking an origin are looked at")
	assert.Equal(t, 1, fake.Calls["FindOrigin"])

	got, err := db.GetContract(ctx, "testnet", idA)
	require.NoError(t, err)
	assert.Equal(t, "GFOUND", got.Creator)
	got, err = db.GetContract(ctx, "testnet", idB)
	require.NoError(t, err)
	assert.Equal(t, "GALREADY", got.Creator, "an existing origin is left alone")

	t.Run("unknown and failed are counted apart", func(t *testing.T) {
		reg, fake, _ := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "a"))
		_, err := reg.Register(ctx, idA)
		require.NoError(t, err)

		sum, err := reg.BackfillOrigins(ctx)
		require.NoError(t, err)
		assert.Equal(t, registry.BackfillSummary{Checked: 1, Unknown: 1}, sum)

		fake.OriginErr = errors.New("rpc down")
		sum, err = reg.BackfillOrigins(ctx)
		require.NoError(t, err)
		assert.Equal(t, registry.BackfillSummary{Checked: 1, Failed: 1}, sum)
	})
}

// claimFixture registers idA with a known deployer and returns what a
// publisher needs to sign for it.
type claimFixture struct {
	reg      *registry.Registry
	deployer *keypair.Full
	claim    attest.Claim
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	reg, fake, _ := newRegistry(t)
	deployer := keypair.MustRandom()
	fake.Deploy(idA, moduleWith(t, "hello"))
	fake.SetOrigin(idA, deployer.Address(), 100)
	result, err := reg.Register(context.Background(), idA)
	require.NoError(t, err)

	return &claimFixture{
		reg: reg, deployer: deployer,
		claim: attest.Claim{
			Network:      "testnet",
			ContractID:   idA,
			Name:         "Hello",
			SourceURL:    "https://github.com/example/hello",
			SourceCommit: "0123456789abcdef0123456789abcdef01234567",
			WasmHash:     result.Contract.CurrentWasmHash,
			IssuedAt:     time.Now().UTC().Format(time.RFC3339),
		},
	}
}

func (f *claimFixture) sign(t *testing.T, key *keypair.Full, c attest.Claim) registry.PublisherClaim {
	t.Helper()
	signer, sig, err := attest.SignClaim(key.Seed(), c)
	require.NoError(t, err)
	return registry.PublisherClaim{Claim: c, Signer: signer, Signature: sig}
}

func TestVerifyPublisher(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("the deployer's fresh claim verifies the contract", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)

		got, err := f.reg.VerifyPublisher(ctx, f.sign(t, f.deployer, f.claim))
		require.NoError(t, err)
		assert.True(t, got.Verified())
		assert.Equal(t, f.deployer.Address(), got.VerifiedBy)
		assert.Equal(t, got.CurrentWasmHash, got.VerifiedWasmHash)
		assert.Equal(t, "Hello", got.Name)
		assert.Equal(t, "https://github.com/example/hello", got.SourceURL)
		assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", got.SourceCommit)
	})

	t.Run("a claim need not pin a version", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		f.claim.WasmHash = ""
		got, err := f.reg.VerifyPublisher(ctx, f.sign(t, f.deployer, f.claim))
		require.NoError(t, err)
		assert.True(t, got.Verified())
	})

	t.Run("someone else's signature is refused", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		_, err := f.reg.VerifyPublisher(ctx, f.sign(t, keypair.MustRandom(), f.claim))
		assert.ErrorIs(t, err, registry.ErrNotDeployer)
		assert.ErrorContains(t, err, f.deployer.Address())
	})

	t.Run("a signature that does not match the claim is invalid", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		req := f.sign(t, f.deployer, f.claim)
		req.Claim.SourceURL = "https://github.com/attacker/hello"
		_, err := f.reg.VerifyPublisher(ctx, req)
		assert.ErrorIs(t, err, registry.ErrInvalidClaim)
	})

	t.Run("claiming to be the deployer without its key is invalid", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		req := f.sign(t, keypair.MustRandom(), f.claim)
		req.Signer = f.deployer.Address()
		_, err := f.reg.VerifyPublisher(ctx, req)
		assert.ErrorIs(t, err, registry.ErrInvalidClaim)
	})

	t.Run("an old claim is stale", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		f.claim.IssuedAt = time.Now().Add(-registry.ClaimMaxAge - time.Minute).UTC().Format(time.RFC3339)
		_, err := f.reg.VerifyPublisher(ctx, f.sign(t, f.deployer, f.claim))
		assert.ErrorIs(t, err, registry.ErrStaleClaim)
	})

	t.Run("a claim from the future is stale", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		f.claim.IssuedAt = time.Now().Add(registry.ClaimMaxFuture + time.Minute).UTC().Format(time.RFC3339)
		_, err := f.reg.VerifyPublisher(ctx, f.sign(t, f.deployer, f.claim))
		assert.ErrorIs(t, err, registry.ErrStaleClaim)
	})

	t.Run("a claim for a version the contract no longer runs is stale", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		deployer := keypair.MustRandom()
		fake.Deploy(idA, moduleWith(t, "v1"))
		fake.SetOrigin(idA, deployer.Address(), 1)
		first, err := reg.Register(ctx, idA)
		require.NoError(t, err)
		fake.Upgrade(idA, moduleWith(t, "v2"))
		_, err = reg.Refresh(ctx, idA)
		require.NoError(t, err)

		f := &claimFixture{reg: reg, deployer: deployer}
		c := attest.Claim{
			Network: "testnet", ContractID: idA, Name: "Hello", SourceURL: "https://github.com/example/hello",
			WasmHash: first.Contract.CurrentWasmHash, IssuedAt: time.Now().UTC().Format(time.RFC3339),
		}
		_, err = reg.VerifyPublisher(ctx, f.sign(t, deployer, c))
		assert.ErrorIs(t, err, registry.ErrStaleClaim)
	})

	t.Run("a claim for another network is invalid", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		f.claim.Network = "public"
		_, err := f.reg.VerifyPublisher(ctx, f.sign(t, f.deployer, f.claim))
		assert.ErrorIs(t, err, registry.ErrInvalidClaim)
	})

	t.Run("a contract that was never registered is not found", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		f.claim.ContractID = idB
		f.claim.WasmHash = ""
		_, err := f.reg.VerifyPublisher(ctx, f.sign(t, f.deployer, f.claim))
		assert.ErrorContains(t, err, "not found")
	})

	t.Run("an unknown deployer cannot be verified against", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "hello"))
		_, err := reg.Register(ctx, idA)
		require.NoError(t, err)

		f := &claimFixture{reg: reg}
		c := attest.Claim{
			Network: "testnet", ContractID: idA, Name: "Hello", SourceURL: "https://github.com/example/hello",
			IssuedAt: time.Now().UTC().Format(time.RFC3339),
		}
		_, err = reg.VerifyPublisher(ctx, f.sign(t, keypair.MustRandom(), c))
		assert.ErrorIs(t, err, registry.ErrDeployerUnknown)
	})

	t.Run("the deployer is looked up on demand if registration could not", func(t *testing.T) {
		t.Parallel()
		reg, fake, _ := newRegistry(t)
		deployer := keypair.MustRandom()
		fake.Deploy(idA, moduleWith(t, "hello"))
		_, err := reg.Register(ctx, idA) // origin not yet traceable
		require.NoError(t, err)
		fake.SetOrigin(idA, deployer.Address(), 12)

		f := &claimFixture{reg: reg}
		c := attest.Claim{
			Network: "testnet", ContractID: idA, Name: "Hello", SourceURL: "https://github.com/example/hello",
			IssuedAt: time.Now().UTC().Format(time.RFC3339),
		}
		got, err := reg.VerifyPublisher(ctx, f.sign(t, deployer, c))
		require.NoError(t, err)
		assert.True(t, got.Verified())
		assert.Equal(t, deployer.Address(), got.Creator)
	})

	t.Run("malformed fields are invalid", func(t *testing.T) {
		t.Parallel()
		f := newClaimFixture(t)
		for name, mutate := range map[string]func(*attest.Claim){
			"empty name":      func(c *attest.Claim) { c.Name = "  " },
			"long name":       func(c *attest.Claim) { c.Name = string(make([]rune, 101)) + "x" },
			"control in name": func(c *attest.Claim) { c.Name = "a\x00b" },
			"relative source": func(c *attest.Claim) { c.SourceURL = "/repo" },
			"javascript url":  func(c *attest.Claim) { c.SourceURL = "javascript:alert(1)" },
			"short commit":    func(c *attest.Claim) { c.SourceCommit = "abc" },
			"bad wasm hash":   func(c *attest.Claim) { c.WasmHash = "zz" },
			"upper-case hash": func(c *attest.Claim) { c.WasmHash = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789" },
			"bad time":        func(c *attest.Claim) { c.IssuedAt = "yesterday" },
			"bad contract id": func(c *attest.Claim) { c.ContractID = "nope" },
		} {
			c := f.claim
			mutate(&c)
			_, err := f.reg.VerifyPublisher(ctx, f.sign(t, f.deployer, c))
			assert.ErrorIs(t, err, registry.ErrInvalidClaim, name)
		}
	})
}
