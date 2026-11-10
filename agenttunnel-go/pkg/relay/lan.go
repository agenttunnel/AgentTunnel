package relay

import (
	"net"
	"strconv"
)

// LANHost is this machine's first private IPv4 address, the one a phone on the
// same network can reach. It returns "" when there is none. Interface addresses
// are read on each call, since a laptop can move between networks.
func LANHost() string {
	return lanHost(net.InterfaceAddrs)
}

// LANURL is the relay URL a phone on the LAN can use for a relay listening on
// listen. A wildcard listen address resolves to this machine's LAN host, a
// specific address is used as is, and loopback gives "" because no phone can
// reach it.
func LANURL(listen net.Addr) string {
	return LANURLWith(listen, LANHost)
}

// LANURLWith is LANURL with the LAN host lookup injected. Tests and callers
// that own their own address source use it.
func LANURLWith(listen net.Addr, lan func() string) string {
	tcp, ok := listen.(*net.TCPAddr)
	if !ok {
		return ""
	}
	host := ""
	switch {
	case tcp.IP.IsUnspecified():
		host = lan()
	case tcp.IP.IsLoopback():
		return ""
	default:
		host = tcp.IP.String()
	}
	if host == "" {
		return ""
	}
	return "ws://" + net.JoinHostPort(host, strconv.Itoa(tcp.Port))
}

// lanHost picks the first non-loopback, non-link-local, private IPv4 address.
// It skips VPN and public addresses, so the choice stays on the local network.
// list is injectable so tests can supply interfaces.
func lanHost(list func() ([]net.Addr, error)) string {
	addrs, err := list()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || !ip.IsPrivate() {
			continue
		}
		return ip.String()
	}
	return ""
}
