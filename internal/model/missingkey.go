package model

import (
	"context"
	"fmt"
)

// MissingKeyProvider stands in for a keyed provider whose API key is not set
// in the daemon's environment or the project's .env (F1).
//
// Without this, registering the real HTTP client anyway sent every request
// straight to the provider, which answered 401 with its own JSON error body
// -- and that raw body reached the UI verbatim, since nothing between the
// provider and the response surface recognized "no key" as a distinct,
// actionable case. Complete here fails before any network call, with a
// message naming the exact variable to set and the fix (`idea daemon stop`,
// so the next start re-reads the environment).
//
// It deliberately does NOT implement ModelLister: a model-listing call on a
// keyless entry should fall back to the bundled catalog (buildProvidersView's
// `ready` gate already keeps it from calling in here), not fail loudly for a
// feature (browsing models) that does not need the key at all.
type MissingKeyProvider struct {
	name   string
	keyEnv string
}

// NewMissingKeyProvider returns a provider that always fails with a message
// naming keyEnv as the fix, for a provider named name.
func NewMissingKeyProvider(name, keyEnv string) *MissingKeyProvider {
	return &MissingKeyProvider{name: name, keyEnv: keyEnv}
}

// Name reports the provider's registered name.
func (p *MissingKeyProvider) Name() string { return p.name }

// Capabilities returns the zero value: nothing about this provider is ever
// actually used to shape a request, since Complete always fails first.
func (p *MissingKeyProvider) Capabilities() Capabilities { return Capabilities{} }

// Complete always fails, before any HTTP call, naming the missing key and the
// fix.
func (p *MissingKeyProvider) Complete(ctx context.Context, req Request) (Response, error) {
	return Response{}, fmt.Errorf(
		"%s is not set in the daemon's environment or the project's .env; set it and run idea daemon stop",
		p.keyEnv)
}
