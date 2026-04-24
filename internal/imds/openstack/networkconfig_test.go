package openstack

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsNICKey(t *testing.T) {
	assert.True(t, isNICKey("net0"))
	assert.True(t, isNICKey("net1"))
	assert.True(t, isNICKey("net12"))
	assert.False(t, isNICKey("services"))
	assert.False(t, isNICKey("eth0"))
	assert.False(t, isNICKey("net"))
	assert.False(t, isNICKey("neta"))
}

// ---------------------------------------------------------------------------
// parseNetworkConfig - absent / invalid
// ---------------------------------------------------------------------------

func TestParseNetworkConfig_Absent(t *testing.T) {
	cfg := parseNetworkConfig("no marker here")
	assert.Nil(t, cfg)
}

func TestParseNetworkConfig_InvalidYAML(t *testing.T) {
	desc := "<!--#network-config\n: [invalid: yaml: {{{\n-->"
	cfg := parseNetworkConfig(desc)
	assert.Nil(t, cfg)
}

func TestParseNetworkConfig_WrongVersion(t *testing.T) {
	desc := "<!--#network-config\nnetwork:\n  version: 3\n-->"
	cfg := parseNetworkConfig(desc)
	assert.Nil(t, cfg)
}

func TestParseNetworkConfig_EmptyBlock(t *testing.T) {
	desc := "<!--#network-config\n-->"
	cfg := parseNetworkConfig(desc)
	assert.Nil(t, cfg)
}

// ---------------------------------------------------------------------------
// V2 parsing
// ---------------------------------------------------------------------------

func TestParseNetworkConfig_V2_DHCP4(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 2
  ethernets:
    net0:
      dhcp4: true
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.True(t, nic.dhcp4)
}

func TestParseNetworkConfig_V2_StaticIPv4(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 2
  ethernets:
    net0:
      addresses:
        - 10.0.0.5/24
      routes:
        - to: 0.0.0.0/0
          via: 10.0.0.1
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	assert.Equal(t, "10.0.0.5", nic.v4Address)
	assert.Equal(t, "255.255.255.0", nic.v4Netmask)
	require.Len(t, nic.v4Routes, 1)
	assert.Equal(t, "10.0.0.1", nic.v4Routes[0].Gateway)
}

func TestParseNetworkConfig_V2_Nameservers(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 2
  ethernets:
    net0:
      dhcp4: true
      nameservers:
        addresses:
          - 8.8.8.8
          - 2001:4860:4860::8888
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	dns := cfg.dnsServices()
	require.Len(t, dns, 2)
	assert.Equal(t, "8.8.8.8", dns[0].Address)
	assert.Equal(t, "2001:4860:4860::8888", dns[1].Address)
}

// ---------------------------------------------------------------------------
// V1 parsing
// ---------------------------------------------------------------------------

func TestParseNetworkConfig_V1_DHCP(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: dhcp
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.True(t, nic.dhcp4)
}

func TestParseNetworkConfig_V1_StaticIPv4(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: static
          address: 10.0.0.5/24
          gateway: 10.0.0.1
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.Equal(t, "10.0.0.5", nic.v4Address)
	assert.Equal(t, "255.255.255.0", nic.v4Netmask)
	require.Len(t, nic.v4Routes, 1)
	assert.Equal(t, "0.0.0.0", nic.v4Routes[0].Network)
	assert.Equal(t, "10.0.0.1", nic.v4Routes[0].Gateway)
}

func TestParseNetworkConfig_V1_StaticIPv4_AddressAndNetmask(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: static
          address: 10.0.0.5
          netmask: 255.255.255.0
          gateway: 10.0.0.1
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.Equal(t, "10.0.0.5", nic.v4Address)
	assert.Equal(t, "255.255.255.0", nic.v4Netmask)
}

func TestParseNetworkConfig_V1_StaticIPv6(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: static6
          address: 2001:db8::10/64
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.Equal(t, "2001:db8::10", nic.v6Address)
	assert.Equal(t, "ffff:ffff:ffff:ffff::", nic.v6Netmask)
}

func TestParseNetworkConfig_V1_DHCP6(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: dhcp6
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.True(t, nic.dhcp6)
}

func TestParseNetworkConfig_V1_SLAAC(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: ipv6_slaac
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.True(t, nic.acceptRA)
}

func TestParseNetworkConfig_V1_Nameserver(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: dhcp
    - type: nameserver
      address:
        - 8.8.8.8
        - 8.8.4.4
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	dns := cfg.dnsServices()
	require.Len(t, dns, 2)
	assert.Equal(t, "8.8.8.8", dns[0].Address)
	assert.Equal(t, "8.8.4.4", dns[1].Address)
}

func TestParseNetworkConfig_V1_MultipleNICs(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: static
          address: 10.0.0.5/24
          gateway: 10.0.0.1
    - type: physical
      name: net1
      subnets:
        - type: dhcp
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	require.Len(t, cfg.nics, 2)
	assert.Equal(t, "10.0.0.5", cfg.nics["net0"].v4Address)
	assert.True(t, cfg.nics["net1"].dhcp4)
}

func TestParseNetworkConfig_V1_StaticWithRoutes(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: static
          address: 10.184.225.122
          netmask: 255.255.255.252
          routes:
            - gateway: 10.184.225.121
              netmask: 255.240.0.0
              destination: 10.176.0.0
            - gateway: 10.184.225.121
              netmask: 255.240.0.0
              destination: 10.208.0.0
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	assert.Equal(t, "10.184.225.122", nic.v4Address)
	assert.Equal(t, "255.255.255.252", nic.v4Netmask)
	require.Len(t, nic.v4Routes, 2)
	assert.Equal(t, "10.176.0.0", nic.v4Routes[0].Network)
	assert.Equal(t, "255.240.0.0", nic.v4Routes[0].Netmask)
	assert.Equal(t, "10.184.225.121", nic.v4Routes[0].Gateway)
	assert.Equal(t, "10.208.0.0", nic.v4Routes[1].Network)
}

func TestParseNetworkConfig_V1_SubnetDNSBecomesService(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: net0
      subnets:
        - type: static
          address: 10.0.0.5/24
          gateway: 10.0.0.1
          dns_nameservers:
            - 8.8.8.8
            - 1.1.1.1
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nics["net0"]
	require.NotNil(t, nic)
	dns := cfg.dnsServices()
	require.Len(t, dns, 2)
	assert.Equal(t, "8.8.8.8", dns[0].Address)
	assert.Equal(t, "1.1.1.1", dns[1].Address)
	for _, r := range nic.v4Routes {
		assert.NotEqual(t, "8.8.8.8", r.Network, "DNS addresses must not appear in routes")
		assert.NotEqual(t, "1.1.1.1", r.Network, "DNS addresses must not appear in routes")
	}
}

func TestParseNetworkConfig_V1_DNSDedup(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: nameserver
      address:
        - 8.8.8.8
    - type: nameserver
      address:
        - 8.8.8.8
        - 1.1.1.1
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	dns := cfg.dnsServices()
	require.Len(t, dns, 2, "duplicate DNS addresses must be deduplicated")
	assert.Equal(t, "8.8.8.8", dns[0].Address)
	assert.Equal(t, "1.1.1.1", dns[1].Address)
}

func TestParseNetworkConfig_V1_NonNICNameWithMAC(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: ens18
      mac_address: '52:54:00:12:34:56'
      subnets:
        - type: dhcp
    - type: nameserver
      address:
        - 8.8.8.8
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	nic := cfg.nicForMAC("52:54:00:12:34:56")
	require.NotNil(t, nic, "entry with guest interface name should match via MAC")
	assert.True(t, nic.dhcp4)
	dns := cfg.dnsServices()
	require.Len(t, dns, 1)
}

func TestParseNetworkConfig_V1_NoMACNoNetNIgnored(t *testing.T) {
	desc := `<!--#network-config
network:
  version: 1
  config:
    - type: physical
      name: eth0
      subnets:
        - type: dhcp
    - type: nameserver
      address:
        - 8.8.8.8
-->`
	cfg := parseNetworkConfig(desc)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.nics, "entry with no MAC and non-netN name should be ignored")
	dns := cfg.dnsServices()
	require.Len(t, dns, 1)
}

// ---------------------------------------------------------------------------
// CIDR helpers
// ---------------------------------------------------------------------------

func TestIPAndMaskFromCIDR_IPv4(t *testing.T) {
	ip, mask, ok := ipAndMaskFromCIDR("10.0.0.5/24")
	require.True(t, ok)
	assert.Equal(t, "10.0.0.5", ip)
	assert.Equal(t, "255.255.255.0", mask)
}

func TestIPAndMaskFromCIDR_IPv6(t *testing.T) {
	ip, mask, ok := ipAndMaskFromCIDR("2001:db8::10/64")
	require.True(t, ok)
	assert.Equal(t, "2001:db8::10", ip)
	assert.Equal(t, "ffff:ffff:ffff:ffff::", mask)
}

func TestIPAndMaskFromCIDR_Invalid(t *testing.T) {
	_, _, ok := ipAndMaskFromCIDR("not-a-cidr")
	assert.False(t, ok)
}

func TestRouteToAndMaskFromCIDR(t *testing.T) {
	network, mask, ok := routeToAndMaskFromCIDR("0.0.0.0/0")
	require.True(t, ok)
	assert.Equal(t, "0.0.0.0", network)
	assert.Equal(t, "0.0.0.0", mask)
}

// ---------------------------------------------------------------------------
// buildNetworks
// ---------------------------------------------------------------------------

func TestBuildNetworks_Nil(t *testing.T) {
	nets, idx := buildNetworks(nil, "net0", 0)
	require.Len(t, nets, 1)
	assert.Equal(t, "ipv4_dhcp", nets[0].Type)
	assert.Equal(t, 1, idx)
}

func TestBuildNetworks_DHCP4(t *testing.T) {
	nets, idx := buildNetworks(&parsedNIC{dhcp4: true}, "net0", 0)
	require.Len(t, nets, 1)
	assert.Equal(t, "ipv4_dhcp", nets[0].Type)
	assert.Equal(t, 1, idx)
}

func TestBuildNetworks_StaticIPv4(t *testing.T) {
	nic := &parsedNIC{
		v4Address: "10.0.0.5",
		v4Netmask: "255.255.255.0",
		v4Routes:  []Route{{Network: "0.0.0.0", Netmask: "0.0.0.0", Gateway: "10.0.0.1"}},
	}
	nets, idx := buildNetworks(nic, "net0", 0)
	require.Len(t, nets, 1)
	assert.Equal(t, "ipv4", nets[0].Type)
	assert.Equal(t, "10.0.0.5", nets[0].IPAddress)
	assert.Equal(t, "255.255.255.0", nets[0].Netmask)
	require.Len(t, nets[0].Routes, 1)
	assert.Equal(t, "10.0.0.1", nets[0].Routes[0].Gateway)
	assert.Equal(t, 1, idx)
}

func TestBuildNetworks_SLAAC(t *testing.T) {
	nets, idx := buildNetworks(&parsedNIC{acceptRA: true}, "net0", 0)
	require.Len(t, nets, 1)
	assert.Equal(t, "ipv6_slaac", nets[0].Type)
	assert.Equal(t, 1, idx)
}

func TestBuildNetworks_DualStack(t *testing.T) {
	nic := &parsedNIC{
		v4Address: "10.0.0.5",
		v4Netmask: "255.255.255.0",
		v6Address: "2001:db8::10",
		v6Netmask: "ffff:ffff:ffff:ffff::",
	}
	nets, idx := buildNetworks(nic, "net0", 0)
	require.Len(t, nets, 2)
	assert.Equal(t, "ipv4", nets[0].Type)
	assert.Equal(t, "ipv6", nets[1].Type)
	assert.Equal(t, 2, idx)
}
