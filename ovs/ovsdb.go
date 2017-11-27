package ovs

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/socketplane/libovsdb"
)

const (
	localhost    = "127.0.0.1"
	ovsdbPort    = 6640
	contextKey   = "container_id"
	contextValue = "container_data"
	minMTU       = 68
)

var (
	quit         chan bool
	update       chan *libovsdb.TableUpdates
	ovsdbCache   map[string]map[string]libovsdb.Row
	contextCache map[string]string
)

type ovsdber struct {
	ovsdb *libovsdb.OvsdbClient
}

type OvsdbNotifier struct {
}

func (o OvsdbNotifier) Update(context interface{}, tableUpdates libovsdb.TableUpdates) {
	populateCache(tableUpdates)
	update <- &tableUpdates
}
func (o OvsdbNotifier) Disconnected(ovsClient *libovsdb.OvsdbClient) {
}
func (o OvsdbNotifier) Locked([]interface{}) {
}
func (o OvsdbNotifier) Stolen([]interface{}) {
}
func (o OvsdbNotifier) Echo([]interface{}) {
}

func (ovsdber *ovsdber) initDBCache() {
	quit = make(chan bool)
	update = make(chan *libovsdb.TableUpdates)
	ovsdbCache = make(map[string]map[string]libovsdb.Row)

	// Register for ovsdb table notifications
	var notifier OvsdbNotifier
	ovsdber.ovsdb.Register(notifier)
	// Populate ovsdb cache for the default Open_vSwitch db
	initCache, err := ovsdber.ovsdb.MonitorAll("Open_vSwitch", "")
	if err != nil {
		log.Errorf("Error populating initial OVSDB cache: %s", err)
	}
	log.Debugf("MonitorAll is %v", *initCache)
	populateCache(*initCache)
	contextCache = make(map[string]string)
	populateContextCache(ovsdber.ovsdb)

	// async monitoring of the ovs bridge(s) for table updates
	go ovsdber.monitorBridges()
	for ovsdber.getRootUUID() == "" {
		time.Sleep(time.Second * 1)
	}
}

func populateContextCache(ovs *libovsdb.OvsdbClient) {
	if ovs == nil {
		return

	}
	tableCache := getTableCache("Interface")
	for _, row := range tableCache {
		config, ok := row.Fields["other_config"]
		ovsMap := config.(libovsdb.OvsMap)
		otherConfig := map[interface{}]interface{}(ovsMap.GoMap)
		if ok {
			containerID, ok := otherConfig[contextKey]
			if ok {
				contextCache[containerID.(string)] = otherConfig[contextValue].(string)
			}
		}
	}
}

func getTableCache(tableName string) map[string]libovsdb.Row {
	return ovsdbCache[tableName]
}

func (ovsdber *ovsdber) portExists(portName string) (bool, error) {
	condition := libovsdb.NewCondition("name", "==", portName)
	selectOp := libovsdb.Operation{
		Op:    "select",
		Table: "Port",
		Where: []interface{}{condition},
	}
	operations := []libovsdb.Operation{selectOp}
	reply, _ := ovsdber.ovsdb.Transact("Open_vSwitch", operations...)

	if len(reply) < len(operations) {
		return false, errors.New("Number of Replies should be at least equal to number of Operations")
	}

	if reply[0].Error != "" {
		errMsg := fmt.Sprintf("Transaction Failed due to an error: %v", reply[0].Error)
		return false, errors.New(errMsg)
	}

	if len(reply[0].Rows) == 0 {
		return false, nil
	}
	return true, nil
}

func (ovsdber *ovsdber) getBridgeServiceType(bridgenName string) (string, error) {
	condition := libovsdb.NewCondition("name", "==", bridgenName)
	selectOp := libovsdb.Operation{
		Op:    "select",
		Table: "BridgeOpt",
		Where: []interface{}{condition},
	}
	operations := []libovsdb.Operation{selectOp}
	reply, _ := ovsdber.ovsdb.Transact("Open_vSwitch", operations...)

	if len(reply) < len(operations) {
		return "", errors.New("Number of Replies should be at least equal to number of Operations")
	}

	if reply[0].Error != "" {
		errMsg := fmt.Sprintf("Transaction Failed due to an error: %v", reply[0].Error)
		return "", errors.New(errMsg)
	}

	rets := reply[0].Rows
	if len(rets) <= 0 {
		log.Warnf("no bridge with name %s", bridgenName)
		return "", errors.New("no record with bridge name")
	}
	log.Debugf("the record with bridgeName %s is %v", bridgenName, rets)

	serviceType := rets[0]["service_type"].(string)
	return serviceType, nil

}

func (ovsdber *ovsdber) getNetworkidByBridgeName(bridgenName string) (string, error) {
	log.Debugf("get networid by bridgeName %s", bridgenName)
	condition := libovsdb.NewCondition("name", "==", bridgenName)
	selectOp := libovsdb.Operation{
		Op:    "select",
		Table: "BridgeOpt",
		Where: []interface{}{condition},
	}
	operations := []libovsdb.Operation{selectOp}
	reply, _ := ovsdber.ovsdb.Transact("Open_vSwitch", operations...)

	if len(reply) < len(operations) {
		return "", errors.New("Number of Replies should be at least equal to number of Operations")
	}

	if reply[0].Error != "" {
		errMsg := fmt.Sprintf("Transaction Failed due to an error: %v", reply[0].Error)
		return "", errors.New(errMsg)
	}

	rets := reply[0].Rows
	if len(rets) <= 0 {
		log.Warnf("no bridge with name %s", bridgenName)
		return "", errors.New("no record with bridge name")
	}
	log.Debugf("the record with bridgeName %s is %v", bridgenName, rets)

	networkid := rets[0]["network_id"].(string)
	return networkid, nil

}

func (ovsdber *ovsdber) getBridgeNameByNetworkId(networkid string) (string, error) {
	log.Debugf("get bridgeName by networkid %s", networkid)
	condition := libovsdb.NewCondition("network_id", "==", networkid)
	selectOp := libovsdb.Operation{
		Op:    "select",
		Table: "BridgeOpt",
		Where: []interface{}{condition},
	}
	operations := []libovsdb.Operation{selectOp}
	reply, _ := ovsdber.ovsdb.Transact("Open_vSwitch", operations...)

	if len(reply) < len(operations) {
		return "", errors.New("Number of Replies should be at least equal to number of Operations")
	}

	if reply[0].Error != "" {
		errMsg := fmt.Sprintf("Transaction Failed due to an error: %v", reply[0].Error)
		return "", errors.New(errMsg)
	}

	rets := reply[0].Rows
	if len(rets) <= 0 {
		log.Warnf("no bridge with networkid %s", networkid)
		return "", errors.New("no record with networkid")
	}
	log.Debugf("the record with networkid %s is %v", networkid, rets)

	bridgeName := rets[0]["name"].(string)
	return bridgeName, nil
}

func (ovsdber *ovsdber) monitorBridges() {
	for {
		select {
		case currUpdate := <-update:
			for table, tableUpdate := range currUpdate.Updates {
				if table == "Bridge" {
					for _, row := range tableUpdate.Rows {
						empty := libovsdb.Row{}
						if !reflect.DeepEqual(row.New, empty) {
							oldRow := row.Old
							if _, ok := oldRow.Fields["name"]; ok {
								name := oldRow.Fields["name"].(string)
								servicetype, err := ovsdber.getBridgeServiceType(name)
								if err != nil {
									log.Warnf("get servicetpye for bridgeName %s, error %v", name, err)
									servicetype = "none"
								}
								networkid, err := ovsdber.getNetworkidByBridgeName(name)
								if err != nil {
									log.Warnf("get networkid for bridgeName %s, error %v", name, err)
									networkid = "none"
								}
								ovsdber.createOvsdbBridge(name, servicetype, networkid)
							}
						}
					}
				}
			}
		}
	}
}

func (ovsdber *ovsdber) getRootUUID() string {
	for uuid := range ovsdbCache["Open_vSwitch"] {
		return uuid
	}
	return ""
}

func populateCache(updates libovsdb.TableUpdates) {
	log.Debugf("udpates is %v", updates)
	for table, tableUpdate := range updates.Updates {
		if _, ok := ovsdbCache[table]; !ok {
			ovsdbCache[table] = make(map[string]libovsdb.Row)
		}
		for uuid, row := range tableUpdate.Rows {
			empty := libovsdb.Row{}
			if !reflect.DeepEqual(row.New, empty) {
				ovsdbCache[table][uuid] = row.New
			} else {
				delete(ovsdbCache[table], uuid)
			}
		}
	}
}

