// Package statesync decides whether a peer's state snapshot may be trusted.
//
// A snapshot is only as good as the commit that produced it. This package
// checks the snapshot's own digest and chained blocks, then requires a
// certificate signed by more than two thirds of the trusted validator set at
// that exact height, with the same state root. A snapshot that fails any check
// is discarded and the caller falls back to block sync.
package statesync

import (
	"errors"
	"fmt"

	"github.com/anorebel/anorebelcoin/internal/lightclient"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/snapshot"
)

var (
	ErrInvalid     = errors.New("statesync: invalid request")
	ErrNetwork     = errors.New("statesync: snapshot is for another network")
	ErrSnapshot    = errors.New("statesync: snapshot verification failed")
	ErrHeight      = errors.New("statesync: certificate height does not match the snapshot")
	ErrRoot        = errors.New("statesync: state root does not match the certificate")
	ErrCertificate = errors.New("statesync: commit certificate is not trusted")
)

// Trusted is the network and validator set a node already trusts. It comes from
// genesis or a previously verified certificate, never from the peer offering
// the snapshot.
type Trusted struct {
	Network string          `json:"network"`
	Set     lightclient.Set `json:"set"`
}

// Offer is what a peer provides: a snapshot, the full base when the snapshot is
// incremental, and the commit certificate at the snapshot height.
type Offer struct {
	Snapshot    *snapshot.File          `json:"snapshot"`
	Base        *snapshot.File          `json:"base,omitempty"`
	Certificate lightclient.Certificate `json:"certificate"`
}

// Result is an accepted snapshot.
type Result struct {
	Height      uint64 `json:"height"`
	StateRoot   string `json:"state_root"`
	Kind        string `json:"kind"`
	SignedPower uint64 `json:"signed_power"`
}

// Verify accepts an offer only when the snapshot is internally consistent, is
// for the trusted network and chain, and is finalized by the trusted set.
func Verify(offer Offer, trusted Trusted) (Result, error) {
	if offer.Snapshot == nil {
		return Result{}, fmt.Errorf("%w: snapshot is required", ErrInvalid)
	}
	if _, err := protocol.NetworkClass(trusted.Network); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := trusted.Set.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	chain, err := protocol.ChainID(trusted.Network)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	if err := snapshot.Verify(offer.Snapshot, offer.Base); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrSnapshot, err)
	}
	if offer.Snapshot.Network != trusted.Network || offer.Snapshot.Chain != chain {
		return Result{}, fmt.Errorf("%w: %s/%s", ErrNetwork, offer.Snapshot.Network, offer.Snapshot.Chain)
	}
	if offer.Certificate.Height != offer.Snapshot.Height {
		return Result{}, fmt.Errorf("%w: certificate %d, snapshot %d",
			ErrHeight, offer.Certificate.Height, offer.Snapshot.Height)
	}
	if offer.Certificate.StateRoot != offer.Snapshot.StateRoot {
		return Result{}, fmt.Errorf("%w: certificate %s, snapshot %s",
			ErrRoot, offer.Certificate.StateRoot, offer.Snapshot.StateRoot)
	}

	signed, err := lightclient.Verify(trusted.Network, trusted.Set, offer.Certificate)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrCertificate, err)
	}
	return Result{
		Height:      offer.Snapshot.Height,
		StateRoot:   offer.Snapshot.StateRoot,
		Kind:        offer.Snapshot.Kind,
		SignedPower: signed,
	}, nil
}
