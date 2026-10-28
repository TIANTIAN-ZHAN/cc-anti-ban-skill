package health

import "net/netip"

// Conservative fixed-target policy: exclude every special-purpose IPv4 block,
// including globally reachable protocol anycast. A transport check has no reason
// to contact these ranges. Source: IANA IPv4 Special-Purpose Address Registry
// (2025-10-09 revision), plus multicast. Enterprise/private targets require a
// separate product authorization flow and are unsupported by this policy.
var specialIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"), netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
}

func publicIPv4(value string) bool {
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range specialIPv4 {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
