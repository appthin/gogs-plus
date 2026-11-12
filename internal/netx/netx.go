package netx

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

var localCIDRs []*net.IPNet

func init() {
	// Parsing hardcoded CIDR strings should never fail, if in case it does, let's
	// fail it at start.
	rawCIDRs := []string{
		// https://datatracker.ietf.org/doc/html/rfc5735:
		"127.0.0.0/8",        // Loopback
		"0.0.0.0/8",          // "This" network
		"100.64.0.0/10",      // Shared address space
		"169.254.0.0/16",     // Link local
		"172.16.0.0/12",      // Private-use networks
		"192.0.0.0/24",       // IETF Protocol assignments
		"192.0.2.0/24",       // TEST-NET-1
		"192.88.99.0/24",     // 6to4 Relay anycast
		"192.168.0.0/16",     // Private-use networks
		"198.18.0.0/15",      // Network interconnect
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"255.255.255.255/32", // Limited broadcast

		// https://datatracker.ietf.org/doc/html/rfc1918:
		"10.0.0.0/8", // Private-use networks

		// https://datatracker.ietf.org/doc/html/rfc6890:
		"::/128",    // Unspecified address, connecting to it reaches the local host
		"::1/128",   // Loopback
		"FC00::/7",  // Unique local address
		"FE80::/10", // Link-local unicast
	}
	for _, raw := range rawCIDRs {
		_, cidr, err := net.ParseCIDR(raw)
		if err != nil {
			panic(fmt.Sprintf("parse CIDR %q: %v", raw, err))
		}
		localCIDRs = append(localCIDRs, cidr)
	}
}

// IsBlockedLocalHostname returns true if given hostname is resolved to a local
// network address that is implicitly blocked (i.e. not exempted from the
// allowlist).
func IsBlockedLocalHostname(hostname string, allowlist []string) bool {
	for _, allow := range allowlist {
		if hostname == allow || allow == "*" {
			return false
		}
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		return true
	}
	for _, ip := range ips {
		for _, cidr := range localCIDRs {
			if cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// ClientIP returns the client IP address of the request. Forwarding headers
// are only trusted when the remote address belongs to one of the trusted proxy
// networks, and the returned address is always normalized to its IPv4 form
// when it is an IPv4-mapped IPv6 address.
func ClientIP(r *http.Request, trusted []*net.IPNet) string {
	remoteIP, ok := parseRemoteAddr(r.RemoteAddr)
	if !ok {
		return ""
	}

	if !containsIP(trusted, remoteIP) {
		return remoteIP.String()
	}

	for _, value := range r.Header.Values("X-Forwarded-For") {
		hops := strings.Split(value, ",")
		// Walk right to left so a client-supplied prefix cannot bypass the
		// trusted proxies that actually forwarded the request.
		for i := len(hops) - 1; i >= 0; i-- {
			hop := strings.TrimSpace(hops[i])
			if hop == "" {
				continue
			}

			ip, ok := parseIP(hop)
			if !ok {
				continue
			}
			if !containsIP(trusted, ip) {
				return ip.String()
			}
		}
	}

	if ip, ok := parseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ok {
		return ip.String()
	}
	return remoteIP.String()
}

// parseRemoteAddr parses the host part of a remote address that carries a port,
// e.g., "127.0.0.1:8080" or "[::1]:8080".
func parseRemoteAddr(address string) (net.IP, bool) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, false
	}
	return parseIP(host)
}

// parseIP parses a bare IP address, stripping any IPv6 zone and normalizing an
// IPv4-mapped IPv6 address to its IPv4 form.
func parseIP(host string) (net.IP, bool) {
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return nil, false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4, true
	}
	return ip, true
}

func containsIP(networks []*net.IPNet, ip net.IP) bool {
	for _, network := range networks {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
