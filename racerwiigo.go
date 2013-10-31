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

var buttonChan chan _Ctype_uint16_t
var exit chan bool
var callback = goCwiidCallback // so it's not garbage collected
var errCallback = goErrCallback
var start *time.Time
var runners []time.Duration

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
			exit <- true
			continue
		}
		//fmt.Printf("Received message - %#v\n", m)
		for x, button := range buttons {
			if m.buttons&button == button {
				if !buttonStatus[x] {
					buttonChan <- button
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
	//func goErrCallback(wm *C.cwiid_wiimote_t, char *C.char, ap C.va_list) {s
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
		exit <- true
	default:
		fmt.Printf("Inside error calback - %s\n", str)
	}
}

func main() {
	buttonStatus = make([]bool, len(buttons))
	var bdaddr C.bdaddr_t
	var wm *C.struct_cwiid_wiimote_t
	buttonChan = make(chan _Ctype_uint16_t, 1)
	exit = make(chan bool, 1)
	ticker := time.NewTicker(time.Second)
	runners = make([]time.Duration, 0, 1024)
	val, err := C.cwiid_set_err(C.getErrCallback())
	if val != 0 || err != nil {
		fmt.Printf("Error setting the callback to catch errors - %d - %v", val, err)
		os.Exit(1)
	}
	for {
	outer:
		for {
			// clear both channels
			select {
			case <-buttonChan:
			case <-exit:
			default:
				break outer
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
	loop:
		for {
			select {
			case <-exit:
				fmt.Println("Wiimote lost connection!")
				break loop
			case button := <-buttonChan:
				switch button {
				case C.CWIID_BTN_A:
					if start == nil {
						start = new(time.Time)
						*start = time.Now()
						fmt.Printf("Race started @ %s\n", start.Format("3:04:05"))
						runners = runners[:0]
					} else {
						runners = append(runners, time.Now().Sub(*start))
						diff := runners[len(runners)-1]
						fmt.Printf("#%d - %s\n", len(runners), diff)
					}
				//case C.CWIID_BTN_B:
				//	fmt.Println("B")
				//case C.CWIID_BTN_1:
				//	fmt.Println("1")
				//case C.CWIID_BTN_2:
				//	fmt.Println("2")
				//case C.CWIID_BTN_MINUS:
				//	fmt.Println("Minus")
				case C.CWIID_BTN_HOME:
					fmt.Println("Race finished!")
					return
					//case C.CWIID_BTN_LEFT:
					//	fmt.Println("Left")
					//case C.CWIID_BTN_RIGHT:
					//	fmt.Println("Right")
					//case C.CWIID_BTN_DOWN:
					//	fmt.Println("Down")
					//case C.CWIID_BTN_UP:
					//	fmt.Println("Up")
					//case C.CWIID_BTN_PLUS:
					//	fmt.Println("Plus")
				}
			case now := <-ticker.C:
				if start != nil {
					diff := now.Sub(*start)
					fmt.Printf("%00f:%00f:%00f\n", diff.Hours(), diff.Minutes(), diff.Seconds())
				}
				// update the clock
			}
		}
	}
}
