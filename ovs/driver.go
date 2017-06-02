package ovs

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	// "github.com/docker/libnetwork/iptables"
	"github.com/gopher-net/dknet"
	"github.com/samalba/dockerclient"
	"github.com/socketplane/libovsdb"
	"github.com/vishvananda/netlink"
)

const (
	defaultRoute     = "0.0.0.0/0"
	ovsPortPrefix    = "ovs-veth0-"
	bridgePrefix     = "ovsbr-"
	containerEthName = "eth"

	optionKey = "com.docker.network.generic"

	mtuOption           = "linker.net.ovs.bridge.mtu"
	modeOption          = "linker.net.ovs.bridge.mode"
	bridgeNameOption    = "linker.net.ovs.bridge.name"
	bindInterfaceOption = "linker.net.ovs.bridge.bind_interface"
	typeOption          = "linker.net.ovs.bridge.type" //"sgw" or "pgw"
	networkNameOption   = "linker.net.ovs.network.name"

	// portMappingKey = "com.docker.network.portmap"

	modeNAT  = "nat"
	modeFlat = "flat"
	type_sgw = "sgw"
	type_pgw = "pgw"

	defaultMTU  = 1500
	defaultMode = modeNAT
)

var (
	validModes = map[string]bool{
		modeNAT:  true,
		modeFlat: true,
	}
)

type Driver struct {
	dknet.Driver
	dockerer
	ovsdber
	networks map[string]*NetworkState
	OvsdbNotifier
}

// NetworkState is filled in at network creation time
// it contains state that we wish to keep for each network
type NetworkState struct {
	BridgeName        string
	MTU               int
	Mode              string
	Gateway           string
	GatewayMask       string
	FlatBindInterface string
	NetworkType       string
	NetworkName       string
}

//CreateNetworkRequest value is :
//{
//  NetworkID:281746a33da5c97b088275925d6dd8b91bd1ba3e7ded0714e2cef47125074e38
//  Options: map[
//                com.docker.network.enable_ipv6:false
//                com.docker.network.generic: map[
//                                                linker.net.ovs.network.name:newovs
//                                                linker.net.ovs.bridge.bind_interface:eth100
//                                                linker.net.ovs.bridge.type:sgw]
//              ]
// IPv4Data:[0xc42011e000]
// IPv6Data:[]
//}
func (d *Driver) CreateNetwork(r *dknet.CreateNetworkRequest) error {
	log.Debugf("Create network request: %+v", r)

	bridgeName, err := getBridgeName(r)
	if err != nil {
		return err
	}

	mtu, err := getBridgeMTU(r)
	if err != nil {
		return err
	}

	mode, err := getBridgeMode(r)
	if err != nil {
		return err
	}

	gateway, mask, err := getGatewayIP(r)
	if err != nil {
		return err
	}

	bindInterface, err := getBindInterface(r)
	if err != nil {
		return err
	}

	networkName, err := getNetworkName(r)
	if err != nil {
		return err
	}

	networktype := getNetworkType(r)

        errc := checkExecutable(networktype, networkName)
	if errc != nil {
		log.Errorf("validate failed, error is %v", errc)
		return errc
	}
        
	ns := &NetworkState{
		BridgeName:        bridgeName,
		MTU:               mtu,
		Mode:              mode,
		Gateway:           gateway,
		GatewayMask:       mask,
		FlatBindInterface: bindInterface,
		NetworkType:       networktype,
		NetworkName:       networkName,
	}
	d.networks[r.NetworkID] = ns

	log.Debugf("Initializing bridge for network %s", r.NetworkID)
	log.Debugf("Network status is %v", *ns)
	if err := d.initBridge(r.NetworkID); err != nil {
		delete(d.networks, r.NetworkID)
		return err
	}

	// d.addBridgeToInterface(bridgeName, bindInterface)

	return nil
}

func checkExecutable(networkType, networkName string) error {
	if !strings.EqualFold(networkType, type_sgw) && !strings.EqualFold(networkType, type_pgw) {
		return nil
	}
	//it's a sgw or pgw type
	if len(networkName) <= 0 {
		log.Errorf("options must specify network name for sgw or pgw type")
		return errors.New("options must specify network name for sgw or pgw type")
	}

	command := "ps -ef | grep /usr/sbin/ovsopt.sh | grep -v grep | wc -l"
	output, _, _ := ExecCommandWithComplete(command)
	if output == "0" {
		return nil
	} else {
		return errors.New("current node already run sgw or pgw process")
	}
}


// func (d *Driver) addBridgeToInterface(bridgeName string, bindInterface string) {
// 	log.Debugf("begin to add ovs bridge %s to interface %s", bridgeName, bindInterface)
// 	err = d.addOvsVethPort(bindInterface, bridgeName, 0)
// 	if err != nil {
// 		log.Errorf("error attaching ovs bridge [ %s ] to interface [ %s ]", bridgeName, bindInterface)
// 		return
// 	}
// 	return
// }

func (d *Driver) DeleteNetwork(r *dknet.DeleteNetworkRequest) error {
	log.Debugf("Delete network request: %+v", r)
	// bridgeName := d.networks[r.NetworkID].BridgeName
	bridgeName := bridgePrefix + truncateID(r.NetworkID)
	log.Debugf("Deleting Bridge %s", bridgeName)
	err := d.deleteBridge(bridgeName)
	if err != nil {
		log.Errorf("Deleting bridge %s failed: %s", bridgeName, err)
		return err
	}
	delete(d.networks, r.NetworkID)
	return nil
}

func (d *Driver) CreateEndpoint(r *dknet.CreateEndpointRequest) error {
	// log.Debugf("Create endpoint request: %+v", r)
	// //add filter and nat rule for container here
	// interfaceobj := *(r.Interface)
	// containerIP := parseContainerIP(interfaceobj.Address)
	// hostPort, containerPort := parsePort(ainterface.Options)
	// log.Infof("hostPort is %s, containerPort is %s", hostPort, containerPort)
	// if hostPort == "" || containerPort == "" {
	// 	return nil
	// } else {

	// }
	return nil
}

func (d *Driver) DeleteEndpoint(r *dknet.DeleteEndpointRequest) error {
	log.Debugf("Delete endpoint request: %+v", r)
	return nil
}

func (d *Driver) EndpointInfo(r *dknet.InfoRequest) (*dknet.InfoResponse, error) {
	res := &dknet.InfoResponse{
		Value: make(map[string]string),
	}
	return res, nil
}

func (d *Driver) Join(r *dknet.JoinRequest) (*dknet.JoinResponse, error) {
	// create and attach local name to the bridge
	log.Debugf("join request is %v", r)
	localVethPair := vethPair(truncateID(r.EndpointID))
	if err := netlink.LinkAdd(localVethPair); err != nil {
		log.Errorf("failed to create the veth pair named: [ %v ] error: [ %s ] ", localVethPair, err)
		return nil, err
	}
	// Bring the veth pair up
	err := netlink.LinkSetUp(localVethPair)
	if err != nil {
		log.Warnf("Error enabling  Veth local iface: [ %v ]", localVethPair)
		return nil, err
	}

	// bridgeName := d.networks[r.NetworkID].BridgeName
	bridgeName := bridgePrefix + truncateID(r.NetworkID)
	err = d.addOvsVethPort(bridgeName, localVethPair.Name, 0)
	if err != nil {
		log.Errorf("error attaching veth [ %s ] to bridge [ %s ]", localVethPair.Name, bridgeName)
		return nil, err
	}
	log.Infof("Attached veth [ %s ] to bridge [ %s ]", localVethPair.Name, bridgeName)

	// SrcName gets renamed to DstPrefix + ID on the container iface
	gatewayIP, err := getIPByInterface(bridgeName)
	if err != nil {
		log.Errorf("error get gateway ip of bridgeName %s", bridgeName)
		return nil, err
	}
	res := &dknet.JoinResponse{
		InterfaceName: dknet.InterfaceName{
			SrcName:   localVethPair.PeerName,
			DstPrefix: containerEthName,
		},
		Gateway: gatewayIP,
	}
	log.Debugf("Join endpoint %s:%s to %s", r.NetworkID, r.EndpointID, r.SandboxKey)
	return res, nil
}

func (d *Driver) Leave(r *dknet.LeaveRequest) error {
	log.Debugf("Leave request: %+v", r)
	localVethPair := vethPair(truncateID(r.EndpointID))
	if err := netlink.LinkDel(localVethPair); err != nil {
		log.Errorf("unable to delete veth on leave: %s", err)
	}
	portID := fmt.Sprintf(ovsPortPrefix + truncateID(r.EndpointID))
	// bridgeName := d.networks[r.NetworkID].BridgeName
	bridgeName := bridgePrefix + truncateID(r.NetworkID)
	err := d.ovsdber.deletePort(bridgeName, portID)
	if err != nil {
		log.Errorf("OVS port [ %s ] delete transaction failed on bridge [ %s ] due to: %s", portID, bridgeName, err)
		return err
	}
	log.Infof("Deleted OVS port [ %s ] from bridge [ %s ]", portID, bridgeName)
	log.Debugf("Leave %s:%s", r.NetworkID, r.EndpointID)
	return nil
}

func NewDriver() (*Driver, error) {
	docker, err := dockerclient.NewDockerClient("unix:///var/run/docker.sock", nil)
	if err != nil {
		return nil, fmt.Errorf("could not connect to docker: %s", err)
	}

	// initiate the ovsdb manager port binding
	var ovsdb *libovsdb.OvsdbClient
	retries := 3
	for i := 0; i < retries; i++ {
		ovsdb, err = libovsdb.Connect(localhost, ovsdbPort)
		if err == nil {
			break
		}
		log.Errorf("could not connect to openvswitch on port [ %d ]: %s. Retrying in 5 seconds", ovsdbPort, err)
		time.Sleep(5 * time.Second)
	}

	if ovsdb == nil {
		return nil, fmt.Errorf("could not connect to open vswitch")
	}

	d := &Driver{
		dockerer: dockerer{
			client: docker,
		},
		ovsdber: ovsdber{
			ovsdb: ovsdb,
		},
		networks: make(map[string]*NetworkState),
	}
	// Initialize ovsdb cache at rpc connection setup
	d.ovsdber.initDBCache()
	return d, nil
}

func getIPByInterface(iname string) (string, error) {
	log.Infof("interface name is %s", iname)
	iface, err := net.InterfaceByName(iname)
	if err != nil {
		log.Errorf("get interfaces by name error %v", err)
		return "", err
	}
	addrs, erra := iface.Addrs()
	if erra != nil {
		log.Errorf("get address by name error %v", erra)
		return "", erra
	}

	log.Infof("the addrs of specific interfaces is %v", addrs)
	if len(addrs) > 0 {
		ip, _, _ := net.ParseCIDR(addrs[0].String())
		return ip.String(), nil
	} else {
		log.Errorf("no ip address on specific interfaces %s", iname)
		return "", errors.New("get ip by interface name error")
	}
}

// func parseContainerIP(fullip string) string {
// 	log.Debugf("the full ip is %s", fullip)
// 	ips := strings.Split(fullip, "/")
// 	ip := ips[0]
// 	log.Infof("the requested container ip is %s", ip)
// 	return ip
// }

// func parsePort(options map[string]interface{}) (string, string) {
// 	if len(options) <= 0 {
// 		log.Infof("no info in options")
// 		return "", ""
// 	}

// 	portMappingIn := options[portMappingKey]
// 	portMapping := portMappingIn.(map[string]string)
// 	if len(portMapping) <= 0 {
// 		log.Infof("no port mapping info")
// 		return "", ""
// 	}

// 	return portMapping["HostPort"], portMapping["Port"]
// }

// Create veth pair. Peername is renamed to eth0 in the container
func vethPair(suffix string) *netlink.Veth {
	return &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: ovsPortPrefix + suffix},
		PeerName:  "ethc" + suffix,
	}
}

// Enable a netlink interface
func interfaceUp(name string) error {
	iface, err := netlink.LinkByName(name)
	if err != nil {
		log.Debugf("Error retrieving a link named [ %s ]", iface.Attrs().Name)
		return err
	}
	return netlink.LinkSetUp(iface)
}

func truncateID(id string) string {
	return id[:5]
}

func getBridgeMTU(r *dknet.CreateNetworkRequest) (int, error) {
	bridgeMTU := defaultMTU
	if r.Options != nil {
		if mtu, ok := r.Options[mtuOption].(int); ok {
			bridgeMTU = mtu
		}
	}
	return bridgeMTU, nil
}

func getBridgeName(r *dknet.CreateNetworkRequest) (string, error) {
	bridgeName := bridgePrefix + truncateID(r.NetworkID)
	if r.Options != nil {
		if name, ok := r.Options[bridgeNameOption].(string); ok {
			bridgeName = name
		}
	}

	return bridgeName, nil
}

func getBridgeMode(r *dknet.CreateNetworkRequest) (string, error) {
	bridgeMode := defaultMode
	if r.Options != nil {
		if mode, ok := r.Options[modeOption].(string); ok {
			if _, isValid := validModes[mode]; !isValid {
				return "", fmt.Errorf("%s is not a valid mode", mode)
			}
			bridgeMode = mode
		}
	}
	return bridgeMode, nil
}

func getGatewayIP(r *dknet.CreateNetworkRequest) (string, string, error) {
	// FIXME: Dear future self, I'm sorry for leaving you with this mess, but I want to get this working ASAP
	// This should be an array
	// We need to handle case where we have
	// a. v6 and v4 - dual stack
	// auxilliary address
	// multiple subnets on one network
	// also in that case, we'll need a function to determine the correct default gateway based on it's IP/Mask
	var gatewayIP string

	if len(r.IPv6Data) > 0 {
		if r.IPv6Data[0] != nil {
			if r.IPv6Data[0].Gateway != "" {
				gatewayIP = r.IPv6Data[0].Gateway
			}
		}
	}
	// Assumption: IPAM will provide either IPv4 OR IPv6 but not both
	// We may want to modify this in future to support dual stack
	if len(r.IPv4Data) > 0 {
		if r.IPv4Data[0] != nil {
			if r.IPv4Data[0].Gateway != "" {
				gatewayIP = r.IPv4Data[0].Gateway
			}
		}
	}

	if gatewayIP == "" {
		return "", "", fmt.Errorf("No gateway IP found")
	}
	parts := strings.Split(gatewayIP, "/")
	if parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("Cannot split gateway IP address")
	}
	return parts[0], parts[1], nil
}

func getBindInterface(r *dknet.CreateNetworkRequest) (string, error) {
	if r.Options != nil {
		optionObj := r.Options[optionKey]
		if optionObj != nil {
			option := optionObj.(map[string]interface{})
			if interfacs, ok := option[bindInterfaceOption].(string); ok {
				return interfacs, nil
			}
		}
	}
	return "", nil
}

func getNetworkName(r *dknet.CreateNetworkRequest) (string, error) {
	if r.Options != nil {
		optionObj := r.Options[optionKey]
		if optionObj != nil {
			option := optionObj.(map[string]interface{})
			if networkName, ok := option[networkNameOption].(string); ok {
				return networkName, nil
			}
		}
	}

	return "", nil
}

func getNetworkType(r *dknet.CreateNetworkRequest) string {
	if r.Options != nil {
		optionObj := r.Options[optionKey]
		if optionObj != nil {
			option := optionObj.(map[string]interface{})
			if interfacs, ok := option[typeOption].(string); ok {
				return interfacs
			}
		}
	}

	return ""
}

