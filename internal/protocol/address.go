package protocol

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

// Bech32m (BIP-350) network-scoped addresses.
//
// Address = Bech32m(hrp, version || sha256(pubkey)[:20]), version 0x01.
// HRP: arce (mainnet), arce-testnet, arce-devnet, arce-consortium.

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var (
	ErrAddress = errors.New("address")
	bech32Gen  = []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
)

// HRP returns the address prefix for a network ID.
func HRP(network string) (string, error) {
	class, err := NetworkClass(network)
	if err != nil {
		return "", err
	}
	switch class {
	case ClassMainnet:
		return "arce", nil
	case ClassTestnet:
		return "arce-testnet", nil
	case ClassDevnet:
		return "arce-devnet", nil
	default:
		return "arce-consortium", nil
	}
}

// AddressFromPubKey derives the network-scoped address for an Ed25519 key.
func AddressFromPubKey(pub []byte, network string) string {
	hrp, err := HRP(network)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(pub)
	data := append([]byte{0x01}, h[:20]...)
	return bech32Encode(hrp, data)
}

// DecodeAddress validates an address and returns its HRP and key hash.
func DecodeAddress(addr string) (hrp string, keyHash []byte, err error) {
	i := strings.LastIndex(addr, "1")
	if i < 1 || i+7 > len(addr) || len(addr) > 90 {
		return "", nil, fmt.Errorf("%w: shape", ErrAddress)
	}
	hrp = addr[:i]
	if hrp != "arce" && hrp != "arce-testnet" && hrp != "arce-devnet" && hrp != "arce-consortium" {
		return "", nil, fmt.Errorf("%w: unknown hrp %q", ErrAddress, hrp)
	}
	data, err := bech32Decode(addr)
	if err != nil {
		return "", nil, err
	}
	if len(data) != 21 || data[0] != 0x01 {
		return "", nil, fmt.Errorf("%w: payload", ErrAddress)
	}
	return hrp, data[1:], nil
}

// ValidateAddress checks that addr is well-formed for the network.
func ValidateAddress(addr, network string) error {
	hrp, want, err := func() (string, string, error) {
		h, e := HRP(network)
		return h, h, e
	}()
	if err != nil {
		return err
	}
	got, _, err := DecodeAddress(addr)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%w: wrong network (want %s)", ErrAddress, hrp)
	}
	return nil
}

func bech32Polymod(values []byte) uint32 {
	chk := uint32(0x2bc830a3) // bech32m constant
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>uint(i))&1 == 1 {
				chk ^= bech32Gen[i]
			}
		}
	}
	return chk
}

func hrpExpand(hrp string) []byte {
	out := make([]byte, 0, len(hrp)*2+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var acc uint32
	var bits uint
	maxv := uint32(1<<to) - 1
	out := make([]byte, 0, len(data)*int(from)/int(to)+1)
	for _, b := range data {
		if uint32(b)>>from != 0 {
			return nil, fmt.Errorf("%w: invalid data", ErrAddress)
		}
		acc = acc<<from | uint32(b)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, fmt.Errorf("%w: invalid padding", ErrAddress)
	}
	return out, nil
}

func bech32Encode(hrp string, data []byte) string {
	five, err := convertBits(data, 8, 5, true)
	if err != nil {
		return ""
	}
	values := append(hrpExpand(hrp), five...)
	values = append(values, 0, 0, 0, 0, 0, 0)
	mod := bech32Polymod(values) ^ 0x2bc830a3
	checksum := make([]byte, 6)
	for i := 0; i < 6; i++ {
		checksum[i] = byte(mod >> uint(5*(5-i)) & 31)
	}
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, v := range append(five, checksum...) {
		sb.WriteByte(bech32Charset[v])
	}
	return sb.String()
}

func bech32Decode(addr string) ([]byte, error) {
	if strings.ToLower(addr) != addr && strings.ToUpper(addr) != addr {
		return nil, fmt.Errorf("%w: mixed case", ErrAddress)
	}
	addr = strings.ToLower(addr)
	i := strings.LastIndex(addr, "1")
	if i < 1 {
		return nil, fmt.Errorf("%w: separator", ErrAddress)
	}
	hrp, rest := addr[:i], addr[i+1:]
	if len(rest) < 6 {
		return nil, fmt.Errorf("%w: short checksum", ErrAddress)
	}
	five := make([]byte, len(rest))
	for j, c := range rest {
		idx := strings.IndexRune(bech32Charset, c)
		if idx < 0 {
			return nil, fmt.Errorf("%w: character %q", ErrAddress, c)
		}
		five[j] = byte(idx)
	}
	if bech32Polymod(append(hrpExpand(hrp), five...)) != 0x2bc830a3 {
		return nil, fmt.Errorf("%w: checksum", ErrAddress)
	}
	return convertBits(five[:len(five)-6], 5, 8, false)
}
