package openstack

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"github.com/wyattanderson/pve-imds/internal/identity"
)

// MetaData is the JSON body served at /openstack/{version}/meta_data.json.
//
// Field names follow the OpenStack Nova metadata API convention. cloud-init
// requires "uuid" (copies it to "instance-id") and treats "hostname" as
// "local-hostname" via its KEY_COPIES logic.
type MetaData struct {
	// UUID is the instance identifier. Required by cloud-init; must be stable
	// across reboots. We use the Proxmox VMID as a decimal string.
	UUID string `json:"uuid"`

	// Name is the human-readable VM name from the PVE config.
	Name string `json:"name,omitempty"`

	// Hostname becomes "local-hostname" in cloud-init's internal metadata.
	Hostname string `json:"hostname,omitempty"`

	// AvailabilityZone maps to the Proxmox node name. In a cluster, each node
	// acts as a distinct availability zone.
	AvailabilityZone string `json:"availability_zone,omitempty"`

	// LaunchIndex is always 0; Proxmox launches one instance at a time.
	LaunchIndex int `json:"launch_index"`

	// Meta holds arbitrary values exposed as instance metadata.
	// We populate it with PVE tags and the well-known pve:vmid / pve:node
	// identifiers. cloud-init also inspects Meta["dsmode"] to control
	// datasource mode, which we intentionally omit.
	Meta map[string]any `json:"meta,omitempty"`
}

// NetworkData is the JSON body served at /openstack/{version}/network_data.json.
//
// cloud-init's convert_net_json turns this into a network_config that is
// applied to configure all NICs. We emit one link and one DHCPv4 network
// entry per virtual NIC in the VM config.
type NetworkData struct {
	Links    []Link    `json:"links"`
	Networks []Network `json:"networks"`
	Services []Service `json:"services"`
}

// Link describes a physical network device. The "id" is referenced by Network
// entries. cloud-init matches links to guest interfaces via "ethernet_mac_address"
// when no "name" is provided.
type Link struct {
	// ID is the stable identifier for this link, referenced by Network.Link.
	ID string `json:"id"`

	// Type "phy" is treated as a physical (Ethernet) interface by cloud-init.
	Type string `json:"type"`

	// EthernetMACAddress is used by cloud-init to match the link to a guest
	// network interface when no explicit name is given.
	EthernetMACAddress string `json:"ethernet_mac_address"`

	// MTU is the interface MTU. Omitted when zero (cloud-init uses system
	// default, typically 1500).
	MTU int `json:"mtu,omitempty"`
}

// Network describes the IP configuration for a link. We default to DHCPv4
// since Proxmox does not record static IP assignments in the VM config.
// Users can override via the <!--#network-config marker.
type Network struct {
	// ID is a unique identifier for this network entry.
	ID string `json:"id"`

	// Type controls how cloud-init configures the interface. Common values:
	// "ipv4_dhcp", "ipv6_dhcp", "ipv6_slaac", "ipv4", "ipv6".
	Type string `json:"type"`

	// Link references the Link.ID that this network applies to.
	Link string `json:"link"`

	// NetworkID is an opaque identifier for the logical network. We synthesise
	// one from the Proxmox bridge name when available.
	NetworkID string `json:"network_id,omitempty"`

	// IPAddress is the static IP address for "ipv4" or "ipv6" type networks.
	IPAddress string `json:"ip_address,omitempty"`

	// Netmask is the subnet mask for "ipv4" or "ipv6" type networks.
	Netmask string `json:"netmask,omitempty"`

	// Routes is the list of static routes for this network.
	Routes []Route `json:"routes,omitempty"`
}

// Route describes a static route within a Network entry.
type Route struct {
	Network string `json:"network"`
	Netmask string `json:"netmask"`
	Gateway string `json:"gateway"`
}

// Service describes a non-IP network service such as a DNS resolver. We emit
// an empty list; DNS is typically provided via DHCP.
type Service struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

// MetadataFromRecord builds a MetaData document from a resolved VMRecord.
func MetadataFromRecord(rec *identity.VMRecord) MetaData {
	meta := map[string]any{
		"pve:vmid": strconv.Itoa(rec.VMID),
		"pve:node": rec.Node,
	}
	if len(rec.Config.Tags) > 0 {
		meta["pve:tags"] = rec.Config.Tags
	}

	uuid := rec.Config.SMBIOS["uuid"]
	if uuid == "" {
		slog.Warn("smbios1 uuid not found in VM config, falling back to VMID",
			"vmid", rec.VMID, "node", rec.Node)
		uuid = strconv.Itoa(rec.VMID)
	}

	return MetaData{
		UUID:             uuid,
		Name:             rec.Config.Name,
		Hostname:         rec.Config.Name,
		AvailabilityZone: rec.Node,
		LaunchIndex:      0,
		Meta:             meta,
	}
}

// networkDataFromRecord builds a NetworkData document from a resolved VMRecord.
//
// One link and one DHCPv4 network entry is emitted per virtual NIC, in index
// order (net0, net1, …). cloud-init matches each link to a guest interface by
// MAC address and configures it for DHCP.
func networkDataFromRecord(rec *identity.VMRecord) NetworkData {
	cfg := parseNetworkConfig(rec.Config.Description)

	// Sort indices for deterministic output.
	indices := make([]int, 0, len(rec.Config.Networks))
	for idx := range rec.Config.Networks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	links := make([]Link, 0, len(indices))
	networks := make([]Network, 0, len(indices)*2)
	netIdx := 0

	for _, idx := range indices {
		nic := rec.Config.Networks[idx]
		linkID := fmt.Sprintf("net%d", idx)

		mtu := nic.MTU
		// MTU == 0 means "inherit from bridge" (Proxmox default ≈ 1500).
		// Omit from the document so cloud-init uses the system default.
		mtu = max(mtu, 0)

		links = append(links, Link{
			ID:                 linkID,
			Type:               "phy",
			EthernetMACAddress: nic.MAC.String(),
			MTU:                mtu,
		})

		parsed := cfg.nicForMAC(nic.MAC.String())
		if parsed == nil {
			parsed = cfg.nicForName(linkID)
		}
		nicNets, nextIdx := buildNetworks(parsed, linkID, netIdx)
		for i := range nicNets {
			if nic.Bridge != "" {
				nicNets[i].NetworkID = fmt.Sprintf("pve-%s-%d", nic.Bridge, netIdx+i)
			}
		}
		networks = append(networks, nicNets...)
		netIdx = nextIdx
	}

	services := cfg.dnsServices()
	if services == nil {
		services = []Service{}
	}

	return NetworkData{
		Links:    links,
		Networks: networks,
		Services: services,
	}
}
