package main

/*
#cgo CFLAGS: -I/usr/include
#cgo LDFLAGS: -lcwiid -Lcwiid/libcwiid/libcwiid.a
#include "racerwiigo.h"
#include <stdlib.h>
#include <cwiid.h>
#include <time.h>
#include <bluetooth/bluetooth.h>
*/
import "C"

import (
	"fmt"
	"os"
	"reflect"
	"time"
	"unsafe"
)

var buttons = []_Ctype_uint16_t{ // only HOME and A buttons are used for this program
	C.CWIID_BTN_A,
	//C.CWIID_BTN_B,
	//C.CWIID_BTN_1,
	//C.CWIID_BTN_2,
	//C.CWIID_BTN_MINUS,
	C.CWIID_BTN_HOME,
	//C.CWIID_BTN_LEFT,
	//C.CWIID_BTN_RIGHT,
	//C.CWIID_BTN_DOWN,
	//C.CWIID_BTN_UP,
	//C.CWIID_BTN_PLUS,
}

var buttonStatus []bool
var errorChan chan bool

//export goCwiidCallback
func goCwiidCallback(wm unsafe.Pointer, a int, mesg *C.struct_cwiid_btn_mesg, tp unsafe.Pointer) {
	//defer C.free(unsafe.Pointer(mesg))
	var messages []C.struct_cwiid_btn_mesg
	sliceHeader := (*reflect.SliceHeader)((unsafe.Pointer(&messages)))
	sliceHeader.Cap = a
	sliceHeader.Len = a
	sliceHeader.Data = uintptr(unsafe.Pointer(mesg))
	for _, m := range messages {
		if m._type != C.CWIID_MESG_BTN {
			fmt.Println("Got message from WiiMote that wasn't a button")
			errorChan <- true
			continue
		}
		for x, button := range buttons {
			if m.buttons&button == button {
				if !buttonStatus[x] {
					if button == C.CWIID_BTN_A {
						fmt.Println("Got A button")
					} else if button == C.CWIID_BTN_HOME {
						fmt.Println("Got Home buton")
					}
					buttonStatus[x] = true
				}
			} else {
				buttonStatus[x] = false
			}
		}
	}
}

//export goErrCallback
func goErrCallback(wm unsafe.Pointer, char *C.char, ap unsafe.Pointer) {
	//func goErrCallback(wm *C.cwiid_wiimote_t, char *C.char, ap C.va_list) {
	str := C.GoString(char)
	switch str {
	case "No Bluetooth interface found":
		fallthrough
	case "no such device":
		fmt.Printf("No Bluetooth device found\n")
		os.Exit(1)
	case "Socket connect error (control channel)":
		fallthrough
	case "No wiimotes found":
		fmt.Println("Wiimote disconnected")
		errorChan <- true
	default:
		fmt.Printf("Inside error calback - %s\n", str)
		fmt.Println("Wiimote disconnected")
		errorChan <- true
	}
}

func main() {
	errorChan = make(chan bool, 10) // anything put on the channel is an error and the wiimote disconnects
	buttonStatus = make([]bool, len(buttons))
	var bdaddr C.bdaddr_t
	var wm *C.struct_cwiid_wiimote_t
	ticker := time.NewTicker(time.Second * 10)
	val, err := C.cwiid_set_err(C.getErrCallback())
	if val != 0 || err != nil {
		fmt.Printf("Error setting the callback to catch errors - %d - %v", val, err)
		os.Exit(1)
	}
	for {
	emptyChannel:
		for {
			select {
			case <-errorChan: // empty the chan
			default:
				break emptyChannel
			}
		}
		fmt.Println("Press 1&2 on the Wiimote now")
		wm, err = C.cwiid_open(&bdaddr, 0)
		if err != nil {
			fmt.Errorf("cwiid_open: %v\n", err)
			continue
		}
		res, err := C.cwiid_command(wm, C.CWIID_CMD_RPT_MODE, C.CWIID_RPT_BTN)
		if res != 0 || err != nil {
			fmt.Printf("Result of command = %d - %v\n", res, err)
		}

		res, err = C.cwiid_set_mesg_callback(wm, C.getCwiidCallback())
		if res != 0 || err != nil {
			fmt.Printf("Result of callback = %d - %v\n", res, err)
		}
		res, err = C.cwiid_enable(wm, C.CWIID_FLAG_MESG_IFC)
		if res != 0 || err != nil {
			fmt.Printf("Result of enable = %d - %v\n", res, err)
		}

		res, err = C.cwiid_set_led(wm, C.CWIID_LED4_ON)
		if res != 0 || err != nil {
			fmt.Printf("Set led result = %d\n", res)
			fmt.Errorf("Err = %v", err)
		}
	wiimoteConnected:
		for {
			select {
			case <-errorChan:
				break wiimoteConnected
			case <-ticker.C:
				fmt.Println("time passed")
			}
		}
	}
}
