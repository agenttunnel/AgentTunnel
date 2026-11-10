package main

import (
	"net"
	"testing"
)

func TestIsLoopbackListener(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{"ipv4 loopback", "127.0.0.1:0", true},
		{"ipv6 loopback", "[::1]:0", true},
		{"all interfaces ipv4", "0.0.0.0:0", false},
		{"all interfaces ipv6", "[::]:0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", tc.addr)
			if err != nil {
				t.Skipf("cannot listen on %s here: %v", tc.addr, err)
			}
			defer ln.Close()
			if got := isLoopbackListener(ln); got != tc.want {
				t.Fatalf("isLoopbackListener(%s) = %v, want %v", ln.Addr(), got, tc.want)
			}
		})
	}
}
