package pmsd

import (
	"context"
	"errors"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

// NewRegistryDial routes a Dial by the provider registry: SOCKET kinds go unwrapped to the FIAS dial, REST_POLL to
// the REST adapter, and an unknown kind is refused before any I/O.
func NewRegistryDial(fias, rest func(context.Context, DialParams) (Conn, error)) func(context.Context, DialParams) (Conn, error) {
	return func(ctx context.Context, p DialParams) (Conn, error) {
		prov, ok := pmsprovider.Get(p.Rev.ConnectorKind)
		if !ok {
			return nil, coded(CodeRevisionInvalid, errors.New("unsupported connector kind"))
		}
		switch prov.Transport {
		case pmsprovider.TransportSocket:
			return fias(ctx, p)
		case pmsprovider.TransportRESTPoll:
			if rest == nil {
				return nil, coded(CodeConfigInvalid, errors.New("REST connector not wired"))
			}
			return rest(ctx, p)
		}
		return nil, coded(CodeRevisionInvalid, errors.New("unsupported connector transport"))
	}
}
