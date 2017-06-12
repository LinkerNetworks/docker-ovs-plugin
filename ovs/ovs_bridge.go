package ovs

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/libnetwork/iptables"
	"github.com/socketplane/libovsdb"
)

//  setupBridge If bridge does not exist create it.
func (d *Driver) initBridge(id string) error {
	bridgeName := d.networks[id].BridgeName
	bindInterface := d.networks[id].FlatBindInterface
	networktype := d.networks[id].NetworkType
	networkname := d.networks[id].NetworkName

	if err := d.ovsdber.addBridge(bridgeName, networktype); err != nil {
		log.Errorf("error creating ovs bridge [ %s ] : [ %s ]", bridgeName, err)
		return err
	}

	retries := 3
	found := false
	for i := 0; i < retries; i++ {
		if found = validateIface(bridgeName); found {
			break
		}
		log.Debugf("A link for the OVS bridge named [ %s ] not found, retrying in 2 seconds", bridgeName)
		time.Sleep(2 * time.Second)
	}
	if found == false {
		return fmt.Errorf("Could not find a link for the OVS bridge named %s", bridgeName)

	}

	bridgeMode := d.networks[id].Mode
	switch bridgeMode {
	case modeNAT:
		{
			gatewayIP := d.networks[id].Gateway + "/" + d.networks[id].GatewayMask
			if err := setInterfaceIP(bridgeName, gatewayIP); err != nil {
				log.Debugf("Error assigning address: %s on bridge: %s with an error of: %s", gatewayIP, bridgeName, err)
			}

			// Validate that the IPAddress is there!
			_, err := getIfaceAddr(bridgeName)
			if err != nil {
				log.Fatalf("No IP address found on bridge %s", bridgeName)
				return err
			}

			// Add NAT rules for iptables
			if err = natOut(gatewayIP); err != nil {
				log.Fatalf("Could not set NAT rules for bridge %s", bridgeName)
				return err
			}
		}

	case modeFlat:
		{
			//ToDo: Add NIC to the bridge
		}
	}

	// Bring the bridge up
	err := interfaceUp(bridgeName)
	if err != nil {
		log.Warnf("Error enabling bridge: [ %s ]", err)
		return err
	}

	runOvsScript(bridgeName, networkname, networktype, bindInterface)

	return nil
}

func runOvsScript(bridgeName, networkName, networkType, bindInterface string) {
	if !strings.EqualFold(networkType, type_sgw) && !strings.EqualFold(networkType, type_pgw) {
		log.Infof("network type is not sgw or pgw, no need to run ovs script, type is %s", networkType)
		return
	}

	var commandTextBuffer bytes.Buffer
	commandTextBuffer.WriteString("/usr/sbin/ovsopt.sh ")
	commandTextBuffer.WriteString(networkType + " ")
	commandTextBuffer.WriteString(networkName + " ")
	commandTextBuffer.WriteString(bridgeName + " ")
	commandTextBuffer.WriteString(bindInterface)

	err := StartOvsService(commandTextBuffer.String())
	if err != nil {
		log.Errorf("start ovsopt.sh error %v", err)
	}

}

func (ovsdber *ovsdber) createBridgeIface(name, servicetype string) error {
	err := ovsdber.createOvsdbBridge(name, servicetype)
	if err != nil {
		log.Errorf("Bridge creation failed for the bridge named [ %s ] with errors: %s", name, err)
	}
	return nil
}

// createOvsdbBridge creates the OVS bridge
func (ovsdber *ovsdber) createOvsdbBridge(bridgeName, servicetype string) error {
	namedBridgeUUID := "bridge"
	namedPortUUID := "port"
	namedIntfUUID := "intf"

	// intf row to insert
	intf := make(map[string]interface{})
	intf["name"] = bridgeName
	intf["type"] = `internal`

	insertIntfOp := libovsdb.Operation{
		Op:       "insert",
		Table:    "Interface",
		Row:      intf,
		UUIDName: namedIntfUUID,
	}

	// Port row to insert
	port := make(map[string]interface{})
	port["name"] = bridgeName
	port["interfaces"] = libovsdb.UUID{namedIntfUUID}

	insertPortOp := libovsdb.Operation{
		Op:       "insert",
		Table:    "Port",
		Row:      port,
		UUIDName: namedPortUUID,
	}

	// Bridge row to insert
	bridge := make(map[string]interface{})
	bridge["name"] = bridgeName
	bridge["stp_enable"] = false
	bridge["ports"] = libovsdb.UUID{namedPortUUID}

	//insert bridge opt info, such as servicetype
	insertBridgeOp := libovsdb.Operation{
		Op:       "insert",
		Table:    "Bridge",
		Row:      bridge,
		UUIDName: namedBridgeUUID,
	}

	bridgeOpt := make(map[string]interface{})
	bridgeOpt["name"] = bridgeName
	bridgeOpt["service_type"] = servicetype
	insertBridgeOptOp := libovsdb.Operation{
		Op:    "insert",
		Table: "BridgeOpt",
		Row:   bridgeOpt,
		// UUIDName: namedBridgeUUID,
	}

	// Inserting a Bridge row in Bridge table requires mutating the open_vswitch table.
	mutateUUID := []libovsdb.UUID{libovsdb.UUID{namedBridgeUUID}}
	mutateSet, _ := libovsdb.NewOvsSet(mutateUUID)
	mutation := libovsdb.NewMutation("bridges", "insert", mutateSet)
	condition := libovsdb.NewCondition("_uuid", "==", libovsdb.UUID{ovsdber.getRootUUID()})

	// Mutate operation
	mutateOp := libovsdb.Operation{
		Op:        "mutate",
		Table:     "Open_vSwitch",
		Mutations: []interface{}{mutation},
		Where:     []interface{}{condition},
	}

	operations := []libovsdb.Operation{insertIntfOp, insertPortOp, insertBridgeOp, insertBridgeOptOp, mutateOp}
	reply, _ := ovsdber.ovsdb.Transact("Open_vSwitch", operations...)

	if len(reply) < len(operations) {
		return errors.New("Number of Replies should be atleast equal to number of Operations")
	}
	for i, o := range reply {
		if o.Error != "" && i < len(operations) {
			return errors.New("Transaction Failed due to an error :" + o.Error + " details : " + o.Details)
		} else if o.Error != "" {
			return errors.New("Transaction Failed due to an error :" + o.Error + " details : " + o.Details)
		}
	}
	return nil
}

// Check if port exists prior to creating a bridge
func (ovsdber *ovsdber) addBridge(bridgeName, servicetype string) error {
	if ovsdber.ovsdb == nil {
		return errors.New("OVS not connected")
	}
	// If the bridge has been created, an internal port with the same name will exist
	exists, err := ovsdber.portExists(bridgeName)
	if err != nil {
		return err
	}
	if !exists {
		if err := ovsdber.createBridgeIface(bridgeName, servicetype); err != nil {
			return err
		}
		exists, err = ovsdber.portExists(bridgeName)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("Error creating Bridge")
		}
	}
	return nil
}

// deleteBridge deletes the OVS bridge
func (d *Driver) deleteBridge(bridgeName string) error {
	//get bridge's servicetype
	serviceType, err := d.ovsdber.getBridgeServiceType(bridgeName)
	if err != nil {
		log.Warnf("failed to get network service type,bridge name is %s", bridgeName)
	}

	// simple delete operation
	condition := libovsdb.NewCondition("name", "==", bridgeName)
	deleteOp := libovsdb.Operation{
		Op:    "delete",
		Table: "Bridge",
		Where: []interface{}{condition},
	}

	//delete bridge opt info
	deleteOptOp := libovsdb.Operation{
		Op:    "delete",
		Table: "BridgeOpt",
		Where: []interface{}{condition},
	}

	bridgeUUID := getBridgeUUIDForName(bridgeName)
	if bridgeUUID == "" {
		log.Error("Unable to find a bridge uuid by name : ", bridgeName)
		return fmt.Errorf("Unable to find a bridge uuid by name : [ %s ]", bridgeName)
	}

	// Deleting a Bridge row in Bridge table requires mutating the open_vswitch table.
	mutateUUID := []libovsdb.UUID{libovsdb.UUID{bridgeUUID}}
	mutateSet, _ := libovsdb.NewOvsSet(mutateUUID)
	mutation := libovsdb.NewMutation("bridges", "delete", mutateSet)
	conditionm := libovsdb.NewCondition("_uuid", "==", libovsdb.UUID{d.ovsdber.getRootUUID()})

	log.Debugf("mutation is %v", mutateSet)
	// simple mutate operation
	mutateOp := libovsdb.Operation{
		Op:        "mutate",
		Table:     "Open_vSwitch",
		Mutations: []interface{}{mutation},
		Where:     []interface{}{conditionm},
	}

	operations := []libovsdb.Operation{deleteOp, deleteOptOp, mutateOp}
	reply, _ := d.ovsdber.ovsdb.Transact("Open_vSwitch", operations...)

	if len(reply) < len(operations) {
		log.Error("Number of Replies should be atleast equal to number of Operations")
	}
	for i, o := range reply {
		if o.Error != "" && i < len(operations) {
			log.Error("Transaction Failed due to an error :", o.Error, " in ", operations[i])
			errMsg := fmt.Sprintf("Transaction Failed due to an error: %s in operation: %v", o.Error, operations[i])
			return errors.New(errMsg)
		} else if o.Error != "" {
			errMsg := fmt.Sprintf("Transaction Failed due to an error : %s", o.Error)
			return errors.New(errMsg)
		}
	}
	log.Debugf("OVSDB delete bridge transaction succesful")

	log.Debugf("check and stop linkerGateway process")
	if !strings.EqualFold(type_pgw, serviceType) && !strings.EqualFold(type_sgw, serviceType) {
		log.Infof("the deleted network service type is %s, no need to stop linkerGateway process", serviceType)
		return nil
	}

	errs := stopOvsService()
	if errs != nil {
		log.Warnf("stop ovs service error %v", errs)
	}

	return nil
}

func getBridgeUUIDForName(name string) string {
	bridgeCache := ovsdbCache["Bridge"]
	for key, val := range bridgeCache {
		if val.Fields["name"] == name {
			return key
		}
	}
	return ""

}

// todo: reconcile with what libnetwork does and port mappings
func natOut(cidr string) error {
	masquerade := []string{
		"POSTROUTING", "-t", "nat",
		"-s", cidr,
		"-j", "MASQUERADE",
	}
	if _, err := iptables.Raw(
		append([]string{"-C"}, masquerade...)...,
	); err != nil {
		incl := append([]string{"-I"}, masquerade...)
		if output, err := iptables.Raw(incl...); err != nil {
			return err
		} else if len(output) > 0 {
			return &iptables.ChainError{
				Chain:  "POSTROUTING",
				Output: output,
			}
		}
	}
	return nil
}

