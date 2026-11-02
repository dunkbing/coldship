// Package netguard keeps outgoing requests that people other than the
// server's admins configure (backup buckets, notification webhooks) off
// the server's own network.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

var notPublic = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// Public reports whether ip is a public unicast address.
func Public(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, n := range notPublic {
		if n.Contains(ip) {
			return false
		}
	}
	return !ip.Equal(net.IPv4bcast)
}

// Control refuses connections to loopback, private, link-local (cloud
// metadata) and other non-public addresses. It runs on the address being
// dialed, after DNS, so a name can't be pointed somewhere else later.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !Public(ip) {
		return fmt.Errorf("%s is not a public address", host)
	}
	return nil
}

// Dialer is a dialer that only reaches public addresses.
func Dialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: Control}
}

// Client is an HTTP client for webhooks: no redirects, a timeout, and, when
// restricted, public addresses only and no proxy.
func Client(restricted bool, timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if restricted {
		tr.Proxy = nil
		tr.DialContext = Dialer().DialContext
	}
	return &http.Client{Transport: tr, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects aren't followed") }}
}
