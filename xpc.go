package goble

/*
#include "xpc_wrapper.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	r "reflect"
	"unsafe"
)

//
// minimal XPC support required for BLE
//

// a dictionary of things
type dict map[string]interface{}

func (d dict) Contains(k string) bool {
	_, ok := d[k]
	return ok
}

func (d dict) MustGetDict(k string) dict {
	return d[k].(dict)
}

func (d dict) MustGetArray(k string) array {
	return d[k].(array)
}

func (d dict) MustGetBytes(k string) []byte {
	return d[k].([]byte)
}

func (d dict) MustGetHexBytes(k string) string {
	return fmt.Sprintf("%x", d[k].([]byte))
}

func (d dict) MustGetInt(k string) int {
	return int(d[k].(int64))
}

func (d dict) MustGetUUID(k string) UUID {
	return d[k].(UUID)
}

func (d dict) GetString(k, defv string) string {
	if v := d[k]; v != nil {
		//log.Printf("GetString %s %#v\n", k, v)
		return v.(string)
	} else {
		//log.Printf("GetString %s default %#v\n", k, defv)
		return defv
	}
}

func (d dict) GetBytes(k string, defv []byte) []byte {
	if v := d[k]; v != nil {
		//log.Printf("GetBytes %s %#v\n", k, v)
		return v.([]byte)
	} else {
		//log.Printf("GetBytes %s default %#v\n", k, defv)
		return defv
	}
}

func (d dict) GetInt(k string, defv int) int {
	if v := d[k]; v != nil {
		//log.Printf("GetString %s %#v\n", k, v)
		return int(v.(int64))
	} else {
		//log.Printf("GetString %s default %#v\n", k, defv)
		return defv
	}
}

func (d dict) GetUUID(k string) UUID {
	return GetUUID(d[k])
}

// an array of things
type array []interface{}

func (a array) GetUUID(k int) UUID {
	return GetUUID(a[k])
}

// a UUID
type UUID [16]byte

func MakeUUID(s string) UUID {
	var sl []byte
	fmt.Sscanf(s, "%32x", &sl)

	var uuid [16]byte
	copy(uuid[:], sl)
	return UUID(uuid)
}

func (uuid UUID) String() string {
	return fmt.Sprintf("%x", [16]byte(uuid))
}

func GetUUID(v interface{}) UUID {
	if v == nil {
		return UUID{}
	}

	if uuid, ok := v.(UUID); ok {
		return uuid
	}

	if bytes, ok := v.([]byte); ok {
		uuid := UUID{}

		for i, b := range bytes {
			uuid[i] = b
		}

		return uuid
	}

	if bytes, ok := v.([]uint8); ok {
		uuid := UUID{}

		for i, b := range bytes {
			uuid[i] = b
		}

		return uuid
	}

	log.Fatalf("invalid type for UUID: %#v", v)
	return UUID{}
}

var (
	CONNECTION_INVALID     = errors.New("connection invalid")
	CONNECTION_INTERRUPTED = errors.New("connection interrupted")
	CONNECTION_TERMINATED  = errors.New("connection terminated")

	TYPE_OF_UUID  = r.TypeOf(UUID{})
	TYPE_OF_BYTES = r.TypeOf([]byte{})
)

type XpcEventHandler interface {
	HandleXpcEvent(event dict, err error)
}

func XpcConnect(service string, eh XpcEventHandler) C.xpc_connection_t {
	cservice := C.CString(service)
	defer C.free(unsafe.Pointer(cservice))
	return C.XpcConnect(cservice, unsafe.Pointer(&eh))
}

//export handleXpcEvent
func handleXpcEvent(event C.xpc_object_t, p unsafe.Pointer) {
	//log.Printf("handleXpcEvent %#v %#v\n", event, p)

	t := C.xpc_get_type(event)
	eh := *((*XpcEventHandler)(p))

	if t == C.TYPE_ERROR {
		if event == C.ERROR_CONNECTION_INVALID {
			// The client process on the other end of the connection has either
			// crashed or cancelled the connection. After receiving this error,
			// the connection is in an invalid state, and you do not need to
			// call xpc_connection_cancel(). Just tear down any associated state
			// here.
			//log.Println("connection invalid")
			eh.HandleXpcEvent(nil, CONNECTION_INVALID)
		} else if event == C.ERROR_CONNECTION_INTERRUPTED {
			//log.Println("connection interrupted")
			eh.HandleXpcEvent(nil, CONNECTION_INTERRUPTED)
		} else if event == C.ERROR_CONNECTION_TERMINATED {
			// Handle per-connection termination cleanup.
			//log.Println("connection terminated")
			eh.HandleXpcEvent(nil, CONNECTION_TERMINATED)
		} else {
			//log.Println("got some error", event)
			eh.HandleXpcEvent(nil, fmt.Errorf("%v", event))
		}
	} else {
		eh.HandleXpcEvent(xpcToGo(event).(dict), nil)
	}
}

// goToXpc converts a go object to an xpc object
func goToXpc(o interface{}) C.xpc_object_t {
	return valueToXpc(r.ValueOf(o))
}

// valueToXpc converts a go Value to an xpc object
//
// note that not all the types are supported, but only the subset required for Blued
func valueToXpc(val r.Value) C.xpc_object_t {
	if !val.IsValid() {
		return nil
	}

	var xv C.xpc_object_t

	switch val.Kind() {
	case r.Int, r.Int8, r.Int16, r.Int32, r.Int64:
		xv = C.xpc_int64_create(C.int64_t(val.Int()))

	case r.Uint, r.Uint8, r.Uint16, r.Uint32:
		xv = C.xpc_int64_create(C.int64_t(val.Uint()))

	case r.String:
		xv = C.xpc_string_create(C.CString(val.String()))

	case r.Map:
		xv = C.xpc_dictionary_create(nil, nil, 0)
		for _, k := range val.MapKeys() {
			v := valueToXpc(val.MapIndex(k))
			C.xpc_dictionary_set_value(xv, C.CString(k.String()), v)
			if v != nil {
				C.xpc_release(v)
			}
		}

	case r.Array, r.Slice:
		if val.Type() == TYPE_OF_UUID {
			// array of bytes
			var uuid [16]byte
			r.Copy(r.ValueOf(uuid[:]), val)
			xv = C.xpc_uuid_create(C.ptr_to_uuid(unsafe.Pointer(&uuid[0])))
		} else if val.Type() == TYPE_OF_BYTES {
			// slice of bytes
			xv = C.xpc_data_create(unsafe.Pointer(val.Pointer()), C.size_t(val.Len()))
		} else {
			xv = C.xpc_array_create(nil, 0)
			l := val.Len()

			for i := 0; i < l; i++ {
				v := valueToXpc(val.Index(i))
				C.xpc_array_append_value(xv, v)
				if v != nil {
					C.xpc_release(v)
				}
			}
		}

	case r.Interface, r.Ptr:
		xv = valueToXpc(val.Elem())

	default:
		log.Fatalf("unsupported %#v", val.String())
	}

	return xv
}

//export arraySet
func arraySet(u unsafe.Pointer, i C.int, v C.xpc_object_t) {
	a := *(*array)(u)
	a[i] = xpcToGo(v)
}

//export dictSet
func dictSet(u unsafe.Pointer, k *C.char, v C.xpc_object_t) {
	d := *(*dict)(u)
	d[C.GoString(k)] = xpcToGo(v)
}

// xpcToGo converts an xpc object to a go object
//
// note that not all the types are supported, but only the subset required for Blued
func xpcToGo(v C.xpc_object_t) interface{} {
	t := C.xpc_get_type(v)

	switch t {
	case C.TYPE_ARRAY:
		a := make(array, C.int(C.xpc_array_get_count(v)))
		C.XpcArrayApply(unsafe.Pointer(&a), v)
		return a

	case C.TYPE_DATA:
		return C.GoBytes(C.xpc_data_get_bytes_ptr(v), C.int(C.xpc_data_get_length(v)))

	case C.TYPE_DICT:
		d := make(dict)
		C.XpcDictApply(unsafe.Pointer(&d), v)
		return d

	case C.TYPE_INT64:
		return int64(C.xpc_int64_get_value(v))

	case C.TYPE_STRING:
		return C.GoString(C.xpc_string_get_string_ptr(v))

	case C.TYPE_UUID:
		a := [16]byte{}
		C.XpcUUIDGetBytes(unsafe.Pointer(&a), v)
		return UUID(a)

	default:
		log.Fatalf("unexpected type %#v, value %#v", t, v)
	}

	return nil
}

// xpc_release is needed by tests, since they can't use CGO
func xpc_release(xv C.xpc_object_t) {
	C.xpc_release(xv)
}
