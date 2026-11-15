package relay

import (
	"errors"
	"net"
	"testing"
)

func ipNet(s string) net.Addr {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestLANHostPicksFirstPrivateIPv4(t *testing.T) {
	list := func() ([]net.Addr, error) {
		return []net.Addr{
			ipNet("127.0.0.1/8"),
			ipNet("fe80::1/64"),
			ipNet("169.254.10.2/16"),
			ipNet("100.64.0.5/10"),  // CGNAT, not private: a VPN overlay
			ipNet("203.0.113.9/24"), // public documentation range
			ipNet("192.168.1.42/24"),
			ipNet("10.0.0.7/8"),
		}, nil
	}
	if got := lanHost(list); got != "192.168.1.42" {
		t.Fatalf("lanHost = %q, want 192.168.1.42", got)
	}
}

func TestLANHostEmptyWhenNoPrivateAddress(t *testing.T) {
	list := func() ([]net.Addr, error) {
		return []net.Addr{ipNet("127.0.0.1/8"), ipNet("169.254.1.1/16"), ipNet("8.8.8.8/32")}, nil
	}
	if got := lanHost(list); got != "" {
		t.Fatalf("lanHost = %q, want empty", got)
	}
}

func TestLANHostEmptyOnInterfaceError(t *testing.T) {
	list := func() ([]net.Addr, error) { return nil, errors.New("no interfaces") }
	if got := lanHost(list); got != "" {
		t.Fatalf("lanHost = %q, want empty", got)
	}
}

func TestLANURL(t *testing.T) {
	tests := []struct {
		name   string
		listen net.Addr
		host   string
		want   string
	}{
		{
			name:   "wildcard uses the LAN host",
			listen: &net.TCPAddr{IP: net.IPv4zero, Port: 7777},
			host:   "192.168.1.42",
			want:   "ws://192.168.1.42:7777",
		},
		{
			name:   "wildcard without a LAN host gives nothing",
			listen: &net.TCPAddr{IP: net.IPv6unspecified, Port: 7777},
			host:   "",
			want:   "",
		},
		{
			name:   "specific LAN address is used as is",
			listen: &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 9000},
			want:   "ws://10.1.2.3:9000",
		},
		{
			name:   "loopback is unreachable from a phone",
			listen: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7777},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LANURLWith(tt.listen, func() string { return tt.host })
			if got != tt.want {
				t.Fatalf("LANURL = %q, want %q", got, tt.want)
			}
		})
	}
}
