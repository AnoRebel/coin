// Package connector defines the handshake every interoperability connector
// completes before it is used.
//
// An adapter advertises the protocol versions and canonical assets it speaks
// and reports its health. Negotiate picks the highest common version and the
// assets the caller requires, and refuses an unhealthy adapter. Operations such
// as quotes and settlement are defined by the concrete adapters, which use the
// negotiated session.
package connector

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

var (
	ErrInvalid   = errors.New("connector: invalid")
	ErrDuplicate = errors.New("connector: duplicate")
	ErrUnhealthy = errors.New("connector: unhealthy")
	ErrVersion   = errors.New("connector: unsupported protocol version")
	ErrAsset     = errors.New("connector: unsupported asset")
)

// Status is an adapter's self-reported health.
type Status string

const (
	Healthy   Status = "healthy"
	Degraded  Status = "degraded"
	Unhealthy Status = "unhealthy"
)

// Health is the answer to a health check. Detail is for operators, not policy.
type Health struct {
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Request is what the caller needs from the connector.
type Request struct {
	Versions []uint32 `json:"versions"`
	Assets   []string `json:"assets"`
	Network  string   `json:"network,omitempty"`
}

// Session is the negotiated capability set.
type Session struct {
	Connector string   `json:"connector"`
	Version   uint32   `json:"version"`
	Assets    []string `json:"assets"`
	Network   string   `json:"network,omitempty"`
}

// Adapter is one external rail. Implementations return their static capability
// set and a fresh health answer; they do not perform the negotiation.
type Adapter interface {
	Name() string
	Versions() []uint32
	Assets() []string
	Health(context.Context) Health
}

// Negotiate validates the adapter's advertised capabilities, checks health,
// and returns the highest shared protocol version and the required assets it
// supports. Degraded adapters negotiate; unhealthy ones do not.
func Negotiate(ctx context.Context, adapter Adapter, request Request) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	name := adapter.Name()
	if name == "" {
		return Session{}, fmt.Errorf("%w: adapter name is required", ErrInvalid)
	}
	versions, err := cleanVersions(adapter.Versions())
	if err != nil {
		return Session{}, err
	}
	if len(versions) == 0 {
		return Session{}, fmt.Errorf("%w: no protocol versions", ErrInvalid)
	}
	adapterAssets, err := cleanAssets(adapter.Assets())
	if err != nil {
		return Session{}, err
	}
	if len(adapterAssets) == 0 {
		return Session{}, fmt.Errorf("%w: no assets", ErrInvalid)
	}
	if len(request.Versions) == 0 {
		return Session{}, fmt.Errorf("%w: request needs a version", ErrInvalid)
	}
	wantedAssets, err := cleanAssets(request.Assets)
	if err != nil || len(wantedAssets) == 0 {
		return Session{}, fmt.Errorf("%w: request needs an asset", ErrInvalid)
	}

	health := adapter.Health(ctx)
	switch health.Status {
	case Healthy, Degraded:
	case Unhealthy:
		return Session{}, fmt.Errorf("%w: %s", ErrUnhealthy, name)
	default:
		return Session{}, fmt.Errorf("%w: health status %q", ErrInvalid, health.Status)
	}

	version, ok := highestCommon(versions, request.Versions)
	if !ok {
		return Session{}, fmt.Errorf("%w: %s", ErrVersion, name)
	}
	assets := intersect(adapterAssets, wantedAssets)
	if len(assets) == 0 {
		return Session{}, fmt.Errorf("%w: %s", ErrAsset, name)
	}
	return Session{Connector: name, Version: version, Assets: assets, Network: request.Network}, nil
}

func cleanVersions(raw []uint32) ([]uint32, error) {
	seen := map[uint32]bool{}
	out := make([]uint32, 0, len(raw))
	for _, v := range raw {
		if v == 0 {
			return nil, fmt.Errorf("%w: protocol versions start at 1", ErrInvalid)
		}
		if seen[v] {
			return nil, fmt.Errorf("%w: version %d", ErrDuplicate, v)
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func cleanAssets(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, asset := range raw {
		if asset == "" {
			return nil, fmt.Errorf("%w: empty asset", ErrInvalid)
		}
		if seen[asset] {
			return nil, fmt.Errorf("%w: asset %q", ErrDuplicate, asset)
		}
		seen[asset] = true
		out = append(out, asset)
	}
	sort.Strings(out)
	return out, nil
}

func highestCommon(adapter, requested []uint32) (uint32, bool) {
	available := make(map[uint32]bool, len(adapter))
	for _, version := range adapter {
		available[version] = true
	}
	var best uint32
	found := false
	for _, version := range requested {
		if available[version] && (!found || version > best) {
			best, found = version, true
		}
	}
	return best, found
}

func intersect(adapter, requested []string) []string {
	wanted := make(map[string]bool, len(requested))
	for _, asset := range requested {
		wanted[asset] = true
	}
	out := make([]string, 0, len(adapter))
	for _, asset := range adapter {
		if wanted[asset] {
			out = append(out, asset)
		}
	}
	sort.Strings(out)
	return out
}
