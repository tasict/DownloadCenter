package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// The strict rules are for connections whose target someone else chose
// (code of a search plugin): only public unicast addresses, only the web
// ports, and the address that was checked is the one connected to.

// ErrNotAllowed is a target the strict rules refuse.
var ErrNotAllowed = errors.New("address not allowed")

// strictNets are the ranges Strict refuses besides Forbidden: private,
// shared, documentation, benchmark, reserved and transition ranges, whose
// addresses are not public hosts or lead to some other network.
var strictNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{
		"0.0.0.0/8",       // this network
		"10.0.0.0/8",      // RFC 1918
		"100.64.0.0/10",   // carrier-grade NAT
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local
		"172.16.0.0/12",   // RFC 1918
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.88.99.0/24",  // 6to4 relay
		"192.168.0.0/16",  // RFC 1918
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, broadcast
		"::/96",           // unspecified, loopback, IPv4-compatible
		"::ffff:0:0:0/96", // IPv4-translated (IPv4-mapped ones are judged as IPv4)
		"64:ff9b::/96",    // NAT64
		"64:ff9b:1::/48",  // local NAT64
		"100::/64",        // discard
		"2001::/23",       // IETF protocol assignments (Teredo included)
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4
		"fc00::/7",        // unique local
		"fe80::/10",       // link-local
		"fec0::/10",       // site-local
		"ff00::/8",        // multicast
	} {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}()

// Strict reports an address the strict rules refuse: everything Forbidden
// refuses (loopback, link-local, unspecified, multicast, the NAS's own
// addresses) and every range that is not public unicast (RFC 1918, unique
// local, carrier-grade NAT, reserved and transition ranges). IPv4-mapped
// IPv6 addresses are judged as the IPv4 address they carry.
func Strict(ip net.IP) bool {
	if ip == nil || (len(ip) != net.IPv4len && len(ip) != net.IPv6len) {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if Forbidden(ip) || ip.IsPrivate() {
		return true
	}
	for _, n := range strictNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// StrictPort reports the ports the strict rules allow: 80 and 443.
func StrictPort(port string) bool { return port == "80" || port == "443" }

// lookupIP resolves a name (tests replace it).
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// ResolveChecked returns the addresses of host (a name, or an address in
// any form the C library takes, see HostIP) when every one of them passes:
// deny refuses none of them. A name with one refused address is refused
// as a whole, so an answer mixing public and internal addresses cannot be
// used to reach the internal one. localhost and its subdomains are refused
// without asking the resolver.
func ResolveChecked(ctx context.Context, host string, deny func(net.IP) bool) ([]net.IP, error) {
	if ip := HostIP(host); ip != nil {
		if deny(ip) {
			return nil, fmt.Errorf("%w: %s", ErrNotAllowed, ip)
		}
		return []net.IP{ip}, nil
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return nil, fmt.Errorf("%w: %s", ErrNotAllowed, host)
	}
	ips, err := lookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no address")
	}
	for _, ip := range ips {
		if deny(ip) {
			return nil, fmt.Errorf("%w: %s", ErrNotAllowed, ip)
		}
	}
	return ips, nil
}

// DialChecked connects to host:port through the addresses ResolveChecked
// accepted, never resolving the name again (no rebinding between the check
// and the connection).
func DialChecked(ctx context.Context, host, port string, deny func(net.IP) bool) (net.Conn, error) {
	ips, err := ResolveChecked(ctx, host, deny)
	if err != nil {
		return nil, err
	}
	return dialRace(ctx, &net.Dialer{Timeout: 15 * time.Second}, "tcp", ips, port)
}

// DialStrict is DialChecked with the strict rules, ports included.
func DialStrict(ctx context.Context, host, port string) (net.Conn, error) {
	if !StrictPort(port) {
		return nil, fmt.Errorf("%w: port %s", ErrNotAllowed, port)
	}
	return DialChecked(ctx, host, port, Strict)
}
