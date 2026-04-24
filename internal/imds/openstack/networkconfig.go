package openstack

import (
	"fmt"
	"log/slog"
	"net"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wyattanderson/pve-imds/internal/imds"
)

type parsedNIC struct {
	dhcp4     bool
	dhcp6     bool
	acceptRA  bool
	v4Address string
	v4Netmask string
	v6Address string
	v6Netmask string
	v4Routes  []Route
	v6Routes  []Route
}

type parsedConfig struct {
	nics     map[string]*parsedNIC
	services []Service
}

type networkWrapper struct {
	Network rawNetwork `yaml:"network"`
}

type rawNetwork struct {
	Version   int                  `yaml:"version"`
	Ethernets map[string]yaml.Node `yaml:"ethernets,omitempty"`
	Config    []yaml.Node          `yaml:"config,omitempty"`
}

func parseNetworkConfig(description string) *parsedConfig {
	raw, ok := imds.ParseNetworkConfig(description)
	if !ok {
		return nil
	}

	var wrapper networkWrapper
	if err := yaml.Unmarshal([]byte(raw), &wrapper); err != nil {
		slog.Warn("failed to parse network-config, falling back to DHCP", "error", err)
		return nil
	}

	switch wrapper.Network.Version {
	case 1:
		return parseV1(wrapper.Network.Config)
	case 2:
		return parseV2(wrapper.Network.Ethernets)
	default:
		slog.Warn("network-config version must be 1 or 2, falling back to DHCP",
			"version", wrapper.Network.Version)
		return nil
	}
}

type v1ConfigEntry struct {
	Type       string     `yaml:"type"`
	Name       string     `yaml:"name,omitempty"`
	MACAddress string     `yaml:"mac_address,omitempty"`
	Subnets    []v1Subnet `yaml:"subnets,omitempty"`
	Address    []string   `yaml:"address,omitempty"`
	Search     []string   `yaml:"search,omitempty"`
}

type v1Subnet struct {
	Type           string    `yaml:"type"`
	Address        string    `yaml:"address,omitempty"`
	Netmask        string    `yaml:"netmask,omitempty"`
	Gateway        string    `yaml:"gateway,omitempty"`
	DNSNameservers []string  `yaml:"dns_nameservers,omitempty"`
	Routes         []v1Route `yaml:"routes,omitempty"`
}

type v1Route struct {
	Destination string `yaml:"destination"`
	Gateway     string `yaml:"gateway,omitempty"`
	Netmask     string `yaml:"netmask,omitempty"`
	Metric      *int   `yaml:"metric,omitempty"`
}

func parseV1(configNodes []yaml.Node) *parsedConfig {
	cfg := &parsedConfig{
		nics:     make(map[string]*parsedNIC),
		services: nil,
	}
	seen := make(map[string]bool)

	for _, node := range configNodes {
		var entry v1ConfigEntry
		if err := node.Decode(&entry); err != nil {
			slog.Debug("failed to decode v1 network-config entry, skipping", "error", err)
			continue
		}

		switch entry.Type {
		case "physical":
			nic := &parsedNIC{}
			for _, sub := range entry.Subnets {
				applyV1Subnet(cfg, seen, nic, sub)
			}

			if entry.MACAddress != "" {
				cfg.nics[normalizeMAC(entry.MACAddress)] = nic
			}
			if isNICKey(entry.Name) {
				cfg.nics[entry.Name] = nic
			}

		case "nameserver":
			for _, addr := range entry.Address {
				if !seen[addr] {
					seen[addr] = true
					cfg.services = append(cfg.services, Service{Type: "dns", Address: addr})
				}
			}
		}
	}

	return cfg
}

func applyV1Subnet(cfg *parsedConfig, seen map[string]bool, nic *parsedNIC, sub v1Subnet) {
	switch sub.Type {
	case "dhcp", "dhcp4":
		nic.dhcp4 = true
	case "dhcp6":
		nic.dhcp6 = true
	case "ipv6_slaac":
		nic.acceptRA = true
	case "ipv6_dhcpv6-stateless":
		nic.acceptRA = true
	case "ipv6_dhcpv6-stateful":
		nic.dhcp6 = true
	case "static":
		ip, mask := parseV1Address(sub.Address, sub.Netmask)
		nic.v4Address = ip
		nic.v4Netmask = mask
		if sub.Gateway != "" {
			nic.v4Routes = append(nic.v4Routes, Route{
				Network: "0.0.0.0",
				Netmask: "0.0.0.0",
				Gateway: sub.Gateway,
			})
		}
		for _, r := range sub.Routes {
			dest, mask := parseV1RouteDest(r.Destination, r.Netmask)
			nic.v4Routes = append(nic.v4Routes, Route{
				Network: dest,
				Netmask: mask,
				Gateway: r.Gateway,
			})
		}
		for _, ns := range sub.DNSNameservers {
			if !seen[ns] {
				seen[ns] = true
				cfg.services = append(cfg.services, Service{Type: "dns", Address: ns})
			}
		}
	case "static6":
		ip, mask := parseV1Address(sub.Address, sub.Netmask)
		nic.v6Address = ip
		nic.v6Netmask = mask
		if sub.Gateway != "" {
			nic.v6Routes = append(nic.v6Routes, Route{
				Network: "::",
				Netmask: "::",
				Gateway: sub.Gateway,
			})
		}
		for _, r := range sub.Routes {
			dest, mask := parseV1RouteDest(r.Destination, r.Netmask)
			nic.v6Routes = append(nic.v6Routes, Route{
				Network: dest,
				Netmask: mask,
				Gateway: r.Gateway,
			})
		}
	}
}

func parseV1Address(address, netmask string) (string, string) {
	if address == "" {
		return "", ""
	}
	if netmask != "" {
		return address, netmask
	}
	ip, mask, ok := ipAndMaskFromCIDR(address)
	if ok {
		return ip, mask
	}
	return address, ""
}

func parseV1RouteDest(destination, netmask string) (string, string) {
	if netmask != "" {
		return destination, netmask
	}
	dest, mask, ok := routeToAndMaskFromCIDR(destination)
	if ok {
		return dest, mask
	}
	return destination, netmask
}

type v2Ethernet struct {
	DHCP4       *bool          `yaml:"dhcp4,omitempty"`
	DHCP6       *bool          `yaml:"dhcp6,omitempty"`
	AcceptRA    *bool          `yaml:"accept-ra,omitempty"`
	Addresses   []string       `yaml:"addresses,omitempty"`
	Routes      []v2Route      `yaml:"routes,omitempty"`
	Nameservers *v2Nameservers `yaml:"nameservers,omitempty"`
	Match       *v2Match       `yaml:"match,omitempty"`
}

type v2Match struct {
	MACAddress string `yaml:"macaddress"`
}

type v2Route struct {
	To     string `yaml:"to"`
	Via    string `yaml:"via,omitempty"`
	Metric *int   `yaml:"metric,omitempty"`
}

type v2Nameservers struct {
	Addresses []string `yaml:"addresses,omitempty"`
	Search    []string `yaml:"search,omitempty"`
}

func parseV2(ethernets map[string]yaml.Node) *parsedConfig {
	cfg := &parsedConfig{
		nics:     make(map[string]*parsedNIC),
		services: nil,
	}

	seen := make(map[string]bool)
	for name, node := range ethernets {
		var eth v2Ethernet
		if err := node.Decode(&eth); err != nil {
			slog.Debug("failed to decode v2 ethernet entry, skipping", "name", name, "error", err)
			continue
		}

		nic := &parsedNIC{}
		if eth.DHCP4 != nil && *eth.DHCP4 {
			nic.dhcp4 = true
		}
		if eth.DHCP6 != nil && *eth.DHCP6 {
			nic.dhcp6 = true
		}
		if eth.AcceptRA != nil && *eth.AcceptRA {
			nic.acceptRA = true
		}

		var v4Routes []Route
		var v6Routes []Route
		for _, r := range eth.Routes {
			dest, mask, ok := routeToAndMaskFromCIDR(r.To)
			if !ok {
				continue
			}
			route := Route{Network: dest, Netmask: mask, Gateway: r.Via}
			if isIPv4CIDR(r.To) {
				v4Routes = append(v4Routes, route)
			} else {
				v6Routes = append(v6Routes, route)
			}
		}

		for _, addr := range eth.Addresses {
			if isIPv4CIDR(addr) {
				ip, mask, _ := ipAndMaskFromCIDR(addr)
				nic.v4Address = ip
				nic.v4Netmask = mask
				nic.v4Routes = v4Routes
			} else if isIPv6CIDR(addr) {
				ip, mask, _ := ipAndMaskFromCIDR(addr)
				nic.v6Address = ip
				nic.v6Netmask = mask
				nic.v6Routes = v6Routes
			}
		}

		if eth.Match != nil && eth.Match.MACAddress != "" {
			cfg.nics[normalizeMAC(eth.Match.MACAddress)] = nic
		}
		cfg.nics[name] = nic

		if eth.Nameservers != nil {
			for _, addr := range eth.Nameservers.Addresses {
				if !seen[addr] {
					seen[addr] = true
					cfg.services = append(cfg.services, Service{Type: "dns", Address: addr})
				}
			}
		}
	}

	return cfg
}

func normalizeMAC(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func isNICKey(s string) bool {
	if len(s) < 4 || s[:3] != "net" {
		return false
	}
	for _, c := range s[3:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isIPv4CIDR(s string) bool {
	ip, _, err := net.ParseCIDR(s)
	if err != nil {
		return false
	}
	return ip.To4() != nil
}

func isIPv6CIDR(s string) bool {
	ip, _, err := net.ParseCIDR(s)
	if err != nil {
		return false
	}
	return ip.To4() == nil
}

func ipAndMaskFromCIDR(cidr string) (string, string, bool) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", false
	}
	mask := net.IP(ipNet.Mask)
	if ip.To4() != nil {
		return ip.String(), mask.To4().String(), true
	}
	return ip.String(), mask.String(), true
}

func routeToAndMaskFromCIDR(cidr string) (string, string, bool) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", false
	}
	network := ipNet.IP.String()
	mask := net.IP(ipNet.Mask)
	if ipNet.IP.To4() != nil {
		return network, mask.To4().String(), true
	}
	return network, mask.String(), true
}

func (p *parsedConfig) nicForMAC(mac string) *parsedNIC {
	if p == nil {
		return nil
	}
	key := normalizeMAC(mac)
	if nic, ok := p.nics[key]; ok {
		return nic
	}
	return nil
}

func (p *parsedConfig) nicForName(name string) *parsedNIC {
	if p == nil {
		return nil
	}
	return p.nics[name]
}

func (p *parsedConfig) dnsServices() []Service {
	if p == nil || len(p.services) == 0 {
		return nil
	}
	return p.services
}

func buildNetworks(nic *parsedNIC, linkID string, netIdx int) ([]Network, int) {
	if nic == nil {
		return []Network{{
			ID:   fmt.Sprintf("network%d", netIdx),
			Type: "ipv4_dhcp",
			Link: linkID,
		}}, netIdx + 1
	}

	var networks []Network

	if nic.dhcp4 {
		networks = append(networks, Network{
			ID:   fmt.Sprintf("network%d", netIdx),
			Type: "ipv4_dhcp",
			Link: linkID,
		})
		netIdx++
	}

	if nic.dhcp6 {
		networks = append(networks, Network{
			ID:   fmt.Sprintf("network%d", netIdx),
			Type: "ipv6_dhcp",
			Link: linkID,
		})
		netIdx++
	}

	if nic.acceptRA && !nic.dhcp6 && nic.v6Address == "" {
		networks = append(networks, Network{
			ID:   fmt.Sprintf("network%d", netIdx),
			Type: "ipv6_slaac",
			Link: linkID,
		})
		netIdx++
	}

	if nic.v4Address != "" && !nic.dhcp4 {
		networks = append(networks, Network{
			ID:        fmt.Sprintf("network%d", netIdx),
			Type:      "ipv4",
			Link:      linkID,
			IPAddress: nic.v4Address,
			Netmask:   nic.v4Netmask,
			Routes:    nic.v4Routes,
		})
		netIdx++
	}

	if nic.v6Address != "" {
		networks = append(networks, Network{
			ID:        fmt.Sprintf("network%d", netIdx),
			Type:      "ipv6",
			Link:      linkID,
			IPAddress: nic.v6Address,
			Netmask:   nic.v6Netmask,
			Routes:    nic.v6Routes,
		})
		netIdx++
	}

	if len(networks) == 0 {
		networks = append(networks, Network{
			ID:   fmt.Sprintf("network%d", netIdx),
			Type: "ipv4_dhcp",
			Link: linkID,
		})
		netIdx++
	}

	return networks, netIdx
}
