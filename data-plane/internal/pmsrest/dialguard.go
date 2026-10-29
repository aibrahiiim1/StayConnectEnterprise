package pmsrest

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

var errForbiddenAddress = errors.New("the address is loopback or link-local")

// refuseInternalAddress is a net.Dialer Control hook. It runs on the address actually about to be connected to, after
// name resolution, so a host name that resolves to 127.0.0.1 or 169.254.x.x is refused exactly like a literal one
// (audit AD-40). RFC 1918 addresses pass: an on-premises PMS lives there.
func refuseInternalAddress(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return errForbiddenAddress
	}
	if pmsprovider.IPRefusal(ap.Addr()) != "" {
		return errForbiddenAddress
	}
	return nil
}

func guardedTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: refuseInternalAddress}).DialContext
	return base
}
