package protocol

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// BIP-39 test vector from the BIP itself (trezor).
const vectorMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func TestMnemonicChecksum(t *testing.T) {
	if err := ValidateMnemonic(vectorMnemonic); err != nil {
		t.Fatalf("valid mnemonic rejected: %v", err)
	}
	bad := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon"
	if err := ValidateMnemonic(bad); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func TestBIP39ReferenceSeed(t *testing.T) {
	// BIP-39: mnemonic with passphrase "TREZOR" -> known seed.
	seed, err := MnemonicToSeed(vectorMnemonic, "TREZOR")
	if err != nil {
		t.Fatal(err)
	}
	want := "c55257c360c07c72029aebc1b53c05ed0362ada38ead3e3e9efa3708e53495531f09a6987599d18264c1e1c92f2cf141630c7a3c4ab7c81b2f001698e7463b04"
	if hex.EncodeToString(seed) != want {
		t.Fatalf("seed = %x, want %s", seed, want)
	}
}

func TestAddressRoundTrip(t *testing.T) {
	kp, _, err := NewKeyPair("arne:devnet")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAddress(kp.Address, "arne:devnet"); err != nil {
		t.Fatalf("address %q invalid: %v", kp.Address, err)
	}
	if err := ValidateAddress(kp.Address, "arne:mainnet"); err == nil {
		t.Fatal("wrong-network address accepted")
	}
	// Mutated checksum must fail.
	b := []byte(kp.Address)
	b[len(b)-1] ^= 1
	if _, _, err := DecodeAddress(string(b)); err == nil {
		t.Fatal("mutated address accepted")
	}
}

func TestSignAndVerifyTx(t *testing.T) {
	kp, _, err := NewKeyPair("arne:devnet")
	if err != nil {
		t.Fatal(err)
	}
	tx := Tx{
		Version: CurrentTxVersion, Network: "arne:devnet", Chain: "arne-devnet-1",
		Type: TxTransfer, Signer: kp.Address, Nonce: 0,
		FeeAsset: NativeAsset("arne:devnet"), MaxFee: 1000,
		Payload: map[string]any{"asset": NativeAsset("arne:devnet"), "to": kp.Address, "amount": uint64(5)},
	}
	if err := tx.Sign(kp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	enc, err := Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodeMap(enc)
	if err != nil {
		t.Fatal(err)
	}
	back, err := TxFromCBOR(dec)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(back.ToCBOR())
	if err != nil || !bytes.Equal(enc, again) {
		t.Fatal("tx round-trip mismatch")
	}
	// Tampering must invalidate.
	back.MaxFee = 1
	if err := back.Verify(); err == nil {
		t.Fatal("tampered tx verified")
	}
}

func TestSignatureDomainSeparation(t *testing.T) {
	kp, _, err := NewKeyPair("arne:devnet")
	if err != nil {
		t.Fatal(err)
	}
	tx := Tx{
		Version: CurrentTxVersion, Network: "arne:devnet", Chain: "arne-devnet-1",
		Type: TxTransfer, Signer: kp.Address,
		FeeAsset: NativeAsset("arne:devnet"), MaxFee: 1000,
		Payload: map[string]any{"asset": NativeAsset("arne:devnet"), "to": kp.Address, "amount": uint64(5)},
	}
	if err := tx.SignPurpose(kp, "tx"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Re-signing with a different purpose must not verify as a transaction.
	other := tx
	other.Signatures = nil
	if err := other.SignPurpose(kp, "message"); err != nil {
		t.Fatal(err)
	}
	if err := other.Verify(); err == nil {
		t.Fatal("purpose-confused signature verified as a tx")
	}
	// A signature from another network must not verify here.
	mainnet, _, err := NewKeyPair("arne:mainnet")
	if err != nil {
		t.Fatal(err)
	}
	foreign := tx
	foreign.Network, foreign.Signer, foreign.Signatures = "arne:mainnet", mainnet.Address, nil
	if err := foreign.SignPurpose(mainnet, "tx"); err != nil {
		t.Fatal(err)
	}
	foreign.Network, foreign.Chain = "arne:devnet", "arne-devnet-1"
	if err := foreign.Verify(); err == nil {
		t.Fatal("cross-network signature verified")
	}
}

func TestDerivationDeterministic(t *testing.T) {
	a, err := DerivePath(vectorMnemonic, "", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DerivePath(vectorMnemonic, "", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || len(a) != 32 {
		t.Fatal("derivation not deterministic")
	}
	c, _ := DerivePath(vectorMnemonic, "", 0, 0, 1)
	if bytes.Equal(a, c) {
		t.Fatal("different indexes produced the same key")
	}
}
