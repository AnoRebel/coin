package protocol

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"golang.org/x/crypto/pbkdf2"
)

//go:embed bip39_english.txt
var wordlistFS embed.FS

const (
	SuiteEd25519 = "ed25519-v1"
	// DerivationPath is the ARNE account path: m/44'/9999'/account'/change'/index'.
	PurposeBIP44 uint32 = 44
	CoinTypeARNE uint32 = 9999
)

var (
	ErrMnemonic = errors.New("mnemonic")
	wordsOnce   sync.Once
	words       []string
	wordIndex   map[string]int
)

func loadWords() {
	if words != nil {
		return
	}
	b, err := wordlistFS.ReadFile("bip39_english.txt")
	if err != nil {
		panic(err)
	}
	words = strings.Fields(string(b))
	if len(words) != 2048 {
		panic("bip39: wordlist must contain 2048 words")
	}
	wordIndex = make(map[string]int, 2048)
	for i, w := range words {
		wordIndex[w] = i
	}
}

// KeyPair is an Ed25519 identity with its derived address.
type KeyPair struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	Address string
	Network string
}

// Sign signs payload with Ed25519.
func (k *KeyPair) Sign(payload []byte) []byte {
	return ed25519.Sign(k.Private, payload)
}

// VerifyEd25519 verifies a detached Ed25519 signature.
func VerifyEd25519(pub, payload, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(ed25519.PublicKey(pub), payload, sig)
}

// NewKeyPair generates a random 24-word mnemonic and derives its key.
func NewKeyPair(network string) (*KeyPair, string, error) {
	entropy := make([]byte, 32)
	if _, err := rand.Read(entropy); err != nil {
		return nil, "", err
	}
	mnemonic, err := MnemonicFromEntropy(entropy)
	if err != nil {
		return nil, "", err
	}
	kp, err := KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
	return kp, mnemonic, err
}

// MnemonicFromEntropy encodes 16-32 bytes as BIP-39 words.
func MnemonicFromEntropy(entropy []byte) (string, error) {
	if len(entropy) < 16 || len(entropy) > 32 || len(entropy)%4 != 0 {
		return "", fmt.Errorf("%w: entropy length", ErrMnemonic)
	}
	loadWords()
	h := sha256.Sum256(entropy)
	bits := make([]byte, 0, len(entropy)*8+len(entropy)/4)
	for _, b := range entropy {
		for i := 7; i >= 0; i-- {
			bits = append(bits, (b>>uint(i))&1)
		}
	}
	cs := len(entropy) * 8 / 32
	for i := 0; i < cs; i++ {
		bits = append(bits, (h[i/8]>>uint(7-i%8))&1)
	}
	out := make([]string, 0, len(bits)/11)
	for i := 0; i+11 <= len(bits); i += 11 {
		idx := 0
		for j := 0; j < 11; j++ {
			idx = idx<<1 | int(bits[i+j])
		}
		out = append(out, words[idx])
	}
	return strings.Join(out, " "), nil
}

// MnemonicToSeed performs PBKDF2-HMAC-SHA512(mnemonic, "mnemonic"+passphrase).
func MnemonicToSeed(mnemonic, passphrase string) ([]byte, error) {
	if err := ValidateMnemonic(mnemonic); err != nil {
		return nil, err
	}
	return pbkdf2.Key([]byte(mnemonic), []byte("mnemonic"+passphrase), 2048, 64, sha512.New), nil
}

// ValidateMnemonic checks word count, vocabulary, and checksum.
func ValidateMnemonic(mnemonic string) error {
	loadWords()
	parts := strings.Fields(mnemonic)
	if len(parts) != 12 && len(parts) != 15 && len(parts) != 18 && len(parts) != 21 && len(parts) != 24 {
		return fmt.Errorf("%w: word count", ErrMnemonic)
	}
	bits := make([]byte, 0, len(parts)*11)
	for _, w := range parts {
		idx, ok := wordIndex[w]
		if !ok {
			return fmt.Errorf("%w: unknown word %q", ErrMnemonic, w)
		}
		for i := 10; i >= 0; i-- {
			bits = append(bits, byte(idx>>uint(i))&1)
		}
	}
	cs := len(parts) * 11 / 33
	entBits := len(bits) - cs
	entropy := make([]byte, entBits/8)
	for i := 0; i < entBits; i++ {
		entropy[i/8] |= bits[i] << uint(7-i%8)
	}
	h := sha256.Sum256(entropy)
	for i := 0; i < cs; i++ {
		if bits[entBits+i] != (h[i/8]>>uint(7-i%8))&1 {
			return fmt.Errorf("%w: checksum", ErrMnemonic)
		}
	}
	return nil
}

// SLIP-0010 Ed25519 derivation (hardened only).
type hdNode struct {
	key       []byte
	chainCode []byte
}

func hmacSHA512(key, data []byte) []byte {
	h := hmac.New(sha512.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func slip10Master(seed []byte) hdNode {
	sum := hmacSHA512([]byte("ed25519 seed"), seed)
	return hdNode{key: sum[:32], chainCode: sum[32:]}
}

func (n hdNode) child(index uint32) hdNode {
	var data []byte
	data = append(data, 0x00)
	data = append(data, n.key...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], index|0x80000000)
	data = append(data, idx[:]...)
	sum := hmacSHA512(n.chainCode, data)
	return hdNode{key: sum[:32], chainCode: sum[32:]}
}

// DerivePath derives an Ed25519 key from a BIP-39 mnemonic using SLIP-0010.
// Path: m/44'/9999'/account'/change'/index'.
func DerivePath(mnemonic, passphrase string, account, change, index uint32) ([]byte, error) {
	seed, err := MnemonicToSeed(mnemonic, passphrase)
	if err != nil {
		return nil, err
	}
	n := slip10Master(seed)
	for _, i := range []uint32{PurposeBIP44, CoinTypeARNE, account, change, index} {
		n = n.child(i)
	}
	return n.key, nil
}

// KeyPairFromMnemonic derives the Ed25519 identity at the ARNE path.
func KeyPairFromMnemonic(mnemonic, passphrase string, account, change, index uint32, network string) (*KeyPair, error) {
	priv, err := DerivePath(mnemonic, passphrase, account, change, index)
	if err != nil {
		return nil, err
	}
	key := ed25519.NewKeyFromSeed(priv)
	pub := key.Public().(ed25519.PublicKey)
	return &KeyPair{
		Private: key, Public: pub,
		Address: AddressFromPubKey(pub, network), Network: network,
	}, nil
}

// KeyPairFromPrivate builds a key pair from a 32-byte seed.
func KeyPairFromPrivate(seed []byte, network string) (*KeyPair, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: seed length", ErrMnemonic)
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	return &KeyPair{Private: key, Public: pub, Address: AddressFromPubKey(pub, network), Network: network}, nil
}

// RandomSeed returns 32 bytes of cryptographic randomness as hex input.
func RandomSeed() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// bigEndian64 encodes a uint64.
func bigEndian64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

var _ = big.NewInt // keep math/big available for callers generating large values
