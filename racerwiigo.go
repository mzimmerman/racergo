package main

/*
#cgo pkg-config: /usr/lib/pkgconfig/cwiid.pc
#include "racerwiigo.h"
#include <stdlib.h>
#include <cwiid.h>
#include <time.h>
#include <bluetooth/bluetooth.h>
*/
import "C"

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

const (
	Disconnected int8 = iota
	Error        int8 = iota
	Finished     int8 = iota
)

var buttonStatus []bool

var racerChan chan time.Time
var statusChan chan int8
var callback = goCwiidCallback // so it's not garbage collected
var errCallback = goErrCallback
var start *time.Time
var optionalEntryFields []string
var bibbedEntries map[int]*Entry   // map of Bib #s
var unbibbedEntries map[int]*Entry // map of sequential Ids
var results []*Result
var raceResultsTemplate *template.Template
var errorTemplate *template.Template
var useWiimote = true
var wiimoteConnected = false
var prizes []*Prize
var mutex sync.Mutex
var serverHandlers chan bool

type HumanDuration time.Duration

type Prize struct {
	Title    string
	LowAge   uint
	HighAge  uint
	Gender   string    // M = only males, F = only Females, O = Overall
	Amount   uint      // how many people win this prize?
	WinAgain bool      // if someone has already won another Prize, can they win this again?
	Winners  []*Result `json:"-"`
}

type Entry struct {
	Bib      int
	Fname    string
	Lname    string
	Male     bool
	Age      uint
	Optional []string
	Result   *Result
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
			statusChan <- Error
			continue
		}
		for x, button := range buttons {
			if m.buttons&button == button {
				if !buttonStatus[x] {
					if button == C.CWIID_BTN_A {
						racerChan <- time.Now()
					} else if button == C.CWIID_BTN_HOME {
						statusChan <- Finished
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
		statusChan <- Disconnected
	default:
		fmt.Printf("Inside error calback - %s\n", str)
		statusChan <- Error
	}
}

func download(w http.ResponseWriter, r *http.Request) {
	filename := fmt.Sprintf("raceresults-%s.csv", time.Now().In(time.Local).Format("2006-01-02"))
	w.Header().Set("Content-type", "application/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	mutex.Lock()
	length := len(unbibbedEntries)
	if length > len(results) {
		length = len(results)
	}
	csvData := make([][]string, 0, length+1)
	headerRow := append([]string{"Fname", "Lname", "Age", "Bib", "Overall Place", "Time"}, optionalEntryFields...)
	blank := make([]string, len(optionalEntryFields))
	csvData = append(csvData, headerRow)
	for _, result := range results {
		if result.Entry == nil {
			csvData = append(csvData, append([]string{"", "", "", "", strconv.Itoa(int(result.Place)), result.Time.String()}, blank...))
		} else {
			csvData = append(csvData, append([]string{result.Entry.Fname, result.Entry.Lname, strconv.Itoa(int(result.Entry.Age)), strconv.Itoa(int(result.Entry.Bib)), strconv.Itoa(int(result.Place)), result.Time.String()}, result.Entry.Optional...))
		}
	}
	for _, entry := range unbibbedEntries {
		csvData = append(csvData, append([]string{entry.Fname, entry.Lname, strconv.Itoa(int(entry.Age)), "", "", ""}, entry.Optional...))
	}
	mutex.Unlock()
	writer := csv.NewWriter(w)
	writer.WriteAll(csvData)
	writer.Flush()
}

func uploadPrizes(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		showErrorForAdmin(w, "Error getting Reader - %s", err)
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		showErrorForAdmin(w, "Error getting Part - %s", err)
		return
	}
	jsonin := json.NewDecoder(part)
	mutex.Lock()
	prizes = make([]*Prize, 0)
	for {
		var prize Prize
		err = jsonin.Decode(&prize)
		if err == io.EOF {
			break // good, we processed them all!
		}
		if err != nil {
			showErrorForAdmin(w, "Error fetching Prize Configurations - %s", err)
			mutex.Unlock()
			return
		}
		prizes = append(prizes, &prize)
	}
	for _, result := range results {
		if result.Entry == nil {
			break // all done
		}
		calculatePrizes(result)
	}
	mutex.Unlock()
	http.Redirect(w, r, "/admin", 301)
}

func calculatePrizes(r *Result) {
	// prizes are calculated from top-down, meaning all "faster" racers have already been placed
	if r.Entry == nil {
		return // can't calculate prizes for someone who hasn't finished the race!
	}
	found := false
	// mutex should already be locked in the parent caller
	for _, prize := range prizes {
		switch {
		case found && !prize.WinAgain:
			fallthrough
		case r.Entry.Age < prize.LowAge:
			fallthrough
		case r.Entry.Age > prize.HighAge:
			fallthrough
		case r.Entry.Male && (prize.Gender == "F"):
			fallthrough
		case !r.Entry.Male && (prize.Gender == "M"):
			fallthrough
		case len(prize.Winners) == int(prize.Amount):
			continue // do not qualify any of these conditions
		}
		found = true
		prize.Winners = append(prize.Winners, r)
	}
}

func uploadRacers(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		showErrorForAdmin(w, "Error getting Reader - %s", err)
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		showErrorForAdmin(w, "Error getting Part - %s", err)
		return
	}
	csvIn := csv.NewReader(part)
	rawEntries, err := csvIn.ReadAll()
	if err != nil {
		showErrorForAdmin(w, "Error Reading CSV file - %s", err)
		return
	}
	if len(rawEntries) <= 1 {
		showErrorForAdmin(w, "Either blank file or only supplied the header row")
		return
	}
	mutex.Lock()

	// make the maps and unlink all previous relationships
	bibbedEntries = make(map[int]*Entry)
	unbibbedEntries = make(map[int]*Entry)
	for _, prize := range prizes {
		prize.Winners = make([]*Result, 0)
	}
	for _, result := range results {
		result.Entry = nil
	}
	// initialize the optionalEntryFields for use when we export/display the data
	optionalEntryFields = make([]string, 0)
	for col := range rawEntries[0] {
		switch rawEntries[0][col] {
		case "Fname":
		case "Lname":
		case "Age":
		case "Gender":
		case "Bib":
		default:
			optionalEntryFields = append(optionalEntryFields, rawEntries[0][col])
		}
	}
	// load the data
	for row := 1; row < len(rawEntries); row++ {
		entry := &Entry{Bib: -1}
		entry.Optional = make([]string, 0)
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
				entry.Bib, err = strconv.Atoi(rawEntries[row][col])
				if err != nil {
					entry.Bib = -1
				}
			default:
				entry.Optional = append(entry.Optional, rawEntries[row][col])
			}
		}
		if entry.Bib < 0 {
			unbibbedEntries[row] = entry
		} else {
			bibbedEntries[entry.Bib] = entry
		}
	}
	mutex.Unlock()
	http.Redirect(w, r, "/admin", 301)
}

func linkBib(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	next, err := strconv.Atoi(r.Form.Get("next"))
	if err != nil {
		showErrorForAdmin(w, "Error %s getting next", err)
		return
	}
	if next > len(results) {
		showErrorForAdmin(w, "Overall place runner #%d has not crossed the finish line yet", next)
		return
	}
	bib, err := strconv.Atoi(r.Form.Get("bib"))
	if bib < 0 {
		showErrorForAdmin(w, "Cannot assign a negative bib number of %d", bib)
		return
	}
	mutex.Lock()
	if err == nil {
		if _, ok := bibbedEntries[bib]; ok {
			if bibbedEntries[bib].Result != nil {
				showErrorForAdmin(w, "Bib number %d already crossed the finish line in place #%d", bib, bibbedEntries[bib].Result.Place)
				mutex.Unlock()
				return
			}
			recalculateAll := results[next-1].Entry != nil
			// remove the old association if there was any
			if recalculateAll {
				bibbedEntries[results[next-1].Entry.Bib].Result = nil
				results[next-1].Entry = nil
			}
			// make the new association
			results[next-1].Entry = bibbedEntries[bib]
			bibbedEntries[bib].Result = results[next-1]
			fmt.Printf("Set bib for place %d to %d\n", next, bib)
			if recalculateAll {
				recomputeAllPrizes()
			} else {
				calculatePrizes(results[next-1])
			}
		} else {
			showErrorForAdmin(w, "Bib number %d was not assigned to anyone.", bib)
			mutex.Unlock()
			return
		}
	} else {
		showErrorForAdmin(w, "Error %s setting bib for place %d to %d", err, next, bib)
		mutex.Unlock()
		return
	}
	mutex.Unlock()
	http.Redirect(w, r, "/admin", 301)
	return
}

func showErrorForAdmin(w http.ResponseWriter, message string, args ...interface{}) {
	w.WriteHeader(409) // conflict header, most likely due to old information in the client
	msg := fmt.Sprintf(message, args...)
	fmt.Println(msg)
	if errorTemplate == nil {
		fmt.Fprintf(w, msg)
		return
	}
	err := errorTemplate.Execute(w, msg)
	if err != nil {
		fmt.Fprintf(w, "Error executing template - %s", err)
	}
}

func removeRacer(w http.ResponseWriter, r *http.Request) {
	place, err := strconv.Atoi(r.FormValue("place"))
	if err != nil {
		showErrorForAdmin(w, "Error %s getting place", err)
		return
	}
	mutex.Lock()
	if place <= len(results) && place > 0 {
		newresults := make([]*Result, len(results)-1)
		x := copy(newresults, results[:place-1]) + 1
		for {
			if x < len(results) {
				results[x].Place = uint(x) // bump the place down one to its index
				newresults[x-1] = results[x]
				x++
			} else {
				break
			}
		}
		results = newresults
	} else {
		showErrorForAdmin(w, "Could not remove runner in place %d", place)
		mutex.Unlock()
		return
	}
	recomputeAllPrizes()
	mutex.Unlock()
	http.Redirect(w, r, "/admin", 301)
	return
}

// mutex should be locked already when calling this
func recomputeAllPrizes() {
	// now need to recompute the prize results
	for _, prize := range prizes {
		prize.Winners = make([]*Result, 0)
	}
	for _, result := range results {
		if result.Entry == nil {
			break // all done
		}
		calculatePrizes(result)
	}
}

func assignBib(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		showErrorForAdmin(w, "Error %s getting next", err)
		return
	}
	bib, err := strconv.Atoi(r.FormValue("bib"))
	if bib < 0 || err != nil {
		showErrorForAdmin(w, "Could not get a valid bib number from %s", r.FormValue("bib"))
		return
	}
	mutex.Lock()

	if entry, ok := unbibbedEntries[id]; ok {
		if _, ok = bibbedEntries[bib]; ok {
			showErrorForAdmin(w, "Bib # %d already assigned to %s %s!", bib, bibbedEntries[bib].Fname, bibbedEntries[bib].Lname)
			mutex.Unlock()
			return
		}
		entry.Bib = bib
		fmt.Printf("Set bib for %s %s to %d", entry.Fname, entry.Lname, bib)
		delete(unbibbedEntries, id)
		bibbedEntries[entry.Bib] = entry
	} else {
		showErrorForAdmin(w, "Id %d was not assigned to anyone.", id)
		mutex.Unlock()
		return
	}
	mutex.Unlock()
	http.Redirect(w, r, "/admin", 301)
	return
}

func addEntry(w http.ResponseWriter, r *http.Request) {
	entry := &Entry{}
	age, err := strconv.Atoi(r.FormValue("Age"))
	if age < 0 {
		showErrorForAdmin(w, "Not a valid age, must be >= 0")
		return
	}
	if err != nil {
		showErrorForAdmin(w, "Error %s getting Age", err)
		return
	}
	entry.Age = uint(age)
	entry.Bib, err = strconv.Atoi(r.FormValue("Bib"))
	if entry.Bib < 0 {
		showErrorForAdmin(w, "Not a valid bib, must be >= 0")
		return
	}
	if err != nil {
		showErrorForAdmin(w, "Error %s getting Bib", err)
		return
	}
	entry.Fname = r.FormValue("Fname")
	entry.Lname = r.FormValue("Lname")
	entry.Male = r.FormValue("Male") == "true"
	entry.Optional = make([]string, 0)
	mutex.Lock()
	for _, s := range optionalEntryFields {
		entry.Optional = append(entry.Optional, r.FormValue(s))
	}
	if bibbedEntries == nil {
		bibbedEntries = make(map[int]*Entry)
	}
	bibbedEntries[entry.Bib] = entry
	fmt.Printf("Added Entry - %#v\n", entry)
	mutex.Unlock()
	http.Redirect(w, r, "/admin", 301)
	return
}

func handler(w http.ResponseWriter, r *http.Request) {
	mutex.Lock()
	data := map[string]interface{}{"Racers": results}
	if strings.HasSuffix(r.RequestURI, "admin") {
		data["Admin"] = true
		if len(unbibbedEntries) > 0 {
			data["Unbibbed"] = unbibbedEntries
			data["Optional"] = optionalEntryFields
		}
		for x := range results {
			if results[x].Entry == nil {
				data["Next"] = results[x].Place
				break
			}
		}
		if _, ok := data["Next"]; !ok {
			data["Next"] = len(results) + 1
		}
		data["WiimoteConnected"] = wiimoteConnected
		data["Fields"] = optionalEntryFields
	}
	if start != nil {
		diff := time.Since(*start)
		data["Start"] = start.Format("3:04:05")
		data["Time"] = HumanDuration(diff).Clock()
		data["Seconds"] = fmt.Sprintf("%.0f", diff.Seconds())
		data["NextUpdate"] = diff / time.Millisecond % 1000
		data["Prizes"] = prizes
	}
	mutex.Unlock()
	<-serverHandlers // wait until a goroutine to handle http requests is free
	raceResultsTemplate, _ = template.ParseFiles("raceResults.template")
	err := raceResultsTemplate.Execute(w, data)
	if err != nil {
		fmt.Fprintf(w, "Error executing template - %s", err)
	}
	serverHandlers <- true // wait for handler to finish, then put it back in the queue so another goroutine can spawn
}

func uploadFile(filename string) (*http.Request, error) {
	// Create buffer
	buf := new(bytes.Buffer) // caveat IMO dont use this for large files, \
	// create a tmpfile and assemble your multipart from there (not tested)
	w := multipart.NewWriter(buf)
	// Create a form field writer for field label
	fw, err := w.CreateFormFile("upload", filename)
	if err != nil {
		return nil, err
	}
	fd, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	// Write file field from file to upload
	_, err = io.Copy(fw, fd)
	if err != nil {
		return nil, err
	}
	// Important if you do not close the multipart writer you will not have a
	// terminating boundry
	w.Close()
	req, err := http.NewRequest("POST", "", buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req, nil
	//io.Copy(os.Stderr, res.Body) // Replace this with Status.Code check
}

func main() {
	numHandlers := runtime.NumCPU()
	runtime.GOMAXPROCS(numHandlers)
	if numHandlers >= 2 {
		// want to leave one cpu not handling racer http requests so as to handle the wiimote quickly
		numHandlers--
	}
	serverHandlers = make(chan bool, numHandlers)
	for x := 0; x < numHandlers; x++ {
		serverHandlers <- true // fill the channel with valid goroutines
	}
	var err error
	raceResultsTemplate, err = template.ParseFiles("raceResults.template")
	if err != nil {
		fmt.Printf("Error parsing template! - %s\n", err)
		return
	}
	errorTemplate, err = template.ParseFiles("error.template")
	if err != nil {
		fmt.Printf("Error parsing template! - %s\n", err)
		return
	}
	ready := make(chan bool)
	go raceFunc(ready)
	<-ready
	req, err := uploadFile("prizes.json")
	if err == nil {
		resp := httptest.NewRecorder()
		uploadPrizes(resp, req)
		if resp.Code != 301 {
			fmt.Println("Unable to load the default prizes.json file.")
		}
	} else {
		fmt.Printf("Unable to load the default prizes.json file - %v\n", err)
	}
	if !useWiimote { // simulate the pressing of the wiimote A button for testing
		go func() {
			simulButton := time.NewTicker(time.Second * 10)
			racerChan <- time.Now() // start race immediately
			for {
				select {
				case <-simulButton.C:
					racerChan <- time.Now()
				}
			}
		}()
	}
	http.HandleFunc("raceresults/", handler)
	http.HandleFunc("raceresults/admin", handler)
	http.HandleFunc("raceresults/linkBib", linkBib)
	http.HandleFunc("raceresults/assignBib", assignBib)
	http.HandleFunc("raceresults/addEntry", addEntry)
	http.HandleFunc("raceresults/download", download)
	http.HandleFunc("raceresults/uploadRacers", uploadRacers)
	http.HandleFunc("raceresults/uploadPrizes", uploadPrizes)
	http.HandleFunc("raceresults/removeRacer", removeRacer)
	http.Handle("raceresults/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))
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

func raceFunc(ready chan bool) {
	buttonStatus = make([]bool, len(buttons))
	var bdaddr C.bdaddr_t
	var wm *C.struct_cwiid_wiimote_t
	mutex.Lock()
	start = nil
	racerChan = make(chan time.Time, 10) // queue up to 10 racers at a time, since we're storing the time they crossed, we don't have to display/process them right away
	statusChan = make(chan int8, 1)
	ready <- true
	ticker := time.NewTicker(time.Second * 10)
	results = make([]*Result, 0, 1024)
	mutex.Unlock()
	tty, err := os.OpenFile("/dev/tty0", os.O_RDWR, 0)
	if err != nil {
		fmt.Printf("Error playing sound! - %v\n", err)
	} else {
		defer tty.Close()
	}
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
			// clear the status channel for any previous errors
			select {
			case <-statusChan:
			default:
				break outer
			}
		}
		if tty != nil {
			fmt.Fprintf(tty, "\x07")
		}
		if useWiimote {
			fmt.Println("Press 1&2 on the Wiimote now")
			wm, err = C.cwiid_open(&bdaddr, 0)
			if err != nil {
				fmt.Errorf("cwiid_open: %v\n", err)
				continue
			}
			if wm == nil {
				continue // could not connect to wiimote
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
		}
		mutex.Lock()
		wiimoteConnected = true // it may be that useWiimote = false, in that case, we still want to "fake" that the wiimote is connected
		mutex.Unlock()
	loop:
		for {
			select {
			case status := <-statusChan:
				if status == Finished {
					ticker.Stop()
					ready <- false
					// just stop listening for button presses on the wiimote, the race website can continue to run
					// leave the WiimoteConnected status as it's only for alerting the race admins if the Wiimote loses connection
					return
				} else if status == Disconnected {
					fmt.Println("Wiimote lost connection")
					mutex.Lock()
					wiimoteConnected = false
					wm = nil
					mutex.Unlock()
					break loop // this takes us to the large loop above so that the wiimote can reconnect
				} else if status == Error {
					fmt.Println("An error occurred when communicating with the wiimote")
					mutex.Lock()
					wiimoteConnected = false
					wm = nil
					mutex.Unlock()
					break loop // this takes us to the large loop above so that the wiimote can reconnect
				}
			case t := <-racerChan:
				// play sound every time that the A button is handled from the Wiimote
				if tty != nil {
					fmt.Fprintf(tty, "\x07")
				}
				mutex.Lock()
				if start == nil {
					start = &t
					ticker.Stop() // stop and "upgrade" the ticker for every second to track time
					ticker = time.NewTicker(time.Second)
					fmt.Printf("Race started @ %s\n", start.Format("3:04:05"))
					results = results[:0]
				} else {
					place := len(results)
					results = append(results, &Result{Place: uint(place + 1), Time: HumanDuration(t.Sub(*start))})
					fmt.Printf("#%d - %s\n", results[place].Place, results[place].Time)
				}
				mutex.Unlock()
			case now := <-ticker.C:
				mutex.Lock()
				if start != nil {
					fmt.Println(HumanDuration(now.Sub(*start)))
				} else {
					fmt.Println("Waiting to start the race")
				}
				mutex.Unlock()
				// update the clock
			}
		}
	}
}
