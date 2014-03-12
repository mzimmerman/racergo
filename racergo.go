package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var startRaceChan chan time.Time
var raceHasStarted bool = false
var raceStart time.Time
var optionalEntryFields []string
var bibbedEntries map[int]*Entry   // map of Bib #s
var unbibbedEntries map[int]*Entry // map of sequential Ids
var results []*Result
var auditLog []Audit
var raceResultsTemplate *template.Template
var errorTemplate *template.Template
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

type Audit struct {
	Time HumanDuration
	Bib  int
}

type Result struct {
	Time      HumanDuration
	Place     uint
	Entry     *Entry
	Confirmed bool
}

func (hd HumanDuration) String() string {
	seconds := time.Duration(hd).Seconds()
	seconds -= float64(time.Duration(hd) / time.Minute * 60)
	return fmt.Sprintf("%#02d:%#02d:%05.2f", time.Duration(hd)/time.Hour, time.Duration(hd)/time.Minute%60, seconds)
}

func (hd HumanDuration) Clock() string {
	return fmt.Sprintf("%#02d:%#02d:%02d", time.Duration(hd)/time.Hour, time.Duration(hd)/time.Minute%60, time.Duration(hd)/time.Second%60)
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
	headerRow := append([]string{"Fname", "Lname", "Age", "Gender", "Bib", "Overall Place", "Time"}, optionalEntryFields...)
	blank := make([]string, len(optionalEntryFields))
	csvData = append(csvData, headerRow)
	for _, result := range results {
		if result.Entry == nil {
			csvData = append(csvData, append([]string{"", "", "", "", "", strconv.Itoa(int(result.Place)), result.Time.String()}, blank...))
		} else {
			csvData = append(csvData, append([]string{result.Entry.Fname, result.Entry.Lname, strconv.Itoa(int(result.Entry.Age)), gender(result.Entry.Male), strconv.Itoa(int(result.Entry.Bib)), strconv.Itoa(int(result.Place)), result.Time.String()}, result.Entry.Optional...))
		}
	}
	for _, entry := range unbibbedEntries {
		csvData = append(csvData, append([]string{entry.Fname, entry.Lname, strconv.Itoa(int(entry.Age)), gender(entry.Male), "", "", ""}, entry.Optional...))
	}
	for _, entry := range bibbedEntries {
		if entry.Result != nil {
			continue // already included in results export
		}
		csvData = append(csvData, append([]string{entry.Fname, entry.Lname, strconv.Itoa(int(entry.Age)), gender(entry.Male), strconv.Itoa(entry.Bib), "", ""}, entry.Optional...))
	}
	mutex.Unlock()
	writer := csv.NewWriter(w)
	writer.WriteAll(csvData)
	writer.Flush()
}

func gender(male bool) string {
	if male {
		return "M"
	}
	return "F"
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
	defer mutex.Unlock()
	prizes = make([]*Prize, 0)
	for {
		var prize Prize
		err = jsonin.Decode(&prize)
		if err == io.EOF {
			break // good, we processed them all!
		}
		if err != nil {
			showErrorForAdmin(w, "Error fetching Prize Configurations - %s", err)
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
	defer mutex.Unlock()

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
	http.Redirect(w, r, "/admin", 301)
}

func startHandler(w http.ResponseWriter, r *http.Request) {
	raceStart = time.Now()
	raceHasStarted = true
	startRaceChan <- raceStart
	http.Redirect(w, r, "/admin", 301)
}

func init() {
	startRaceChan = make(chan time.Time)
	go listenForRacers()
	numHandlers := runtime.NumCPU()
	runtime.GOMAXPROCS(numHandlers)
	if numHandlers >= 2 {
		// want to leave one cpu not handling racer http requests so as to handle the processing of racers quickly
		numHandlers--
	}
	serverHandlers = make(chan bool, numHandlers)
	for x := 0; x < numHandlers; x++ {
		serverHandlers <- true // fill the channel with valid goroutines
	}
	var err error
	raceResultsTemplate, err = template.ParseFiles("raceResults.template")
	if err != nil {
		log.Fatalf("Error parsing template! - %s\n", err)
		return
	}
	errorTemplate, err = template.ParseFiles("error.template")
	if err != nil {
		log.Fatalf("Error parsing template! - %s\n", err)
		return
	}
}

func linkBib(w http.ResponseWriter, r *http.Request) {
	if !raceHasStarted {
		showErrorForAdmin(w, "Cannot link a bib, the race hasn't started!")
		return
	}
	removeBib := r.FormValue("remove") == "true"
	bib, err := strconv.Atoi(r.FormValue("bib"))
	if err != nil {
		showErrorForAdmin(w, "Error %s getting bib number", err)
		return
	}
	if bib < 0 {
		showErrorForAdmin(w, "Cannot assign a negative bib number of %d", bib)
		return
	}
	deltaT := HumanDuration(time.Since(raceStart))
	mutex.Lock()
	defer mutex.Unlock()
	auditLog = append(auditLog, Audit{deltaT, bib})
	entry, ok := bibbedEntries[bib]
	if !ok {
		showErrorForAdmin(w, "Bib number %d was not assigned to anyone.", bib)
		return
	}
	if removeBib {
		if entry.Result == nil {
			// entry already removed, act successful
			http.Redirect(w, r, "/admin", 301)
			return
		}
		index := int(entry.Result.Place) - 1
		log.Printf("Bib = %d, index = %d, len(results) = %d", bib, index, len(results))
		entry.Result = nil
		if index >= len(results) {
			// something's out of whack here -- The Entry has a Result but the Result isn't in the results slice
			// the fix is removing the entry's result which happens before this if statement
			showErrorForAdmin(w, "Bib has a result recorded but is not in the results table! - attempted to fix it")
			return
		}
		results = append(results[:index], results[index+1:]...)
		for x := index; x < len(results); x++ {
			results[x].Place = results[x].Place - 1
		}
		http.Redirect(w, r, "/admin", 301)
		return
	}
	if entry.Result != nil {
		if !entry.Result.Confirmed {
			entry.Result.Confirmed = true
			http.Redirect(w, r, "/admin", 301)
			return
		}
		showErrorForAdmin(w, "Bib number %d already confirmed for place #%d", bib, entry.Result.Place)
		return
	}
	result := &Result{
		Time:      deltaT,                 // Larry modifed to use delta time
		Place:     uint(len(results) + 1), // Larry explicit cast
		Confirmed: false,
		Entry:     entry,
	}
	results = append(results, result)
	entry.Result = result
	log.Printf("Set bib for place %d to %d\n", result.Place, bib)
	calculatePrizes(result)
	http.Redirect(w, r, "/admin", 301)
	return
}

func showErrorForAdmin(w http.ResponseWriter, message string, args ...interface{}) {
	w.WriteHeader(409) // conflict header, most likely due to old information in the client
	msg := fmt.Sprintf(message, args...)
	log.Println(msg)
	if errorTemplate == nil {
		fmt.Fprintf(w, msg)
		return
	}
	err := errorTemplate.Execute(w, msg)
	if err != nil {
		fmt.Fprintf(w, "Error executing template - %s", err)
	}
}

// mutex needs to be locked already when calling this
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
	defer mutex.Unlock()

	if entry, ok := unbibbedEntries[id]; ok {
		if _, ok = bibbedEntries[bib]; ok {
			showErrorForAdmin(w, "Bib # %d already assigned to %s %s!", bib, bibbedEntries[bib].Fname, bibbedEntries[bib].Lname)
			return
		}
		entry.Bib = bib
		log.Printf("Set bib for %s %s to %d", entry.Fname, entry.Lname, bib)
		delete(unbibbedEntries, id)
		bibbedEntries[entry.Bib] = entry
	} else {
		showErrorForAdmin(w, "Id %d was not assigned to anyone.", id)
		return
	}
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
	defer mutex.Unlock()
	for _, s := range optionalEntryFields {
		entry.Optional = append(entry.Optional, r.FormValue(s))
	}
	if bibbedEntries == nil {
		bibbedEntries = make(map[int]*Entry)
	}
	bibbedEntries[entry.Bib] = entry
	log.Printf("Added Entry - %#v\n", entry)
	http.Redirect(w, r, "/admin", 301)
	return
}

func handler(w http.ResponseWriter, r *http.Request) {
	<-serverHandlers // wait until a goroutine to handle http requests is free
	mutex.Lock()
	defer func() {
		serverHandlers <- true // wait for handler to finish, then put it back in the queue so another goroutine can spawn
		mutex.Unlock()
	}()
	data := map[string]interface{}{"Racers": results}
	if strings.HasSuffix(r.RequestURI, "admin") {
		data["Admin"] = true
		if len(unbibbedEntries) > 0 {
			data["Unbibbed"] = unbibbedEntries
		}
		data["Fields"] = optionalEntryFields
		end := len(results) - 10
		if end < 0 {
			end = 0
		}
		data["RecentRacers"] = results[end:]
	}
	if raceHasStarted {
		diff := time.Since(raceStart)
		data["Start"] = raceStart.Format("3:04:05")
		data["Time"] = HumanDuration(diff).Clock()
		data["Seconds"] = fmt.Sprintf("%.0f", diff.Seconds())
		data["NextUpdate"] = diff / time.Millisecond % 1000
		data["Prizes"] = prizes
	}
	raceResultsTemplate, _ = template.ParseFiles("raceResults.template")
	err := raceResultsTemplate.Execute(w, data)
	if err != nil {
		fmt.Fprintf(w, "Error executing template - %s", err)
	}
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

func reset() {
	log.Printf("Initializing the race")
	raceHasStarted = false
	results = make([]*Result, 0, 1024)
	auditLog = make([]Audit, 0, 1024)
	req, err := uploadFile("prizes.json")
	if err == nil {
		resp := httptest.NewRecorder()
		uploadPrizes(resp, req)
		if resp.Code != 301 {
			log.Println("Unable to load the default prizes.json file.")
		}
	} else {
		log.Printf("Unable to load the default prizes.json file - %v\n", err)
	}
}

func main() {
	reset()
	http.HandleFunc("raceresults/", handler)
	http.HandleFunc("raceresults/admin", handler)
	http.HandleFunc("raceresults/start", startHandler)
	http.HandleFunc("raceresults/linkBib", linkBib)
	http.HandleFunc("raceresults/assignBib", assignBib)
	http.HandleFunc("raceresults/addEntry", addEntry)
	http.HandleFunc("raceresults/download", download)
	http.HandleFunc("raceresults/uploadRacers", uploadRacers)
	http.HandleFunc("raceresults/uploadPrizes", uploadPrizes)
	//http.HandleFunc("raceresults/removeRacer", removeRacer)
	http.Handle("raceresults/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))
	//http.Handle("/", http.RedirectHandler("http://raceresults/", 307))
	err := http.ListenAndServe(":80", nil)
	if err != nil {
		log.Printf("Error starting http server! - %s\n", err)
		err = http.ListenAndServe(":8080", nil)
		if err != nil {
			log.Fatalf("Error starting http server! - %s\n", err)
			return
		}
	}
}

func listenForRacers() {
	ticker := time.NewTicker(time.Second * 10)
	var start time.Time
	for {
		select {
		case start = <-startRaceChan:
			ticker.Stop() // stop and "upgrade" the ticker for every second to track time
			ticker = time.NewTicker(time.Second)
			log.Printf("Race started @ %s\n", start.Format("3:04:05"))
		case now := <-ticker.C:
			if raceHasStarted {
				log.Println(HumanDuration(now.Sub(start)))
			} else {
				log.Println("Waiting to start the race")
			}
			// update the clock
		}
	}
}
