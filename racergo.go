package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/darkhelmet/env"
	"github.com/mzimmerman/sendgrid-go"
)

var config struct {
	webserverHostname string // the url to serve on - default localhost:8080
	sendgriduser      string // the Sendgrid user for e-mail integration
	sendgridpass      string // the Sendgrid password for e-mail integration
	emailField        string // the title of the Email field in the uploaded CSV - default Email
	emailFrom         string // the from address for the e-mail integration
	raceName          string // Name of the race, default Campus Life 5k Orchard Run
}

type templateRequest struct {
	name   string
	writer io.Writer
}

type TemplatePool struct {
	pool sync.Pool
}

func NewTemplatePool() *TemplatePool {
	return &TemplatePool{
		pool: sync.Pool{
			New: func() interface{} {
				return &bytes.Buffer{}
			},
		},
	}
}

func (tp *TemplatePool) Get() *bytes.Buffer {
	buf := tp.pool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func (tp *TemplatePool) Put(buf *bytes.Buffer) {
	tp.pool.Put(buf)
}

const SENDGRIDUSER = "API_USER"
const SENDGRIDPASS = "API_PASS"

var raceResultsTemplate *template.Template
var errorTemplate *template.Template
var serverHandlers chan struct{}

var linkBibChan = make(chan Bib)
var removeBibChan = make(chan Bib)
var addEntryChan = make(chan Entry)
var prizesChan = make(chan []Prize)
var downloadEntries = make(chan []*Entry)
var downloadPrizes = make(chan []Prize)
var requestTemplateData = make(chan templateRequest)
var modifyEntryChan = make(chan struct {
	entry Entry
	index Index
	nonce int
})
var resetRaceStateChan = make(chan struct{})

//var swapInNewChan = make(chan struct {
//	allEntries    []Entry
//	bibbedEntries map[Bib]*Entry
//})
var setOptionalFieldsChan = make(chan []string)
var getOptionalFieldsChan = make(chan []string)
var modifyEntriesChan = make(chan struct {
	e Entry
	i Index
})
var startRaceChan = make(chan time.Time)
var errorChan = make(chan error)

func init() {
	config.webserverHostname = env.StringDefault("RACERGOHOSTNAME", "localhost:8080")
	config.sendgriduser = env.StringDefault("RACERGOSENDGRIDUSER", SENDGRIDUSER)
	config.sendgridpass = env.StringDefault("RACERGOSENDGRIDPASS", SENDGRIDPASS)
	config.raceName = env.StringDefault("RACERGORACENAME", "Set RACERGORACENAME environment variable to change race name")
	config.emailField = env.StringDefault("RACERGOEMAILFIELD", "Email")
	config.emailFrom = env.StringDefault("RACERGOFROMEMAIL", "racergo@nonexistenthost.com")
	numHandlers := runtime.NumCPU()
	runtime.GOMAXPROCS(numHandlers)
	if numHandlers >= 2 {
		// want to leave one cpu not handling racer http requests so as to handle the processing of racers quickly
		numHandlers--
	}
	serverHandlers = make(chan struct{}, numHandlers)
	for x := 0; x < numHandlers; x++ {
		serverHandlers <- struct{}{} // fill the channel with valid goroutines
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
	go manageRaceState()
}

const NoBib Bib = -1

type Bib int32

func (b Bib) String() string {
	if b < 0 {
		return "--"
	}
	return strconv.Itoa(int(b))
}

type Place uint16

func (p Place) String() string {
	if p == 0 {
		return "--"
	}
	return strconv.Itoa(int(p))
}

type Index uint16

type Prize struct {
	Title    string
	LowAge   uint
	HighAge  uint
	Gender   string   // M = only males, F = only Females, O = Overall
	Amount   uint     // how many people win this prize?
	WinAgain bool     // if someone has already won another Prize, can they win this again?
	Winners  []*Entry `json:"-"`
}

type Entry struct {
	Bib          Bib
	Fname        string
	Lname        string
	Male         bool
	Age          uint
	Optional     []string
	Duration     HumanDuration
	TimeFinished time.Time
	Confirmed    bool
}

func (e Entry) Place(p int) int {
	return p + 1
}

func (e Entry) HasFinished() bool {
	return e.Duration > 0
}

type Audit struct {
	Duration HumanDuration
	Bib      Bib
	Remove   bool
}

type EntrySort []*Entry

func (es *EntrySort) Len() int {
	return len(*es)
}

func (es *EntrySort) Less(i, j int) bool {
	if (*es)[i].Duration == (*es)[j].Duration {
		return (*es)[i].Bib < (*es)[j].Bib
	}
	if !(*es)[i].HasFinished() { // this entry didn't finish, it doesn't beat anyone
		return false
	}
	if !(*es)[j].HasFinished() {
		return true
	}
	return (*es)[i].Duration < (*es)[j].Duration
}

func (es *EntrySort) Swap(i, j int) {
	(*es)[i], (*es)[j] = (*es)[j], (*es)[i]
}

type HumanDuration time.Duration

func (hd HumanDuration) String() string {
	if hd == 0 {
		return "--"
	}
	seconds := time.Duration(hd).Seconds()
	seconds -= float64(time.Duration(hd) / time.Minute * 60)
	return fmt.Sprintf("%#02d:%#02d:%05.2f", time.Duration(hd)/time.Hour, time.Duration(hd)/time.Minute%60, seconds)
}

func (hd HumanDuration) Clock() string {
	if hd == 0 {
		return "--"
	}
	return fmt.Sprintf("%#02d:%#02d:%02d", time.Duration(hd)/time.Hour, time.Duration(hd)/time.Minute%60, time.Duration(hd)/time.Second%60)
}

func ParseHumanDuration(val string) (HumanDuration, error) {
	var duration HumanDuration
	if val == "--" { // zero value case
		return duration, nil
	}
	str := strings.Split(val, ":")
	if len(str) < 3 {
		return duration, fmt.Errorf("%s is not a valid race duration, must have two semicolons", val)
	}
	secs := strings.Split(str[2], ".")
	if len(secs) < 2 {
		return duration, fmt.Errorf("%s does not contain a valid seconds time, must have a decimal place", val)
	}
	hours, err := strconv.Atoi(str[0])
	if err != nil {
		return duration, fmt.Errorf("Error parsing hours - %s - %v", str[0], err)
	}
	minutes, err := strconv.Atoi(str[1])
	if err != nil {
		return duration, fmt.Errorf("Error parsing minutes - %s - %v", str[1], err)
	}
	seconds, err := strconv.Atoi(secs[0])
	if err != nil {
		return duration, fmt.Errorf("Error parsing seconds - %s - %v", secs[0], err)
	}
	hundredths, err := strconv.Atoi(secs[1])
	if err != nil {
		return duration, fmt.Errorf("Error parsing hundredths - %s - %v", secs[1], err)
	}
	duration = HumanDuration((time.Hour * time.Duration(hours)) + (time.Minute * time.Duration(minutes)) + (time.Second * time.Duration(seconds)) + (time.Millisecond * 10 * time.Duration(hundredths)))
	return duration, nil
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	filename := fmt.Sprintf(config.webserverHostname+"-%s.csv", time.Now().In(time.Local).Format("2006-01-02"))
	w.Header().Set("Content-type", "application/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	entries := <-downloadEntries
	csvData := make([][]string, 0, len(entries)+1)
	headerRow := append([]string{"Fname", "Lname", "Age", "Gender", "Bib", "Overall Place", "Duration", "Time Finished"}, <-getOptionalFieldsChan...)
	csvData = append(csvData, headerRow)
	for place, entry := range entries {
		csvData = append(csvData, append([]string{entry.Fname, entry.Lname, strconv.Itoa(int(entry.Age)), gender(entry.Male), entry.Bib.String(), strconv.Itoa(place + 1), entry.Duration.String(), entry.TimeFinished.Format(time.ANSIC)}, entry.Optional...))
	}
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

func uploadPrizesHandler(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error getting Reader - %s", err)
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error getting Part - %s", err)
		return
	}
	jsonin := json.NewDecoder(part)
	newPrizes := make([]Prize, 0, 48)
	for {
		var prize Prize
		err = jsonin.Decode(&prize)
		if err == io.EOF {
			break // good, we processed them all!
		}
		if err != nil {
			showErrorForAdmin(w, r.Referer(), "Error fetching Prize Configurations - %s", err)
			return
		}
		newPrizes = append(newPrizes, prize)
	}
	prizesChan <- newPrizes
	http.Redirect(w, r, "/admin", 301)
}

func calculatePrizes(r *Entry, prizes []Prize) {
	// prizes are calculated from top-down, meaning all "faster" racers have already been placed
	found := false
	for p := range prizes {
		switch {
		case found && !prizes[p].WinAgain:
			fallthrough
		case r.Age < prizes[p].LowAge:
			fallthrough
		case r.Age > prizes[p].HighAge:
			fallthrough
		case r.Male && (prizes[p].Gender == "F"):
			fallthrough
		case !r.Male && (prizes[p].Gender == "M"):
			fallthrough
		case len(prizes[p].Winners) == int(prizes[p].Amount):
			continue // do not qualify any of these conditions
		}
		found = true
		prizes[p].Winners = append(prizes[p].Winners, r)
		log.Printf("Placing #%d in prize %s, place %d", r.Bib, prizes[p].Title, len(prizes[p].Winners))
	}
}

func uploadRacersHandler(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error getting Reader - %s", err)
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error getting Part - %s", err)
		return
	}
	csvIn := csv.NewReader(part)
	rawEntries, err := csvIn.ReadAll()
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error Reading CSV file - %s", err)
		return
	}
	if len(rawEntries) <= 1 {
		showErrorForAdmin(w, r.Referer(), "Either blank file or only supplied the header row")
		return
	}

	// make the new in-memory data stores and unlink all previous relationships
	newBibbedEntries := make(map[Bib]*Entry)
	newAllEntries := make([]*Entry, 0, 1024)
	// initialize the optionalEntryFields for use when we export/display the data
	newOptionalEntryFields := make([]string, 0)
	mandatoryFields := map[string]struct{}{
		"Fname":  struct{}{},
		"Lname":  struct{}{},
		"Age":    struct{}{},
		"Gender": struct{}{},
	}
	for col := range rawEntries[0] {
		switch rawEntries[0][col] {
		case "Fname":
			fallthrough
		case "Lname":
			fallthrough
		case "Age":
			fallthrough
		case "Gender":
			delete(mandatoryFields, rawEntries[0][col])
		case "Bib": // Bib is a special case but is not mandatory
		default:
			newOptionalEntryFields = append(newOptionalEntryFields, rawEntries[0][col])
		}
	}
	if len(mandatoryFields) > 0 {
		showErrorForAdmin(w, r.Referer(), "CSV file missing the following fields - %s", mandatoryFields)
		return
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
				tmpBib, err := strconv.Atoi(rawEntries[row][col])
				if err != nil {
					entry.Bib = -1
				} else {
					entry.Bib = Bib(tmpBib)
				}
			default:
				entry.Optional = append(entry.Optional, rawEntries[row][col])
			}
		}
		if _, ok := newBibbedEntries[entry.Bib]; ok {
			showErrorForAdmin(w, r.Referer(), "Duplicate bib #%d detected in uploaded CSV file.  Import failed.", entry.Bib)
			return
		}
		if entry.Bib >= 0 {
			newBibbedEntries[entry.Bib] = entry
		}
		newAllEntries = append(newAllEntries, entry)
	}
	setOptionalFieldsChan <- newOptionalEntryFields
	err = <-errorChan
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "%v", err)
		return
	}
	for _, e := range newAllEntries {
		addEntryChan <- *e
		err = <-errorChan
		if err != nil {
			showErrorForAdmin(w, r.Referer(), "%v - partial import", err)
			return
		}
	}

	http.Redirect(w, r, "/admin", 301)
}

//func auditPostHandler(w http.ResponseWriter, r *http.Request) {
//	mutex.Lock()
//	defer mutex.Unlock()
//	if !auditClean {
//		showErrorForAdmin(w, r.Referer(), "Data modified since audit record pulled, no updates made.  Try again.")
//	}
//	auditClean = false
//	// wipe the in-memory data stores
//	newBibbedEntries := make(map[Bib]*Entry)
//	newAllEntries := make([]*Entry, 0, 1024)
//	r.ParseForm()
//	// load the new entries
//	for row := 0; ; row++ {
//		rowString := strconv.Itoa(row) + "."
//		entry := &Entry{Bib: -1}
//		entry.Optional = make([]string, 0)
//		entry.Fname = r.FormValue(rowString + "Fname")
//		entry.Lname = r.FormValue(rowString + "Lname")
//		tmpAge, _ := strconv.Atoi(r.FormValue(rowString + "Age"))
//		entry.Age = uint(tmpAge)
//		entry.Male = (r.FormValue(rowString+"Male") == "M")
//		tmpBib, err := strconv.Atoi(r.FormValue(rowString + "Bib"))
//		if err != nil {
//			entry.Bib = -1
//		} else {
//			entry.Bib = Bib(tmpBib)
//		}
//		if entry.Fname == "" && entry.Lname == "" && entry.Age == 0 && entry.Bib == -1 {
//			break // this one has all default/empty values, must be the end of the records found
//		}
//		duration, err := ParseHumanDuration(r.FormValue(rowString + "Time"))
//		if err != nil {
//			fmt.Printf("Unable to parse duration - %v\n", err)
//		} else {
//			entry.Duration = duration
//			// TODO: entry.TimeFinished = raceStart
//			entry.Confirmed = true
//		}
//		for _, opt := range optionalEntryFields {
//			entry.Optional = append(entry.Optional, r.FormValue(rowString+opt))
//		}
//		if entry.Bib >= 0 {
//			if _, ok := newBibbedEntries[entry.Bib]; ok {
//				showErrorForAdmin(w, r.Referer(), fmt.Sprintf("Cannot assign bib #%d to multiple runners.", entry.Bib))
//				return
//			}
//			newBibbedEntries[entry.Bib] = entry
//		}
//		newAllEntries = append(newAllEntries, entry)
//	}
//	// no issues/errors, load the data
//	bibbedEntries = newBibbedEntries
//	allEntries = newAllEntries
//	// now rebuild results
//	sort.Sort((*EntrySort)(&allEntries))
//	results = results[:0]
//	var place Place
//	for x, e := range allEntries {
//		if e.HasFinished() {
//			place++
//			e.Place = place
//			results = append(results, &allEntries[x])
//		}
//	}
//	for _, prize := range prizes {
//		prize.Winners = make([]*Entry, 0)
//	}
//	recomputeAllPrizes()
//	http.Redirect(w, r, "/audit", 301)
//}

func startHandler(w http.ResponseWriter, r *http.Request) {
	startRaceChan <- time.Now()
	http.Redirect(w, r, "/admin", 301)
}

func linkBibHandler(w http.ResponseWriter, r *http.Request) {
	removeBib := r.FormValue("remove") == "true"
	tmpBib, err := strconv.Atoi(r.FormValue("bib"))
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error %s getting bib number", err)
		return
	}
	if tmpBib < 0 {
		showErrorForAdmin(w, r.Referer(), "Cannot assign a negative bib number of %d", tmpBib)
		return
	}
	bib := Bib(tmpBib)
	if removeBib {
		removeBibChan <- bib
	} else {
		linkBibChan <- bib
	}
	err = <-errorChan
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "%v", err)
		return
	}
	http.Redirect(w, r, "/admin", 301)
	//deltaT := HumanDuration(time.Since(raceStart))
	//mutex.Lock()
	//defer mutex.Unlock()
	//auditClean = false
	//auditLog = append(auditLog, Audit{Duration: deltaT, Bib: bib, Remove: removeBib})
	//entry, ok := bibbedEntries[bib]
	//if !ok {
	//	showErrorForAdmin(w, r.Referer(), "Bib number %d was not assigned to anyone.", bib)
	//	return
	//}
	//if removeBib {
	//	if entry.Duration == 0 {
	//		// entry already removed, act successful
	//		http.Redirect(w, r, "/admin", 301)
	//		return
	//	}
	//	index := int(entry.Place) - 1
	//	log.Printf("Bib = %d, index = %d, len(results) = %d", bib, index, len(results))
	//	entry.Duration = 0
	//	entry.TimeFinished = time.Time{}
	//	entry.Confirmed = false
	//	results = append(results[:index], results[index+1:]...)
	//	for x := index; x < len(results); x++ {
	//		allEntries[results[x]].Place--
	//	}
	//	http.Redirect(w, r, "/admin", 301)
	//	return
	//}
	//if entry.Duration == 0 {
	//	if entry.Confirmed {
	//		showErrorForAdmin(w, r.Referer(), "Bib number %d already confirmed for place #%d", bib, entry.Place)
	//		return
	//	}
	//	entry.Confirmed = true
	//	http.Redirect(w, r, "/admin", 301)
	//	if emailIndex == -1 { // no e-mail address was found on data load, just return
	//		return
	//	}
	//	emailAddr := entry.Optional[emailIndex]
	//	_, err = mail.ParseAddress(emailAddr)
	//	if err != nil {
	//		log.Printf("Error parsing e-mail address of %s\n", emailAddr)
	//		return
	//	}
	//}
	//entry.Duration = deltaT
	//entry.Place = Place(len(results) + 1)
	//entry.Confirmed = false
	//results = append(results, entry.Index)
	//http.Redirect(w, r, "/admin", 301)
}

func sendEmailResponse(e Entry, hd HumanDuration, emailIndex int) {
	if emailIndex == -1 { // no e-mail address was found on data load, just return
		return
	}
	emailAddr := e.Optional[emailIndex]
	_, err := mail.ParseAddress(emailAddr)
	if err != nil {
		log.Printf("Error parsing e-mail address of %s\n", emailAddr)
		return
	}
	m := sendgrid.NewMail()
	client := sendgrid.NewSendGridClient(config.sendgriduser, config.sendgridpass)
	m.AddTo(fmt.Sprintf("%s %s <%s>", e.Fname, e.Lname, emailAddr))
	m.SetSubject(fmt.Sprintf("%s Results", config.raceName))
	m.SetText(fmt.Sprintf("Congratulations %s %s!  You finished the %s in %s!", e.Fname, e.Lname, config.raceName, hd))
	m.SetFrom(config.emailFrom)
	backoff := time.Second
	for {
		err := client.Send(m)
		if err == nil {
			log.Printf("Success sending %#v", m)
			return
		}
		backoff = backoff * 2
		log.Printf("Error sending mail to %s - %v, trying again in %s", emailAddr, err, backoff)
		time.Sleep(backoff)
	}
}

func showErrorForAdmin(w http.ResponseWriter, referrer string, message string, args ...interface{}) {
	w.WriteHeader(409) // conflict header, most likely due to old information in the client
	msg := fmt.Sprintf(message, args...)
	log.Println(msg)
	if errorTemplate == nil {
		fmt.Fprintf(w, msg)
		return
	}
	err := errorTemplate.Execute(w, map[string]interface{}{"Message": msg, "Referrer": referrer})
	if err != nil {
		fmt.Fprintf(w, "Error executing template - %s", err)
	}
}

func recomputeAllPrizes(prizes []Prize, allEntries []*Entry) {
	for p := range prizes {
		prizes[p].Winners = prizes[p].Winners[:0]
	}
	for _, v := range allEntries {
		if !v.Confirmed {
			break // all done
		}
		calculatePrizes(v, prizes)
	}
}

//func assignBibHandler(w http.ResponseWriter, r *http.Request) {
//	id, err := strconv.Atoi(r.FormValue("id"))
//	if err != nil {
//		showErrorForAdmin(w, r.Referer(), r.Referer(), "Error %s getting next", err)
//		return
//	}
//	tmpBib, err := strconv.Atoi(r.FormValue("bib"))
//	if tmpBib < 0 || err != nil {
//		showErrorForAdmin(w, r.Referer(), "Could not get a valid bib number from %s", r.FormValue("bib"))
//		return
//	}
//	bib := Bib(tmpBib)
//	mutex.Lock()
//	defer mutex.Unlock()

//	if len(allEntries) > id {
//		entry := allEntries[id]
//		if _, ok := bibbedEntries[bib]; ok {
//			showErrorForAdmin(w, r.Referer(), "Bib # %d already assigned to %s %s!", bib, bibbedEntries[bib].Fname, bibbedEntries[bib].Lname)
//			return
//		}
//		entry.Bib = bib
//		log.Printf("Set bib for %s %s to %d", entry.Fname, entry.Lname, bib)
//		bibbedEntries[entry.Bib] = entry
//	} else {
//		showErrorForAdmin(w, r.Referer(), "Id %d was not assigned to anyone.", id)
//		return
//	}
//	http.Redirect(w, r, "/admin", 301)
//	return
//}

func addEntryHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	entry := Entry{}
	age, err := strconv.Atoi(r.FormValue("Age"))
	if age < 0 {
		showErrorForAdmin(w, r.Referer(), "Not a valid age, must be >= 0")
		return
	}
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error %s getting Age", err)
		return
	}
	entry.Age = uint(age)
	tmpBib, err := strconv.Atoi(r.FormValue("Bib"))
	entry.Bib = Bib(tmpBib)
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error %s getting Bib", err)
		return
	}
	entry.Fname = r.FormValue("Fname")
	entry.Lname = r.FormValue("Lname")
	entry.Male = r.FormValue("Male") == "true"
	entry.Optional = make([]string, 0)
	optionalEntryFields := <-getOptionalFieldsChan
	for _, s := range optionalEntryFields {
		entry.Optional = append(entry.Optional, r.FormValue(s))
	}
	addEntryChan <- entry
	err = <-errorChan
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "%v", err)
		return
	}
	http.Redirect(w, r, "/admin", 301)
	return
}

func handler(w http.ResponseWriter, r *http.Request) {
	<-serverHandlers // wait until a goroutine to handle http requests is free
	defer func() {
		serverHandlers <- struct{}{} // wait for handler to finish, then put it back in the queue so another handler can work
	}()
	requestTemplateData <- templateRequest{
		name:   strings.Trim(r.URL.Path, "/"),
		writer: w,
	}
	err := <-errorChan
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error executing template - %v", err)
		log.Printf("Error executing template - %v", err)
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

func recordTimeForBib(bib Bib, bibbedEntries map[Bib]*Entry, allEntries *[]*Entry, emailFieldIndex int, raceStart time.Time, prizes []Prize) error {
	if entry, ok := bibbedEntries[bib]; ok {
		if !entry.Confirmed {
			if entry.HasFinished() {
				entry.Confirmed = true
				log.Printf("Bib #%d confirmed with duration - %s", bib, entry.Duration)
				// TODO: Verify that every entry before them is *also* confirmed, otherwise their finishing place could be wrong
				recomputeAllPrizes(prizes, *allEntries)
				go sendEmailResponse(*entry, entry.Duration, emailFieldIndex)
				return nil
			}
			now := time.Now()
			entry.Duration = HumanDuration(now.Sub(raceStart))
			entry.TimeFinished = now
			sorted := EntrySort(*allEntries)
			sort.Sort(&sorted)
			log.Printf("Bib #%d linked with duration - %s", bib, entry.Duration)
			return nil
		}
		return fmt.Errorf("Bib #%d already confirmed!", bib)
	}
	return fmt.Errorf("Bib %d not found", bib)
}

func removeTimeForBib(bib Bib, bibbedEntries map[Bib]*Entry, allEntries *[]*Entry) error {
	if entry, ok := bibbedEntries[bib]; ok {
		if !entry.Confirmed {
			if entry.HasFinished() {
				entry.Duration = 0
				entry.TimeFinished = time.Time{}
				sorted := EntrySort(*allEntries)
				sort.Sort(&sorted)
				log.Printf("Removed time for racer #%d", bib)
				return nil
			}
			return fmt.Errorf("Cannot remove time for bib #%d, time is already removed.", bib)
		}
		return fmt.Errorf("Bib #%d already confirmed!", bib)
	}
	return fmt.Errorf("Bib %d not found", bib)
}

func addEntry(entry Entry, bibbedEntries map[Bib]*Entry, allEntries *[]*Entry, raceStart time.Time) error {
	if entry.Fname == "" {
		return fmt.Errorf("Entry missing first name!")
	}
	if entry.Lname == "" {
		return fmt.Errorf("Entry missing last name!")
	}
	if raceStart.IsZero() {
		entry.Confirmed = false
		entry.Duration = 0
		entry.Confirmed = false
	}
	if entry.Bib >= 0 {
		if _, ok := bibbedEntries[entry.Bib]; ok {
			return fmt.Errorf("Entry already exists for bib #%d", entry.Bib)
		}
		*allEntries = append(*allEntries, &entry)
		bibbedEntries[entry.Bib] = &entry
	} else {
		if !raceStart.IsZero() {
			return fmt.Errorf("Entry does not contain a bib # and the race has started!")
		}
		*allEntries = append(*allEntries, &entry)
	}
	log.Printf("Added Entry - %#v\n", entry)
	return nil
}

type RecentRacer struct {
	Entry
	Place Place
}

func generateTemplate(req templateRequest, allEntries []*Entry, raceStart time.Time, auditLog []Audit, optionalEntryFields []string, prizes []Prize, modifyNonce int, tmplPool *TemplatePool) error {
	data := map[string]interface{}{"Entries": allEntries}
	switch req.name {
	default:
		req.name = "default"
	case "audit":
		data["Audit"] = auditLog
		data["Fields"] = optionalEntryFields
		data["Nonce"] = modifyNonce
		fallthrough
	case "admin":
		data["Admin"] = true
		fallthrough
	case "results":
		numRecent := 10
		recentRacers := make([]RecentRacer, 0, numRecent)
		for i := len(allEntries) - 1; i >= 0; i-- {
			if allEntries[i].HasFinished() {
				recentRacers = append(recentRacers, RecentRacer{
					Entry: *allEntries[i],
					Place: Place(i + 1),
				})
			}
			if len(recentRacers) == numRecent { // list no more than numRecent most recent
				break
			}
		}
		data["RecentRacers"] = recentRacers
	}
	if !raceStart.IsZero() {
		diff := time.Since(raceStart)
		data["Start"] = raceStart.Format("3:04:05")
		data["Time"] = HumanDuration(diff).Clock()
		data["Seconds"] = fmt.Sprintf("%.0f", diff.Seconds())
		data["NextUpdate"] = diff / time.Millisecond % 1000
	}
	data["Prizes"] = prizes
	buf := tmplPool.Get()
	defer tmplPool.Put(buf)
	raceResultsTemplate, _ = template.ParseFiles("raceResults.template")
	err := raceResultsTemplate.ExecuteTemplate(buf, req.name, data)
	if err == nil {
		// no errors processing the template, copy the generated data
		io.Copy(req.writer, buf)
	}
	return err
}

func modifyEntry(entry Entry, index Index, bibbedEntries map[Bib]*Entry, allEntries *[]*Entry) error {
	return nil
}

func manageRaceState() {
	src := make(chan time.Time)
	go listenForRacers(src)

	tmplPool := NewTemplatePool()
	var raceStart time.Time
	var optionalEntryFields []string
	bibbedEntries := make(map[Bib]*Entry) // map of Bib #s pointing to bibbed entries only, for link bib lookup
	allEntries := make([]*Entry, 0, 1024) // a sorted slice of all Entries, bibbed and unbibbed, w/ result or not, sorted by Place (first to last)
	auditLog := make([]Audit, 0, 1024)    // A writeonly location to record the actions/events of the race
	prizes := make([]Prize, 0, 48)
	modifyNonce := 0
	optionalEmailIndex := -1 // initialize it to an invalid value
	log.Printf("Initialized the race")
	for {
		select {
		case <-resetRaceStateChan:
			allEntries = allEntries[:0]
			for _, p := range prizes {
				p.Winners = make([]*Entry, 0, p.Amount)
			}
			modifyNonce = 0
			optionalEntryFields = nil
			bibbedEntries = make(map[Bib]*Entry)
			raceStart = time.Time{}
		case templateReq := <-requestTemplateData:
			if templateReq.name == "audit" {
				modifyNonce = rand.Int()
			}
			errorChan <- generateTemplate(templateReq, allEntries, raceStart, auditLog, optionalEntryFields, prizes, modifyNonce, tmplPool)
		case tmp := <-setOptionalFieldsChan:
			modifyNonce = 0
			if optionalEntryFields == nil {
				optionalEntryFields = tmp
				errorChan <- nil
			} else {
				errorChan <- fmt.Errorf("Optional entry fields are already set")
			}
		case getOptionalFieldsChan <- optionalEntryFields:
		case prizes = <-prizesChan:
			recomputeAllPrizes(prizes, allEntries)
		case raceStart = <-startRaceChan:
			src <- raceStart
		case mod := <-modifyEntryChan:
			if mod.nonce != modifyNonce {
				errorChan <- fmt.Errorf("Error updating entry - audit record was out of date, try your change again")
			} else {
				errorChan <- modifyEntry(mod.entry, mod.index, bibbedEntries, &allEntries)
			}
			modifyNonce = 0
		case entry := <-addEntryChan:
			errorChan <- addEntry(entry, bibbedEntries, &allEntries, raceStart)
		case bib := <-linkBibChan:
			modifyNonce = 0
			if raceStart.IsZero() {
				errorChan <- fmt.Errorf("Race has not started yet, cannot link a bib")
			} else {
				errorChan <- recordTimeForBib(bib, bibbedEntries, &allEntries, optionalEmailIndex, raceStart, prizes)
			}
		case bib := <-removeBibChan:
			modifyNonce = 0
			errorChan <- removeTimeForBib(bib, bibbedEntries, &allEntries)
		case downloadEntries <- allEntries:
		case downloadPrizes <- prizes:
		}
	}
}

func init() {
	http.HandleFunc(config.webserverHostname+"/", handler)
	http.HandleFunc(config.webserverHostname+"/admin", handler)
	http.HandleFunc(config.webserverHostname+"/start", startHandler)
	http.HandleFunc(config.webserverHostname+"/linkBib", linkBibHandler)
	//http.HandleFunc(config.webserverHostname+"/assignBib", assignBibHandler)
	http.HandleFunc(config.webserverHostname+"/addEntry", addEntryHandler)
	http.HandleFunc(config.webserverHostname+"/download", downloadHandler)
	http.HandleFunc(config.webserverHostname+"/uploadRacers", uploadRacersHandler)
	http.HandleFunc(config.webserverHostname+"/uploadPrizes", uploadPrizesHandler)
	//http.HandleFunc(config.webserverHostname+"/auditPost", auditPostHandler)
	http.Handle(config.webserverHostname+"/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))
	http.Handle(config.webserverHostname+"/fonts/", http.StripPrefix("/fonts/", http.FileServer(http.Dir("fonts/"))))
	http.Handle("/", http.RedirectHandler("http://"+config.webserverHostname+"/", 307))
	req, err := uploadFile("prizes.json")
	if err == nil {
		resp := httptest.NewRecorder()
		uploadPrizesHandler(resp, req)
		if resp.Code != 301 {
			log.Println("Unable to load the default prizes.json file.")
		}
	} else {
		log.Printf("Unable to load the default prizes.json file - %v\n", err)
	}
}

func main() {
	log.Printf("Starting http server")
	listener, err := net.Listen("tcp", ":80")
	if err != nil {
		log.Printf("Error listening on port 80, trying 8080 instead! - %s\n", err)
		listener, err = net.Listen("tcp4", ":8080")
		if err != nil {
			log.Fatalf("Error listening on port 8080! - %s\n", err)
			return
		}
	}
	port := strings.Split(listener.Addr().String(), ":")
	portNum := port[len(port)-1]
	log.Printf("Server listening on port %s\n", portNum)
	log.Printf("Basic - http://localhost:%s", portNum)
	log.Printf("Admin - http://localhost:%s/admin", portNum)
	log.Printf("Audit - http://localhost:%s/audit", portNum)
	log.Printf("Large Screen Live Results - http://localhost:%s/results", portNum)
	err = http.Serve(listener, nil)
	if err != nil {
		log.Fatalf("Error starting http server! - %s\n", err)
	}
}

func listenForRacers(raceStarter chan time.Time) {
	ticker := time.NewTicker(time.Second * 10)
	var start time.Time
	raceHasStarted := false
	for {
		select {
		case start = <-raceStarter:
			ticker.Stop() // stop and "upgrade" the ticker for every second to track time
			ticker = time.NewTicker(time.Second)
			log.Printf("Race started @ %s\n", start.Format("3:04:05"))
			raceHasStarted = true
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
