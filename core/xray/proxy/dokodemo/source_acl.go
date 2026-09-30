package dokodemo

import (
	"fmt"
	"net/netip"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/headers/noop"
	"github.com/xtls/xray-core/transport/internet/tcp"
	"github.com/xtls/xray-core/transport/internet/tls"
)

// ParseSourceCIDRs bounds configuration cost and keeps mapped IPv4 rules unambiguous.
func ParseSourceCIDRs(values []string) ([]netip.Prefix, error) {
	if len(values) > 256 {
		return nil, fmt.Errorf("Tunnel source ACL accepts at most 256 CIDRs")
	}
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("invalid Tunnel source CIDR %q; use native IPv4 or IPv6 CIDRs", value)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// ValidateSourceACLPorts also rejects typed receivers that select Unix workers.
func ValidateSourceACLPorts(ports *net.PortList) error {
	if ports == nil || len(ports.Range) == 0 {
		return fmt.Errorf("Tunnel source ACL requires TCP/UDP listener ports")
	}
	for _, ports := range ports.Range {
		if ports.GetFrom() == 0 || ports.GetTo() < ports.GetFrom() || ports.GetTo() > 65535 {
			return fmt.Errorf("Tunnel source ACL requires listener ports in 1..65535")
		}
	}
	return nil
}

// ValidateSourceACLTransport rejects wrappers that can replace the physical peer.
func ValidateSourceACLTransport(stream *internet.MemoryStreamConfig, listen net.Address) error {
	if stream == nil || stream.ProtocolName != "tcp" {
		return fmt.Errorf("Tunnel source ACL requires raw TCP/UDP transport")
	}
	if listen != nil && listen.Family().IsDomain() && listen.Domain() != "localhost" {
		return fmt.Errorf("Tunnel source ACL requires an IP listener, not a Unix socket")
	}
	if stream.SocketSettings.GetAcceptProxyProtocol() {
		return fmt.Errorf("Tunnel source ACL cannot accept PROXY protocol")
	}
	transport, ok := stream.ProtocolSettings.(*tcp.Config)
	if !ok || transport == nil {
		return fmt.Errorf("Tunnel source ACL requires known raw TCP settings")
	}
	if transport.AcceptProxyProtocol {
		return fmt.Errorf("Tunnel source ACL cannot accept PROXY protocol")
	}
	if transport.HeaderSettings != nil {
		header, err := transport.HeaderSettings.GetInstance()
		if _, ok := header.(*noop.ConnectionConfig); err != nil || !ok {
			return fmt.Errorf("Tunnel source ACL does not support this TCP header")
		}
	}
	if stream.TcpmaskManager != nil || stream.UdpmaskManager != nil {
		return fmt.Errorf("Tunnel source ACL does not support transport masks")
	}
	if stream.SecurityType != "" {
		if config, ok := stream.SecuritySettings.(*tls.Config); !ok || config == nil {
			return fmt.Errorf("Tunnel source ACL supports only ordinary TLS security")
		}
	}
	return nil
}

func (d *DokodemoDoor) sourceAllowed(network net.Network, remote net.Addr) bool {
	if network != net.Network_TCP && network != net.Network_UDP {
		return false
	}
	var peer netip.Addr
	switch remote := remote.(type) {
	case *net.TCPAddr:
		if remote != nil {
			peer = remote.AddrPort().Addr()
		}
	case *net.UDPAddr:
		if remote != nil {
			peer = remote.AddrPort().Addr()
		}
	}
	peer = peer.Unmap().WithZone("")
	if !peer.IsValid() {
		return false
	}
	for _, prefix := range d.allowedSourcePrefixes {
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}
