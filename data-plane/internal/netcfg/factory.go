package netcfg

// The factory-prepared client network: what an appliance offers to activate before anything has been configured. It is a definition, not a row: nothing
// exists in the database, and nothing serves clients, until a signed site assignment is in place and an administrator activates it.
const (
	FactoryName       = "Client Wi-Fi"
	FactorySubnet     = "10.20.0.0/20"
	FactoryGateway    = "10.20.0.1"
	FactoryPoolStart  = "10.20.0.10"
	FactoryPoolEnd    = "10.20.15.254"
	FactoryDomain     = "guest.local"
	FactoryLeaseSecs  = 3600
	FactoryLeaseMin   = 900
	FactoryLeaseMax   = 7200
	FactoryNetworkTyp = "untagged"
)

// Factory returns the prepared network for a LAN port. The caller supplies the port (never hard-coded) and identity (never hard-coded either).
func Factory(parentInterface string) GuestNetwork {
	return GuestNetwork{
		Name: FactoryName, Enabled: true, NetworkType: FactoryNetworkTyp, ParentInterface: parentInterface,
		GatewayIP: FactoryGateway, SubnetCIDR: FactorySubnet, PrefixLen: 20,
		DHCPMode: DHCPLocal, DNSMode: "appliance", DomainName: FactoryDomain,
		LeaseDefault: FactoryLeaseSecs, LeaseMin: FactoryLeaseMin, LeaseMax: FactoryLeaseMax,
		CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true, ClientIsolation: true,
		Pools: []Pool{{StartIP: FactoryPoolStart, EndIP: FactoryPoolEnd}},
	}
}
