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
	"encoding/csv"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
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
var entries map[uint]*Entry // map of Bib #s
var results []*Result
var raceResultsTemplate *template.Template
var useWiimote = false

type HumanDuration time.Duration

type Entry struct {
	Id     uint
	Bib    uint
	Fname  string
	Lname  string
	Male   bool
	Age    uint
	Result *Result
}

type Result struct {
	Time  HumanDuration
	Place uint
	Entry *Entry
}

func (hd HumanDuration) String() string {
	seconds := time.Duration(hd).Seconds()
	seconds -= float64(time.Duration(hd) / time.Minute * 60)
	return fmt.Sprintf("%#02d:%#02d:%05.2f", time.Duration(hd)/time.Hour, time.Duration(hd)/time.Minute%60, seconds)
}

func (hd HumanDuration) Clock() string {
	return fmt.Sprintf("%#02d:%#02d:%02d", time.Duration(hd)/time.Hour, time.Duration(hd)/time.Minute%60, time.Duration(hd)/time.Second%60)
}

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

func download(w http.ResponseWriter, r *http.Request) {
	filename := fmt.Sprintf("raceresults-%s.csv", time.Now().In(time.Local).Format("2006-01-02"))
	w.Header().Set("Content-type", "application/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	length := len(entries)
	if length > len(results) {
		length = len(results)
	}
	csvData := make([][]string, 0, length+1)
	csvData = append(csvData, []string{"Fname", "Lname", "Age", "Bib", "Overall Place", "Time"})
	for _, result := range results {
		if result.Entry == nil {
			csvData = append(csvData, []string{"", "", "", "", strconv.Itoa(int(result.Place)), result.Time.String()})
		} else {
			csvData = append(csvData, []string{result.Entry.Fname, result.Entry.Lname, strconv.Itoa(int(result.Entry.Age)), strconv.Itoa(int(result.Entry.Bib)), strconv.Itoa(int(result.Place)), result.Time.String()})
		}
	}
	for _, entry := range entries {
		if entry.Result == nil {
			// did not account for this one above
			csvData = append(csvData, []string{entry.Fname, entry.Lname, strconv.Itoa(int(entry.Age)), "", "", ""})
		}
	}
	writer := csv.NewWriter(w)
	writer.WriteAll(csvData)
	writer.Flush()
}

func uploadRacers(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		fmt.Fprintf(w, "Error getting Reader - %s", err)
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		fmt.Fprintf(w, "Error getting Part - %s", err)
		return
	}
	csvIn := csv.NewReader(part)
	rawEntries, err := csvIn.ReadAll()
	if err != nil {
		fmt.Fprintf(w, "Error Reading CSV file - %s", err)
		return
	}
	if len(rawEntries) <= 1 {
		fmt.Fprintf(w, "Either blank file or only supplied the header row")
		return
	}
	// make the map and unlink all previous relationships (if any)
	entries = make(map[uint]*Entry)
	for _, result := range results {
		result.Entry = nil
	}
	for row := 1; row < len(rawEntries); row++ {
		entry := &Entry{Id: uint(row)}
		for col := range rawEntries[row] {
			switch rawEntries[0][col] {
			case "Fname":
				entry.Fname = rawEntries[row][col]
			case "Lname":
				entry.Lname = rawEntries[row][col]
			case "Age":
				tmpAge, _ := strconv.Atoi(rawEntries[row][col])
				entry.Age = uint(tmpAge)
			case "Gender":
				entry.Male = (rawEntries[row][col] == "M")
			case "Bib":
				tmpBib, _ := strconv.Atoi(rawEntries[row][col])
				entry.Bib = uint(tmpBib)
			default:
				fmt.Printf("Field %s not imported, dropping\n", rawEntries[0][col])
			}
		}
		fmt.Printf("Read entry - %v", entry)
		fmt.Printf("CSV Input = %v", rawEntries[row])
		if entry.Bib != 0 {
			entries[entry.Bib] = entry
		} else {
			fmt.Printf("Skipping due to no bib assigned - %v", entry)
		}
	}
	http.Redirect(w, r, "/admin", 301)
	return
}

func bibHandler(w http.ResponseWriter, r *http.Request) {
	next, err := strconv.Atoi(r.FormValue("next"))
	if err != nil || next > len(results) {
		fmt.Fprintf(w, "Error %s getting next", err)
		return
	}
	tempBib, err := strconv.Atoi(r.FormValue("bib"))
	if tempBib < 0 {
		fmt.Fprintf(w, "Cannot assign a negative bib number of %d", tempBib)
		return
	}
	bib := uint(tempBib)
	if err == nil {
		if _, ok := entries[bib]; ok {
			results[next-1].Entry = entries[bib]
			entries[bib].Result = results[next-1]
			fmt.Printf("Set bib for place %d to %d", next, bib)
		} else {
			fmt.Fprintf(w, "Bib number %d was not assigned to anyone.", bib)
			return
		}
	} else {
		fmt.Printf("Error %s setting bib for place %d to %d", err, next, bib)
	}
	http.Redirect(w, r, "/admin", 301)
	return
}

func handler(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{"Racers": results}
	if start != nil {
		diff := time.Since(*start)
		data["Start"] = start.Format("3:04:05")
		data["Time"] = HumanDuration(diff).Clock()
		data["Seconds"] = fmt.Sprintf("%.0f", diff.Seconds())
		data["NextUpdate"] = diff / time.Millisecond % 1000
		if strings.HasSuffix(r.RequestURI, "admin") {
			data["Admin"] = true
			for x := range results {
				if results[x].Entry == nil {
					data["Next"] = results[x].Place
					break
				}
			}
		}
	}
	raceResultsTemplate, err := template.ParseFiles("raceResults.template")
	if err != nil {
		fmt.Fprintf(w, "Error parsing template - %s", err)
	} else {
		err = raceResultsTemplate.Execute(w, data)
		if err != nil {
			fmt.Fprintf(w, "Error executing template - %s", err)
		}
	}
}

func main() {
	var err error
	raceResultsTemplate, err = template.ParseFiles("raceResults.template")
	if err != nil {
		fmt.Printf("Error parsing template! - %s\n", err)
		return
	}
	go raceFunc()
	http.HandleFunc("raceresults/", handler)
	http.HandleFunc("raceresults/admin", handler)
	http.HandleFunc("raceresults/bib", bibHandler)
	http.HandleFunc("raceresults/download", download)
	http.HandleFunc("raceresults/uploadRacers", uploadRacers)
	http.Handle("/", http.RedirectHandler("http://raceresults/", 307))
	err = http.ListenAndServe(":80", nil)
	if err != nil {
		fmt.Printf("Error starting http server! - %s\n", err)
		err = http.ListenAndServe(":8080", nil)
		if err != nil {
			fmt.Printf("Error starting http server! - %s\n", err)
			return
		}
	}
}

func raceFunc() {
	buttonStatus = make([]bool, len(buttons))
	var bdaddr C.bdaddr_t
	var wm *C.struct_cwiid_wiimote_t
	buttonChan = make(chan _Ctype_uint16_t, 1)
	exit = make(chan bool, 1)
	ticker := time.NewTicker(time.Second)
	results = make([]*Result, 0, 1024)
	var err error
	if useWiimote {
		val, err := C.cwiid_set_err(C.getErrCallback())
		if val != 0 || err != nil {
			fmt.Printf("Error setting the callback to catch errors - %d - %v", val, err)
			os.Exit(1)
		}
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
		if useWiimote {
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
		} else { // simulate the pressing of the wiimote A button for testing
			go func() {
				simulButton := time.NewTicker(time.Second * 10)
				buttonChan <- C.CWIID_BTN_A // start race immediately
				//buttonChan <- C.CWIID_BTN_A // first runner! :)
				for {
					select {
					case <-simulButton.C:
						buttonChan <- C.CWIID_BTN_A
					}
				}
			}()
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
						results = results[:0]
					} else {
						place := len(results)
						results = append(results, &Result{Place: uint(place + 1), Time: HumanDuration(time.Now().Sub(*start))})
						fmt.Printf("#%d - %s\n", results[place].Place, results[place].Time)
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
					diff := HumanDuration(now.Sub(*start))
					fmt.Println(diff)
				}
				// update the clock
			}
		}
	}
}
