package statesync

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/crypto/bls"
	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/lightclient"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/snapshot"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

const (
	testNetwork = "arne:devnet"
	testChain   = "arne-devnet-1"
)

type validatorKey struct {
	id     string
	secret []byte
	public string
}

type fixture struct {
	store  storage.Store
	state  *ledger.State
	key    *protocol.KeyPair
	height uint64
}

func keys(t *testing.T) []validatorKey {
	t.Helper()
	out := make([]validatorKey, 0, 3)
	for _, seed := range []string{"validator-one", "validator-two", "validator-three", "validator-four"} {
		secret, err := bls.SecretFromSeed([]byte(seed))
		if err != nil {
			t.Fatal(err)
		}
		pub, err := bls.PublicKeyOf(secret)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, validatorKey{id: seed, secret: secret, public: hex.EncodeToString(pub)})
	}
	return out
}

func setOf(keys []validatorKey) lightclient.Set {
	set := lightclient.Set{
		Height:     1,
		Validators: make([]lightclient.Validator, 0, len(keys)),
	}
	for _, key := range keys {
		set.Validators = append(set.Validators, lightclient.Validator{
			ID: key.id, PublicKey: key.public, Power: 1,
		})
	}
	return set
}

func newFixture(t *testing.T, amount uint64, blocks int) *fixture {
	t.Helper()
	key, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ledger.NewGenesis(ledger.Genesis{
		Network: testNetwork, Chain: testChain, Time: 1_700_000_000,
		Params: ledger.DefaultParams(),
		Allocations: []ledger.Allocation{{
			Address: key.Address, Asset: protocol.NativeAsset(testNetwork), Amount: amount,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: storage.NewMemory(), state: state, key: key}
	f.advance(t, blocks)
	return f
}

func (f *fixture) advance(t *testing.T, blocks int) {
	t.Helper()
	for i := 0; i < blocks; i++ {
		f.height++
		block, _, err := f.state.ApplyOrderedBlock(nil, 1_700_000_000+f.height, f.height, f.key.Address)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.SaveCommit(block, f.state.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) snapshot(t *testing.T) *snapshot.File {
	t.Helper()
	file, err := snapshot.Create(f.store, snapshot.Options{Network: testNetwork, Chain: testChain})
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func certificate(t *testing.T, file *snapshot.File, signers []validatorKey) lightclient.Certificate {
	t.Helper()
	cert := lightclient.Certificate{
		Height:             file.Height,
		Round:              1,
		BlockHash:          strings.Repeat("22", 32),
		StateRoot:          file.StateRoot,
		NextValidatorsRoot: strings.Repeat("33", 32),
	}
	message := lightclient.Message(testNetwork, cert)
	for _, signer := range signers {
		signature, err := bls.Sign(signer.secret, message)
		if err != nil {
			t.Fatal(err)
		}
		cert.Signatures = append(cert.Signatures, lightclient.Signature{
			Validator: signer.id, Signature: hex.EncodeToString(signature),
		})
	}
	return cert
}

func TestVerifiesFinalizedSnapshot(t *testing.T) {
	keys := keys(t)
	file := newFixture(t, 10_000_000, 3).snapshot(t)
	trusted := Trusted{Network: testNetwork, Set: setOf(keys)}
	result, err := Verify(Offer{
		Snapshot:    file,
		Certificate: certificate(t, file, keys[:3]),
	}, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if result.Height != 3 || result.StateRoot != file.StateRoot || result.Kind != snapshot.KindFull {
		t.Fatalf("result = %+v", result)
	}
	if result.SignedPower != 3 {
		t.Fatalf("signed power = %d", result.SignedPower)
	}
}

func TestTamperedSnapshotIsDiscarded(t *testing.T) {
	keys := keys(t)
	file := newFixture(t, 10_000_000, 2).snapshot(t)
	file.State["balance/altered"] = "00"

	_, err := Verify(Offer{
		Snapshot:    file,
		Certificate: certificate(t, file, keys[:3]),
	}, Trusted{Network: testNetwork, Set: setOf(keys)})
	if !errors.Is(err, ErrSnapshot) {
		t.Fatalf("tampered snapshot = %v, want ErrSnapshot", err)
	}
}

func TestStateRootMustMatchTheCertificate(t *testing.T) {
	keys := keys(t)
	first := newFixture(t, 10_000_000, 2).snapshot(t)
	other := newFixture(t, 9_000_000, 2).snapshot(t)
	if first.StateRoot == other.StateRoot {
		t.Fatal("fixtures share a state root")
	}

	_, err := Verify(Offer{Snapshot: other, Certificate: certificate(t, first, keys[:3])},
		Trusted{Network: testNetwork, Set: setOf(keys)})
	if !errors.Is(err, ErrRoot) {
		t.Fatalf("root mismatch = %v, want ErrRoot", err)
	}
}

func TestCertificateHeightMustMatch(t *testing.T) {
	keys := keys(t)
	file := newFixture(t, 10_000_000, 2).snapshot(t)
	cert := certificate(t, file, keys[:3])
	cert.Height = file.Height + 1

	_, err := Verify(Offer{Snapshot: file, Certificate: cert},
		Trusted{Network: testNetwork, Set: setOf(keys)})
	if !errors.Is(err, ErrHeight) {
		t.Fatalf("height mismatch = %v, want ErrHeight", err)
	}
}

func TestAnotherNetworkIsRejected(t *testing.T) {
	keys := keys(t)
	file := newFixture(t, 10_000_000, 2).snapshot(t)

	_, err := Verify(Offer{Snapshot: file, Certificate: certificate(t, file, keys[:3])},
		Trusted{Network: "arne:testnet", Set: setOf(keys)})
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("network mismatch = %v, want ErrNetwork", err)
	}
}

func TestInsufficientQuorumIsRejected(t *testing.T) {
	keys := keys(t)
	file := newFixture(t, 10_000_000, 2).snapshot(t)

	_, err := Verify(Offer{Snapshot: file, Certificate: certificate(t, file, keys[:2])},
		Trusted{Network: testNetwork, Set: setOf(keys)})
	if !errors.Is(err, ErrCertificate) {
		t.Fatalf("half the power = %v, want ErrCertificate", err)
	}
}

func TestForgedSignatureIsRejected(t *testing.T) {
	keys := keys(t)
	file := newFixture(t, 10_000_000, 2).snapshot(t)
	cert := certificate(t, file, keys[:3])
	cert.Signatures[0].Signature = strings.Repeat("00", 96)

	_, err := Verify(Offer{Snapshot: file, Certificate: cert},
		Trusted{Network: testNetwork, Set: setOf(keys)})
	if !errors.Is(err, ErrCertificate) {
		t.Fatalf("forged signature = %v, want ErrCertificate", err)
	}
}

func TestIncrementalNeedsItsBase(t *testing.T) {
	keys := keys(t)
	f := newFixture(t, 10_000_000, 2)
	base := f.snapshot(t)
	f.advance(t, 1)
	incremental, err := snapshot.Create(f.store, snapshot.Options{
		Network: testNetwork, Chain: testChain, Base: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incremental.Kind != snapshot.KindIncremental {
		t.Fatalf("kind = %s", incremental.Kind)
	}

	_, err = Verify(Offer{
		Snapshot:    incremental,
		Certificate: certificate(t, incremental, keys[:3]),
	}, Trusted{Network: testNetwork, Set: setOf(keys)})
	if !errors.Is(err, ErrSnapshot) {
		t.Fatalf("incremental without base = %v, want ErrSnapshot", err)
	}

	result, err := Verify(Offer{
		Snapshot:    incremental,
		Base:        base,
		Certificate: certificate(t, incremental, keys[:3]),
	}, Trusted{Network: testNetwork, Set: setOf(keys)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != snapshot.KindIncremental || result.Height != 3 {
		t.Fatalf("result = %+v", result)
	}
}
