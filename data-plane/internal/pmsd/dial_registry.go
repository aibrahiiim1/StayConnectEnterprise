package pmsd

import (
	"context"
	"errors"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

// NewRegistryDial routes a Dial to the adapter the provider registry names for the revision's connector kind.
//
// A SOCKET connector (protel-fias) is handed, unchanged, to the FIAS dial it has always used: the same
// function value, the same parameters, nothing wrapped around the connection it returns. A REST_POLL connector
// goes to the polled REST adapter. A kind the registry does not know is refused before any I/O.
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
