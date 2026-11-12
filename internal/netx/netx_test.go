package netx

import (
	"net"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsLocalHostname(t *testing.T) {
	tests := []struct {
		hostname  string
		allowlist []string
		want      bool
	}{
		{hostname: "localhost", want: true},       // #00
		{hostname: "127.0.0.1", want: true},       // #01
		{hostname: "::1", want: true},             // #02
		{hostname: "0:0:0:0:0:0:0:1", want: true}, // #03
		{hostname: "127.0.0.95", want: true},      // #04
		{hostname: "0.0.0.0", want: true},         // #05
		{hostname: "192.168.123.45", want: true},  // #06

		{hostname: "gogs.io", want: false},         // #07
		{hostname: "google.com", want: false},      // #08
		{hostname: "165.232.140.255", want: false}, // #09

		{hostname: "192.168.123.45", allowlist: []string{"10.0.0.17"}, want: true}, // #10
		{hostname: "gogs.local", allowlist: []string{"gogs.local"}, want: false},   // #11

		{hostname: "192.168.123.45", allowlist: []string{"*"}, want: false}, // #12

		{hostname: "::", want: true},              // #13
		{hostname: "0:0:0:0:0:0:0:0", want: true}, // #14
	}
	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			assert.Equal(t, test.want, IsBlockedLocalHostname(test.hostname, test.allowlist))
		})
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        []string
		xRealIP    string
		trusted    []string
		want       string
	}{
		{
			name:       "untrusted remote ignores spoofed forwarding headers",
			remoteAddr: "198.51.100.5:1234",
			xff:        []string{"203.0.113.7"},
			xRealIP:    "203.0.113.8",
			trusted:    []string{"10.0.0.0/8"},
			want:       "198.51.100.5",
		},
		{
			name:       "nil trusted networks ignores forwarding headers",
			remoteAddr: "198.51.100.5:1234",
			xff:        []string{"203.0.113.7"},
			want:       "198.51.100.5",
		},
		{
			name:       "trusted remote returns forwarded address",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"203.0.113.7"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "rightmost non-trusted hop wins",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"203.0.113.7, 10.0.0.1"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "leftmost entry is skipped when a rightmost one is public",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"1.2.3.4, 203.0.113.9"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.9",
		},
		{
			name:       "all trusted hops fall back to x-real-ip",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"10.0.0.1, 10.0.0.2"},
			xRealIP:    "203.0.113.9",
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.9",
		},
		{
			name:       "x-real-ip used when x-forwarded-for is absent",
			remoteAddr: "10.0.0.5:1234",
			xRealIP:    "203.0.113.9",
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.9",
		},
		{
			name:       "empty segments and unparseable hops are skipped",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"203.0.113.7, , garbage, 10.0.0.1"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "multiple x-forwarded-for headers are joined",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"203.0.113.7", "10.0.0.1"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "ipv4-mapped ipv6 remote is normalized",
			remoteAddr: "[::ffff:198.51.100.5]:1234",
			xff:        []string{"203.0.113.7"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "198.51.100.5",
		},
		{
			name:       "ipv4-mapped ipv6 trusted remote honors forwarding",
			remoteAddr: "[::ffff:10.0.0.5]:1234",
			xff:        []string{"203.0.113.7"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "ipv4-mapped ipv6 forwarded entry is normalized",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"::ffff:203.0.113.7"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "ipv6 remote with bracketed host and port",
			remoteAddr: "[2001:db8::5]:8080",
			xff:        []string{"203.0.113.7"},
			trusted:    []string{"2001:db8::/32"},
			want:       "203.0.113.7",
		},
		{
			name:       "untrusted ipv6 remote is normalized",
			remoteAddr: "[2001:0db8:0000:0000:0000:0000:0000:0005]:8080",
			xff:        []string{"203.0.113.7"},
			want:       "2001:db8::5",
		},
		{
			name:       "ipv6 zone is stripped",
			remoteAddr: "[fe80::1%eth0]:1234",
			want:       "fe80::1",
		},
		{
			name:       "remote address without port returns empty",
			remoteAddr: "198.51.100.5",
			trusted:    []string{"10.0.0.0/8"},
			want:       "",
		},
		{
			name:       "unparseable remote address returns empty",
			remoteAddr: "not-an-ip:1234",
			trusted:    []string{"10.0.0.0/8"},
			want:       "",
		},
		{
			name:       "trusted loopback honors forwarding",
			remoteAddr: "127.0.0.1:1234",
			xff:        []string{"203.0.113.7"},
			trusted:    []string{"127.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "trusted remote without forwarding headers returns remote",
			remoteAddr: "10.0.0.5:1234",
			trusted:    []string{"10.0.0.0/8"},
			want:       "10.0.0.5",
		},
		{
			name:       "trusted remote with unusable headers returns remote",
			remoteAddr: "10.0.0.5:1234",
			xff:        []string{"garbage"},
			xRealIP:    "also-garbage",
			trusted:    []string{"10.0.0.0/8"},
			want:       "10.0.0.5",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = test.remoteAddr
			for _, value := range test.xff {
				r.Header.Add("X-Forwarded-For", value)
			}
			if test.xRealIP != "" {
				r.Header.Set("X-Real-IP", test.xRealIP)
			}

			assert.Equal(t, test.want, ClientIP(r, parseNetworks(t, test.trusted)))
		})
	}
}

func parseNetworks(t *testing.T, cidrs []string) []*net.IPNet {
	t.Helper()
	if cidrs == nil {
		return nil
	}

	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		require.NoError(t, err)
		networks = append(networks, network)
	}
	return networks
}
