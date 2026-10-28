package node

import (
	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// DevnetGenesis builds a funded, self-contained devnet: a funder account, a
// faucet account, a fee-swap reserve, and a demo USD asset.
func DevnetGenesis(network string, now uint64, funder, faucet *protocol.KeyPair) ledger.Genesis {
	native := protocol.NativeAsset(network)
	const arce = uint64(100_000_000) // 1 ARCE at 8 decimals
	chain, _ := protocol.ChainID(network)
	return ledger.Genesis{
		Network: network,
		Chain:   chain,
		Time:    now,
		Params:  ledger.DefaultParams(),
		Assets: []protocol.Asset{{
			ID: "usd:" + protocol.ShortNetwork(network), Class: "fiat", Symbol: "USD",
			Name: "Demo US Dollar", Decimals: 2, Policy: "capped",
			MaxSupply: 10_000_000_000, Issuer: funder.Address,
			Metadata: map[string]string{"iso4217": "USD", "demo": "true"},
		}},
		Allocations: []ledger.Allocation{
			{Address: funder.Address, Asset: native, Amount: 1_000 * arce},
			{Address: faucet.Address, Asset: native, Amount: 10_000 * arce},
		},
		Pools: map[string]map[string]uint64{
			ledger.PoolFeeSwap: {native: 1_000_000 * arce},
		},
	}
}
