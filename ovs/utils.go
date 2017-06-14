package ovs

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const (
	serviceName = "/etc/systemd/system/linkerGateway.service"
)

var systemDConfig = `[Unit]
Description=Linker pgateway or sgateway process
[Service]
ExecStart={{.Command}}
[Install]
WantedBy=multi-user.target
`

// Generate a mac addr
func makeMac(ip net.IP) string {
	hw := make(net.HardwareAddr, 6)
	hw[0] = 0x7a
	hw[1] = 0x42
	copy(hw[2:], ip.To4())
	return hw.String()
}

// Return the IPv4 address of a network interface
func getIfaceAddr(name string) (*net.IPNet, error) {
	iface, err := netlink.LinkByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := netlink.AddrList(iface, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("Interface %s has no IP addresses", name)
	}
	if len(addrs) > 1 {
		log.Infof("Interface [ %v ] has more than 1 IPv4 address. Defaulting to using [ %v ]\n", name, addrs[0].IP)
	}
	return addrs[0].IPNet, nil
}

// Set the IP addr of a netlink interface
func setInterfaceIP(name string, rawIP string) error {
	retries := 2
	var iface netlink.Link
	var err error
	for i := 0; i < retries; i++ {
		iface, err = netlink.LinkByName(name)
		if err == nil {
			break
		}
		log.Debugf("error retrieving new OVS bridge netlink link [ %s ]... retrying", name)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("Abandoning retrieving the new OVS bridge link from netlink, Run [ ip link ] to troubleshoot the error: %s", err)
		return err
	}
	ipNet, err := netlink.ParseIPNet(rawIP)
	if err != nil {
		return err
	}
	addr := &netlink.Addr{ipNet, ""}
	return netlink.AddrAdd(iface, addr)
}

// Increment an IP in a subnet
func ipIncrement(networkAddr net.IP) net.IP {
	for i := 15; i >= 0; i-- {
		b := networkAddr[i]
		if b < 255 {
			networkAddr[i] = b + 1
			for xi := i + 1; xi <= 15; xi++ {
				networkAddr[xi] = 0
			}
			break
		}
	}
	return networkAddr
}

// Check if a netlink interface exists in the default namespace
func validateIface(ifaceStr string) bool {
	_, err := net.InterfaceByName(ifaceStr)
	if err != nil {
		log.Debugf("The requested interface [ %s ] was not found on the host: %s", ifaceStr, err)
		return false
	}
	return true
}

func ExecCommandWithComplete(input string) (output string, errput string, err error) {
	var retoutput string
	var reterrput string
	cmd := exec.Command("/bin/bash", "-c", input)
	log.Debugf("execute local command [%v]", cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Errorf("init stdout failed, error is %v", err)
		return "", "", err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Errorf("init stderr failed, error is %v", err)
		return "", "", err
	}

	if err := cmd.Start(); err != nil {
		log.Errorf("start command failed, error is %v", err)
		return "", "", err
	}

	bytesErr, err := ioutil.ReadAll(stderr)
	if err != nil {
		log.Errorf("read stderr failed, error is %v", err)
		return "", "", err
	}

	if len(bytesErr) != 0 {
		reterrput = strings.Trim(string(bytesErr), "\n")
	}

	bytes, err := ioutil.ReadAll(stdout)
	if err != nil {
		log.Errorf("read stdout failed, error is %v", err)
		return "", reterrput, err
	}

	if len(bytes) != 0 {
		retoutput = strings.Trim(string(bytes), "\n")
	}

	if err := cmd.Wait(); err != nil {
		log.Errorf("wait command failed, error is %v", err)
		log.Errorf("reterrput is %s", reterrput)
		return retoutput, reterrput, err
	}

	log.Debugf("retouput is %s", retoutput)
	log.Debugf("reterrput is %s", reterrput)
	return retoutput, reterrput, err
}

// func ExecCommandWithoutComplete(input string) (err error) {
// 	runlog, errl := os.Create("/tmp/nohup.out")
// 	if errl != nil {
// 		log.Debugf("create nohup log error %v", errl)
// 	}
// 	cmd := exec.Command("nohup", "bash", "-c", input, "&")
// 	log.Debugf("execute local command [%v]", cmd)
// 	if runlog != nil {
// 		cmd.Stdout = runlog
// 		cmd.Stderr = runlog
// 	}

// 	if err := cmd.Start(); err != nil {
// 		log.Errorf("start command failed, error is %v", err)
// 		return err
// 	}

// 	return err
// }

func StartOvsService(input string) (err error) {
	serviceFile, err := os.Create(serviceName)
	if err != nil {
		log.Warnf("failed to create sgw or pgw service file %v", err)
		return err
	}

	defer serviceFile.Close()

	templ, err := template.New("systemDConfig").Parse(systemDConfig)
	if err != nil {
		log.Warnf("create systemd service error %v", err)
		return err
	}

	if err := templ.Execute(
		serviceFile,
		&struct {
			Command string
		}{
			input,
		},
	); err != nil {
		log.Warnf("build systemd service error %v", err)
		return err
	}

	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		log.Warnf("systemctl daemon-reload error %v", err)
		return err
	}

	if err := exec.Command("systemctl", "start", "linkerGateway.service").Run(); err != nil {
		log.Warnf("systemctl start linkerGateway error %v", err)
		return err
	}
        if err := exec.Command("systemctl", "enable", "linkerGateway.service").Run(); err != nil {
		log.Warnf("systemctl enable linkerGateway error %v", err)
	}

	return nil
}

func stopOvsService() (err error) {
	log.Infof("stop and remove linkerGateway process")

	if err := exec.Command("systemctl", "stop", "linkerGateway.service").Run(); err != nil {
		log.Warnf("systemctl stop linkerGateway error %v", err)
		return err
	}
        if err := exec.Command("systemctl", "disable", "linkerGateway.service").Run(); err != nil {
		log.Warnf("systemctl disable linkerGateway error %v", err)
	}

	if err := os.Remove(serviceName); err != nil {
		log.Warnf("remove linkerGateway.service file error %v", err)
		return err
	}
	return nil
}

// func needStopGatewayProcess(docker dockerer, networkID string) bool {
// 	networks, err := docker.client.ListNetworks("")
// 	if err != nil {
// 		log.Warnf("get all networks on node error %v", err)
// 		return false
// 	}

// 	for _, network := range networks {
// 		if strings.EqualFold(networkID, network.ID) {
// 			log.Debugf("delete network is %v", network)
// 			option := (network.Options).(map[string]string)
// 			serviceType := option[typeOption]
// 			if strings.EqualFold(serviceType, type_sgw) ||
// 				strings.EqualFold(serviceType, type_pgw) {
// 				return true
// 			}
// 		}
// 	}

// 	return false
// }

