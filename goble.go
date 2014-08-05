package goble

/*
#include <xpc/xpc.h>
#include "xpc_wrapper.h"
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"time"
)

//
// BLE support
//

var STATES = []string{"unknown", "resetting", "unsupported", "unauthorized", "poweredOff", "poweredOn"}

type Property int

const (
	Broadcast                 Property = 1 << iota
	Read                               = 1 << iota
	WriteWithoutResponse               = 1 << iota
	Write                              = 1 << iota
	Notify                             = 1 << iota
	Indicate                           = 1 << iota
	AuthenticatedSignedWrites          = 1 << iota
	ExtendedProperties                 = 1 << iota
)

type ServiceData struct {
	uuid string
	data []byte
}

type ServiceHandle struct {
	uuid        string
	startHandle int
	endHandle   int
}

type Advertisement struct {
	LocalName        string
	TxPowerLevel     int
	ManufacturerData []byte
	ServiceData      []ServiceData
	ServiceUuids     []string
}

type Peripheral struct {
	uuid          UUID
	advertisement Advertisement
	rssi          int
	services      map[interface{}]ServiceHandle
}

// GATT Descriptor
type Descriptor struct {
	uuid  UUID
	value []byte
}

// GATT Characteristic
type Characteristic struct {
	uuid        UUID
	properties  Property
	secure      Property
	descriptors []Descriptor
	value       []byte
}

// GATT Service
type Service struct {
	uuid            UUID
	characteristics []Characteristic
}

type BLE struct {
	Emitter
	conn    C.xpc_connection_t
	verbose bool

	peripherals            map[string]*Peripheral
	attributes             array
	lastServiceAttributeId int
}

func New() *BLE {
	ble := &BLE{peripherals: map[string]*Peripheral{}}
	ble.conn = XpcConnect("com.apple.blued", ble)
	return ble
}

func (ble *BLE) SetVerbose(v bool) {
	ble.verbose = v
}

// process BLE events and asynchronous errors
// (implements XpcEventHandler)
func (ble *BLE) HandleXpcEvent(event dict, err error) {
	id := event.MustGetInt("kCBMsgId")
	args := event.MustGetDict("kCBMsgArgs")

	if ble.verbose {
		log.Printf("event: %v %#v\n", id, args)
	}

	switch id {
	case 6: // state change
		state := args.MustGetInt("kCBMsgArgState")
		ble.Emit(Event{Name: "stateChange", State: STATES[state]})

	case 16: // advertising start
		result := args.MustGetInt("kCBMsgArgResult")
		if result != 0 {
			log.Printf("event: error in advertisingStart %v\n", result)
		} else {
			ble.Emit(Event{Name: "advertisingStart"})
		}

	case 17: // advertising stop
		result := args.MustGetInt("kCBMsgArgResult")
		if result != 0 {
			log.Printf("event: error in advertisingStop %v\n", result)
		} else {
			ble.Emit(Event{Name: "advertisingStop"})
		}

	case 37: // discover
		advdata := args.MustGetDict("kCBMsgArgAdvertisementData")
		if len(advdata) == 0 {
			//log.Println("event: discover with no advertisment data")
			break
		}

		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")

		advertisement := Advertisement{
			LocalName:        advdata.GetString("kCBAdvDataLocalName", args.GetString("kCBMsgArgName", "")),
			TxPowerLevel:     advdata.GetInt("kCBAdvDataTxPowerLevel", 0),
			ManufacturerData: advdata.GetBytes("kCBAdvDataManufacturerData", nil),
			ServiceData:      []ServiceData{},
			ServiceUuids:     []string{},
		}

		rssi := args.GetInt("kCBMsgArgRssi", 0)

		if uuids, ok := advdata["kCBAdvDataServiceUUIDs"]; ok {
			for _, uuid := range uuids.(array) {
				advertisement.ServiceUuids = append(advertisement.ServiceUuids, GetUUID(uuid).String())
			}
		}

		if sdata, ok := advdata["kCBAdvDataServiceData"]; ok {
			for _, data := range sdata.(array) {
				bytes := data.([]byte)
				sd := ServiceData{
					uuid: fmt.Sprintf("%x", bytes[0]),
					data: bytes[1:],
				}

				advertisement.ServiceData = append(advertisement.ServiceData, sd)
			}
		}

		ble.peripherals[deviceUuid.String()] = &Peripheral{
			uuid:          deviceUuid,
			advertisement: advertisement,
			rssi:          rssi,
			services:      map[interface{}]ServiceHandle{},
		}

		ble.Emit(Event{Name: "discover", DeviceUUID: deviceUuid, Advertisement: advertisement, Rssi: rssi})

	case 38: // connect
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		ble.Emit(Event{Name: "connect", DeviceUUID: deviceUuid})

	case 40: // disconnect
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		ble.Emit(Event{Name: "disconnect", DeviceUUID: deviceUuid})

	case 54: // rssiUpdate
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		rssi := args.MustGetInt("kCBMsgArgData")

		if p, ok := ble.peripherals[deviceUuid.String()]; ok {
			p.rssi = rssi
		}

		ble.Emit(Event{Name: "rssiUpdate", DeviceUUID: deviceUuid, Rssi: rssi})

	case 55: // serviceDiscover
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		servicesUuids := []string{}
		services := map[interface{}]ServiceHandle{}

		if dservices, ok := args["kCBMsgArgServices"]; ok {
			for _, s := range dservices.(array) {
				service := s.(dict)
				serviceHandle := ServiceHandle{
					uuid:        fmt.Sprintf("%x", service.MustGetBytes("kCBMsgArgUUID")),
					startHandle: service.MustGetInt("kCBMsgArgServiceStartHandle"),
					endHandle:   service.MustGetInt("kCBMsgArgServiceEndHandle")}

				services[serviceHandle.uuid] = serviceHandle
				services[serviceHandle.startHandle] = serviceHandle

				servicesUuids = append(servicesUuids, serviceHandle.uuid)
			}
		}

		ble.peripherals[deviceUuid.String()].services = services
		ble.Emit(Event{Name: "servicesDiscover", DeviceUUID: deviceUuid, Services: servicesUuids})
	}
}

// send a message to Blued
func (ble *BLE) sendCBMsg(id int, args dict) {
	message := dict{"kCBMsgId": id, "kCBMsgArgs": args}
	if ble.verbose {
		log.Printf("sendCBMsg %#v\n", message)
	}

	C.XpcSendMessage(ble.conn, goToXpc(message), true)
}

// initialize BLE
func (ble *BLE) Init() {
	ble.sendCBMsg(1, dict{"kCBMsgArgName": fmt.Sprintf("node-%v", time.Now().Unix()),
		"kCBMsgArgOptions": dict{"kCBInitOptionShowPowerAlert": 0}, "kCBMsgArgType": 0})
}

// start advertising
func (ble *BLE) StartAdvertising(name string, serviceUuids []UUID) {
	uuids := make([]string, len(serviceUuids))
	for i, uuid := range serviceUuids {
		uuids[i] = uuid.String()
	}
	ble.sendCBMsg(8, dict{"kCBAdvDataLocalName": name, "kCBAdvDataServiceUUIDs": uuids})
}

// start advertising as IBeacon (raw data)
func (ble *BLE) StartAdvertisingIBeaconData(data []byte) {
	ble.sendCBMsg(8, dict{"kCBAdvDataAppleBeaconKey": data})
}

// start advertising as IBeacon
func (ble *BLE) StartAdvertisingIBeacon(uuid UUID, major, minor uint16, measuredPower int8) {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uuid[:])
	binary.Write(buf, binary.BigEndian, major)
	binary.Write(buf, binary.BigEndian, minor)
	binary.Write(buf, binary.BigEndian, measuredPower)

	ble.sendCBMsg(8, dict{"kCBAdvDataAppleBeaconKey": buf.Bytes()})
}

// stop advertising
func (ble *BLE) StopAdvertising() {
	ble.sendCBMsg(9, nil)
}

// start scanning
func (ble *BLE) StartScanning(serviceUuids []UUID, allowDuplicates bool) {
	uuids := []string{}

	for _, uuid := range serviceUuids {
		uuids = append(uuids, uuid.String())
	}

	args := dict{"kCBMsgArgUUIDs": uuids}
	if allowDuplicates {
		args["kCBMsgArgOptions"] = dict{"kCBScanOptionAllowDuplicates": 1}
	} else {
		args["kCBMsgArgOptions"] = dict{}
	}

	ble.sendCBMsg(29, args)
}

// stop scanning
func (ble *BLE) StopScanning() {
	ble.sendCBMsg(30, nil)
}

// connect
func (ble *BLE) Connect(deviceUuid UUID) {
	uuid := deviceUuid.String()
	if p, ok := ble.peripherals[uuid]; ok {
		ble.sendCBMsg(31, dict{"kCBMsgArgOptions": dict{"kCBConnectOptionNotifyOnDisconnection": 1},
			"kCBMsgArgDeviceUUID": p.uuid})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// disconnect
func (ble *BLE) Disconnect(deviceUuid UUID) {
	uuid := deviceUuid.String()
	if p, ok := ble.peripherals[uuid]; ok {
		ble.sendCBMsg(32, dict{"kCBMsgArgDeviceUUID": p.uuid})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// update rssi
func (ble *BLE) UpdateRssi(deviceUuid UUID) {
	uuid := deviceUuid.String()
	if p, ok := ble.peripherals[uuid]; ok {
		ble.sendCBMsg(43, dict{"kCBMsgArgDeviceUUID": p.uuid})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// discover services
func (ble *BLE) DiscoverServices(deviceUuid UUID, uuids []UUID) {
	sUuid := deviceUuid.String()
	if p, ok := ble.peripherals[sUuid]; ok {
		sUuids := make([]string, len(uuids))
		for i, uuid := range uuids {
			sUuids[i] = uuid.String()
		}
		ble.sendCBMsg(44, dict{"kCBMsgArgDeviceUUID": p.uuid, "kCBMsgArgUUIDs": sUuids})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// remove all services
func (ble *BLE) RemoveServices() {
	ble.sendCBMsg(12, nil)
}

// set services
func (ble *BLE) SetServices(services []Service) {
	ble.sendCBMsg(12, nil) // remove all services
	ble.attributes = array{nil}

	attributeId := 1

	for _, service := range services {
		arg := dict{
			"kCBMsgArgAttributeID":     attributeId,
			"kCBMsgArgAttributeIDs":    []int{},
			"kCBMsgArgCharacteristics": nil,
			"kCBMsgArgType":            1, // 1 => primary, 0 => excluded
			"kCBMsgArgUUID":            service.uuid.String(),
		}

		ble.attributes = append(ble.attributes, service)
		ble.lastServiceAttributeId = attributeId
		attributeId += 1

		characteristics := array{}

		for _, characteristic := range service.characteristics {
			properties := 0
			permissions := 0

			if Read&characteristic.properties != 0 {
				properties |= 0x02

				if Read&characteristic.secure != 0 {
					permissions |= 0x04
				} else {
					permissions |= 0x01
				}
			}

			if WriteWithoutResponse&characteristic.properties != 0 {
				properties |= 0x04

				if WriteWithoutResponse&characteristic.secure != 0 {
					permissions |= 0x08
				} else {
					permissions |= 0x02
				}
			}

			if Write&characteristic.properties != 0 {
				properties |= 0x08

				if WriteWithoutResponse&characteristic.secure != 0 {
					permissions |= 0x08
				} else {
					permissions |= 0x02
				}
			}

			if Notify&characteristic.properties != 0 {
				if Notify&characteristic.secure != 0 {
					properties |= 0x100
				} else {
					properties |= 0x10
				}
			}

			if Indicate&characteristic.properties != 0 {
				if Indicate&characteristic.secure != 0 {
					properties |= 0x200
				} else {
					properties |= 0x20
				}
			}

			descriptors := array{}
			for _, descriptor := range characteristic.descriptors {
				descriptors = append(descriptors, dict{"kCBMsgArgData": descriptor.value, "kCBMsgArgUUID": descriptor.uuid.String()})
			}

			characteristicArg := dict{
				"kCBMsgArgAttributeID":              attributeId,
				"kCBMsgArgAttributePermissions":     permissions,
				"kCBMsgArgCharacteristicProperties": properties,
				"kCBMsgArgData":                     characteristic.value,
				"kCBMsgArgDescriptors":              descriptors,
				"kCBMsgArgUUID":                     characteristic.uuid.String(),
			}

			ble.attributes = append(ble.attributes, characteristic)
			characteristics = append(characteristics, characteristicArg)

			attributeId += 1
		}

		arg["kCBMsgArgCharacteristics"] = characteristics
		ble.sendCBMsg(10, arg) // remove all services
	}
}
