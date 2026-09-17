package fetch

// Internal test for the unexported dial-guard glue (peer extraction +
// proxied short-circuit). The external suite can't reach these without
// exporting test-only API; the security decision is worth locking.
import (
	"net"
	"net/netip"
	"testing"
)

func TestDialPeerAllowed(t *testing.T) {
	loopback := net.TCPAddr{IP: net.ParseIP("127.0.0.1")}
	public := net.TCPAddr{IP: net.ParseIP("93.184.216.34")}
	weird := net.TCPAddr{IP: []byte{1, 2, 3}} // malformed → fail closed
	strict := SSRFOptions{}
	relaxed := SSRFOptions{AllowPrivate: true}

	cases := []struct {
		name    string
		addr    net.Addr
		opts    SSRFOptions
		proxied bool
		want    bool
	}{
		{"public direct strict", &public, strict, false, true},
		{"loopback direct strict", &loopback, strict, false, false},
		{"loopback direct relaxed", &loopback, relaxed, false, true},
		{"loopback proxied strict", &loopback, strict, true, true},
		{"public proxied strict", &public, strict, true, true},
		{"malformed addr strict", &weird, strict, false, false},
		{"loopback v6 direct strict", &net.TCPAddr{IP: net.ParseIP("::1")}, strict, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dialPeerAllowed(tc.addr, tc.opts, tc.proxied); got != tc.want {
				t.Errorf("dialPeerAllowed(%s, %+v, proxied=%v) = %v, want %v", tc.addr, tc.opts, tc.proxied, got, tc.want)
			}
		})
	}
	// The predicate underneath: 4-in-6 mapped loopback stays rejected.
	mapped := netip.MustParseAddr("::ffff:127.0.0.1")
	if isPublicIP(mapped) {
		t.Error("::ffff:127.0.0.1 classified public")
	}
}
