package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
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
	sendgrid "github.com/mzimmerman/sendgrid-go"
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
	name    string
	writer  io.Writer
	request *http.Request
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

var headers = []string{"Fname", "Lname", "Age", "Gender", "Bib", "Overall Place", "Duration", "Time Finished", "Confirmed"}
var serverHandlers chan struct{}
var raceResultsTemplate *template.Template
var raceResultsFuncMap template.FuncMap
var errorTemplate *template.Template
var tmplPool *TemplatePool

func init() {
	tmplPool = NewTemplatePool()
	config.webserverHostname = env.StringDefault("RACERGOHOSTNAME", "localhost:8080")
	config.sendgriduser = env.StringDefault("RACERGOSENDGRIDUSER", SENDGRIDUSER)
	config.sendgridpass = env.StringDefault("RACERGOSENDGRIDPASS", SENDGRIDPASS)
	config.raceName = env.StringDefault("RACERGORACENAME", "Set RACERGORACENAME environment variable to change race name")
	config.emailField = env.StringDefault("RACERGOEMAILFIELD", "Email")
	config.emailFrom = env.StringDefault("RACERGOFROMEMAIL", "racergo@nonexistenthost.com")
	numHandlers := runtime.NumCPU()
	if numHandlers >= 2 {
		// want to leave one cpu not handling racer http requests so as to handle the processing of racers quickly
		numHandlers--
	}
	serverHandlers = make(chan struct{}, numHandlers)
	for x := 0; x < numHandlers; x++ {
		serverHandlers <- struct{}{} // fill the channel with valid goroutines
	}
	var err error
	raceResultsFuncMap = template.FuncMap{"textequal": func(a, b string) bool {
		return a == b
	}}
	raceResultsTemplate, err = template.New("template").Funcs(raceResultsFuncMap).ParseFiles("raceResults.template")
	if err != nil {
		log.Fatalf("Error parsing template - %s\n", err)
		return
	}
	errorTemplate, err = template.ParseFiles("error.template")
	if err != nil {
		log.Fatalf("Error parsing template! - %s\n", err)
		return
	}
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

// used in html templates
func (e Entry) Place(p int) int {
	return p + 1
}

func (e Entry) Nonce() string {
	s := md5.Sum([]byte(fmt.Sprintf("%d%d%t%d%s%s%t%s", e.Age, e.Bib, e.Confirmed, e.Duration, e.Fname, e.Lname, e.Male, e.Optional)))
	return base64.StdEncoding.EncodeToString(s[:])
}

func (e Entry) HasFinished() bool {
	return e.Duration > 0
}

func (e Entry) TimeFinishedString() string {
	if e.HasFinished() {
		return e.TimeFinished.Format(time.ANSIC)
	}
	return "--"
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
	if val == "--" || val == "" { // zero value case
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

func downloadHandler(w http.ResponseWriter, r *http.Request, race *Race) {
	filename := fmt.Sprintf(config.webserverHostname+"-%s.csv", time.Now().In(time.Local).Format("2006-01-02"))
	w.Header().Set("Content-type", "application/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	writer := csv.NewWriter(w)
	race.WriteCSV(writer)
	writer.Flush()
}

func gender(male bool) string {
	if male {
		return "M"
	}
	return "F"
}

func uploadPrizesHandler(w http.ResponseWriter, r *http.Request, race *Race) {
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
	race.SetPrizes(newPrizes)
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

func uploadRacersHandler(w http.ResponseWriter, r *http.Request, race *Race) {
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
	// accept a file with only time attached to a row in the "Time Finished" field
	if len(rawEntries) >= 2 {
		if len(rawEntries[1]) >= 7 {
			found := true
			for v := 0; v < 6; v++ {
				if rawEntries[1][v] != "" {
					found = false
					break
				}
			}
			if found {
				startTime, err := time.ParseInLocation(time.ANSIC, rawEntries[1][7], time.Local)
				if err == nil {
					err = race.Start(&startTime)
					if err != nil {
						showErrorForAdmin(w, r.Referer(), "Error starting race - %s", err)
						return
					}
					rawEntries = append(rawEntries[:1], rawEntries[2:]...) // delete the time header and pull in the rest of the file
				}
			}
		}
	}

	// make the new in-memory data stores and unlink all previous relationships
	newBibbedEntries := make(map[Bib]Entry)
	newAllEntries := make([]Entry, 0, 1024)
	// initialize the optionalEntryFields for use when we export/display the data
	newOptionalEntryFields := make([]string, 0)
	mandatoryFields := map[string]struct{}{
		"Fname":  struct{}{},
		"Lname":  struct{}{},
		"Age":    struct{}{},
		"Gender": struct{}{},
	}
	reservedFields := map[string]struct{}{
		"Fname":         struct{}{},
		"Lname":         struct{}{},
		"Age":           struct{}{},
		"Gender":        struct{}{},
		"Bib":           struct{}{},
		"Overall Place": struct{}{},
		"Duration":      struct{}{},
		"Time Finished": struct{}{},
		"Confirmed":     struct{}{},
	}
	for col := range rawEntries[0] {
		if _, ok := mandatoryFields[rawEntries[0][col]]; ok {
			delete(mandatoryFields, rawEntries[0][col])
			continue
		}
		if _, ok := reservedFields[rawEntries[0][col]]; !ok {
			// optional field since it's not in the reserved list
			newOptionalEntryFields = append(newOptionalEntryFields, rawEntries[0][col])
		}
	}
	if len(mandatoryFields) > 0 {
		showErrorForAdmin(w, r.Referer(), "CSV file missing the following fields - %s", mandatoryFields)
		return
	}
	// load the data
	for row := 1; row < len(rawEntries); row++ {
		entry := Entry{Bib: -1}
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
			case "Overall Place":
				// ignore since this will be calculated on sort
			case "Duration":
				entry.Duration, err = ParseHumanDuration(rawEntries[row][col])
				if err != nil {
					showErrorForAdmin(w, r.Referer(), "Error parsing duration %s - %v.  Import failed.", rawEntries[row][col], err)
				}
			case "Time Finished":
			// ignore since Time Finished is based on Duration and race start time
			case "Confirmed":
				entry.Confirmed = rawEntries[row][col] == "true"
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
	err = race.SetOptionalFields(newOptionalEntryFields)
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "%v", err)
		return
	}
	for _, e := range newAllEntries {
		err = race.AddEntry(e)
		if err != nil {
			showErrorForAdmin(w, r.Referer(), "%v - partial import on record - %#v", err, e)
			return
		}
	}
	http.Redirect(w, r, "/admin", 301)
}

func startHandler(w http.ResponseWriter, r *http.Request, race *Race) {
	err := race.Start(nil)
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error starting race - %s", err)
		return
	}
	http.Redirect(w, r, "/admin", 301)
}

func linkBibHandler(w http.ResponseWriter, r *http.Request, race *Race) {
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
		err = race.RemoveTimeForBib(bib)
	} else {
		err = race.RecordTimeForBib(bib)
	}
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "%v", err)
		return
	}
	if r.FormValue("scanned") == "true" {
		err = race.RecordTimeForBib(bib)
		if err != nil {
			showErrorForAdmin(w, r.Referer(), "%v", err)
			return
		}
		// using code 409 so it doesn't cache the response
		http.Error(w, "Bib found and linked successfully", 409)
		return
	}
	http.Redirect(w, r, r.Referer(), 301)
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

func parseEntry(r *http.Request, race *Race) (Entry, error) {
	r.ParseForm()
	entry := Entry{}
	age, err := strconv.Atoi(r.FormValue("Age"))
	if age < 0 {
		return entry, fmt.Errorf("%s is not a valid age, must be >= 0", r.FormValue("Age"))
	}
	if err != nil {
		return entry, fmt.Errorf("Error %v getting Age", err)
	}
	entry.Age = uint(age)
	tmpBib, err := strconv.Atoi(r.FormValue("Bib"))
	entry.Bib = Bib(tmpBib)
	if err != nil {
		return entry, fmt.Errorf("Error %v getting Bib", err)
	}
	entry.Fname = r.FormValue("Fname")
	entry.Lname = r.FormValue("Lname")
	entry.Male = r.FormValue("Male") == "M"
	if !entry.Male && !(r.FormValue("Male") == "F") {
		return entry, fmt.Errorf("You didn't choose a gender!")
	}
	entry.Optional = make([]string, 0)
	entry.Duration, err = ParseHumanDuration(r.FormValue("Duration"))
	if err != nil {
		return entry, fmt.Errorf("Error %v getting duration from %s", err, r.FormValue("Duration"))
	}
	entry.Confirmed = r.FormValue("Confirmed") == "true"
	optionalEntryFields := race.GetOptionalFields()
	for _, s := range optionalEntryFields {
		entry.Optional = append(entry.Optional, r.FormValue(s))
	}
	return entry, nil
}

func addEntryHandler(w http.ResponseWriter, r *http.Request, race *Race) {
	entry, err := parseEntry(r, race)
	page := "dayof"
	if strings.Contains(r.Referer(), "/admin") {
		page = "admin"
	}
	referTo := fmt.Sprintf("http://%s/%s?%s", config.webserverHostname, page, r.Form.Encode())
	if err != nil {
		showErrorForAdmin(w, referTo, "%v", err)
		return
	}
	err = race.AddEntry(entry)
	if err != nil {
		showErrorForAdmin(w, referTo, "%v", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s", page), 301)
	return
}

func handler(w http.ResponseWriter, r *http.Request, race *Race) {
	<-serverHandlers // wait until a goroutine to handle http requests is free
	defer func() {
		serverHandlers <- struct{}{} // wait for handler to finish, then put it back in the queue so another handler can work
	}()
	err := race.GenerateTemplate(templateRequest{
		name:    strings.Trim(r.URL.Path, "/"),
		writer:  w,
		request: r,
	})
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

func (race *Race) RecordTimeForBib(bib Bib) error {
	race.Lock()
	defer race.Unlock()
	if race.started.IsZero() {
		return fmt.Errorf("Race has not started yet, cannot link a bib")
	}
	if entry, ok := race.bibbedEntries[bib]; ok {
		if !entry.Confirmed {
			now := race.GetTime()
			duration := HumanDuration(now.Sub(race.started))
			if entry.HasFinished() {
				entry.Confirmed = true
				log.Printf("Bib #%d confirmed with duration - %s", bib, entry.Duration)
				race.auditLog = append(race.auditLog, Audit{
					Duration: duration,
					Bib:      bib,
					Remove:   false,
				})
				// TODO: Verify that every entry before them is *also* confirmed, otherwise their finishing place could be wrong
				recomputeAllPrizes(race.prizes, race.allEntries)
				go sendEmailResponse(*entry, entry.Duration, race.optionalEmailIndex)
				return nil
			}
			entry.Duration = duration
			entry.TimeFinished = now
			race.lockedSortEntries()
			log.Printf("Bib #%d linked with duration - %s", bib, entry.Duration)
			race.auditLog = append(race.auditLog, Audit{
				Duration: entry.Duration,
				Bib:      bib,
				Remove:   false,
			})
			return nil
		}
		return fmt.Errorf("Bib #%d already confirmed!", bib)
	}
	return fmt.Errorf("Bib %d not found", bib)
}

func (race *Race) RemoveTimeForBib(bib Bib) error {
	race.Lock()
	defer race.Unlock()
	if entry, ok := race.bibbedEntries[bib]; ok {
		if !entry.Confirmed {
			if entry.HasFinished() {
				entry.Duration = 0
				entry.TimeFinished = time.Time{}
				race.lockedSortEntries()
				log.Printf("Removed time for racer #%d", bib)
				race.auditLog = append(race.auditLog, Audit{
					Duration: HumanDuration(race.GetTime().Sub(race.started)),
					Bib:      bib,
					Remove:   true,
				})
				return nil
			}
			return fmt.Errorf("Cannot remove time for bib #%d, time is already removed.", bib)
		}
		return fmt.Errorf("Bib #%d already confirmed!", bib)
	}
	return fmt.Errorf("Bib %d not found", bib)
}

func (race *Race) normalizeEntry(entry *Entry) error {
	if entry.Fname == "" {
		return fmt.Errorf("Entry missing first name!")
	}
	if entry.Lname == "" {
		return fmt.Errorf("Entry missing last name!")
	}
	if race.started.IsZero() {
		entry.Confirmed = false
		entry.Duration = 0
	} else {
		// entry.Confirmed status not modified
		entry.TimeFinished = race.started.Add(time.Duration(entry.Duration))
	}
	if entry.Duration == 0 {
		entry.Confirmed = false
	}
	return nil
}

func (race *Race) AddEntry(entry Entry) error {
	race.Lock()
	defer race.Unlock()
	err := race.normalizeEntry(&entry)
	if err != nil {
		return err
	}
	if entry.Bib >= 0 {
		if _, ok := race.bibbedEntries[entry.Bib]; ok {
			return fmt.Errorf("Entry already exists for bib #%d", entry.Bib)
		}
		race.allEntries = append(race.allEntries, &entry)
		race.bibbedEntries[entry.Bib] = &entry
	} else {
		if !race.started.IsZero() {
			return fmt.Errorf("Entry does not contain a bib # and the race has started!")
		}
		race.allEntries = append(race.allEntries, &entry)
	}
	log.Printf("Added Entry - %#v\n", entry)
	race.lockedSortEntries()
	recomputeAllPrizes(race.prizes, race.allEntries)
	return nil
}

func (race *Race) lockedSortEntries() {
	sorted := EntrySort(race.allEntries)
	sort.Sort(&sorted)
}

type RecentRacer struct {
	*Entry
	Place Place
}

func (race *Race) GenerateTemplate(req templateRequest) error {
	race.Lock()
	defer race.Unlock()
	data := map[string]interface{}{"Entries": race.allEntries}
	req.request.ParseForm()
	for key, val := range req.request.Form {
		data[key] = val[0]
	}
	switch req.name {
	default:
		req.name = "default"
	case "audit":
		data["Audit"] = race.auditLog
		fallthrough
	case "admin":
		data["Fields"] = race.optionalEntryFields
		data["Admin"] = true
		fallthrough
	case "results":
		numRecent := 10
		recentRacers := make([]RecentRacer, 0, numRecent)
		for i := len(race.allEntries) - 1; i >= 0; i-- {
			if race.allEntries[i].HasFinished() {
				if !race.allEntries[i].Confirmed || len(recentRacers) < numRecent {
					// add all unconfirmed racers that have finished, but only add confirmed recent racers up to length of numRecent
					recentRacers = append(recentRacers, RecentRacer{
						Entry: race.allEntries[i],
						Place: Place(i + 1),
					})
				}
			}
		}
		data["RecentRacers"] = recentRacers
	case "dayof":
	}
	if !race.started.IsZero() {
		diff := time.Since(race.started)
		data["Start"] = race.started.Format("3:04:05")
		data["Time"] = HumanDuration(diff).Clock()
		data["Seconds"] = fmt.Sprintf("%.0f", diff.Seconds())
		data["NextUpdate"] = diff / time.Millisecond % 1000
	}
	data["Prizes"] = race.prizes
	buf := tmplPool.Get()
	defer tmplPool.Put(buf)
	// comment out below four lines for performance!
	raceResultsTemplate, err := template.New("template").Funcs(raceResultsFuncMap).ParseFiles("raceResults.template")
	if err != nil {
		return err
	}
	err = raceResultsTemplate.ExecuteTemplate(buf, req.name, data)
	if err == nil {
		// no errors processing the template, copy the generated data
		io.Copy(req.writer, buf)
	}
	return err
}

func modifyEntryHandler(w http.ResponseWriter, r *http.Request, race *Race) {
	place, err := strconv.Atoi(r.FormValue("Place"))
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error %s getting place", err)
		return
	}
	nonce := r.FormValue("Nonce")
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "Error %s getting nonce", err)
		return
	}
	entry, err := parseEntry(r, race)
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "%v", err)
		return
	}
	err = race.ModifyEntry(nonce, Place(place), entry)
	if err != nil {
		showErrorForAdmin(w, r.Referer(), "%v", err)
		return
	}
	race.RecordTimeForBib(entry.Bib) //confirm all modified entries
	http.Redirect(w, r, r.Referer(), 301)
	return
}

type Race struct {
	started             time.Time
	startRaceChan       chan time.Time
	optionalEntryFields []string
	bibbedEntries       map[Bib]*Entry // map of Bib #s pointing to bibbed entries only, for link bib lookup
	allEntries          []*Entry       // a sorted slice of all Entries, bibbed and unbibbed, w/ result or not, sorted by Place (first to last)
	auditLog            []Audit        // A writeonly location to record the actions/events of the race
	prizes              []Prize
	optionalEmailIndex  int
	sync.RWMutex
	testingTime *time.Time //used only for testing -- if set, return time events from here, otherwise, pull time from syscall
}

func NewRace() *Race {
	start := make(chan time.Time)
	go listenForRacers(start)
	race := &Race{
		startRaceChan:      start,
		bibbedEntries:      make(map[Bib]*Entry),
		allEntries:         make([]*Entry, 0, 1024),
		auditLog:           make([]Audit, 0, 1024),
		prizes:             make([]Prize, 0, 48),
		optionalEmailIndex: -1, // initialize it to an invalid value
	}
	log.Printf("Initialized the race")
	return race
}

func (race *Race) GetTime() time.Time {
	if race.testingTime == nil {
		return time.Now()
	}
	return *race.testingTime
}

func (race *Race) WriteCSV(writer *csv.Writer) error {
	race.Lock()
	defer race.Unlock()
	err := writer.Write(append(headers, race.optionalEntryFields...))
	if err != nil {
		return err
	}
	if !race.started.IsZero() {
		timeStarted := []string{"", "", "", "", "", "", "", race.started.Format(time.ANSIC), ""}
		err = writer.Write(append(timeStarted, race.optionalEntryFields...))
		if err != nil {
			return err
		}
	}
	for place, entry := range race.allEntries {
		err = writer.Write(append([]string{entry.Fname, entry.Lname, strconv.Itoa(int(entry.Age)), gender(entry.Male), entry.Bib.String(), strconv.Itoa(place + 1), entry.Duration.String(), entry.TimeFinishedString(), fmt.Sprintf("%t", entry.Confirmed)}, entry.Optional...))
		if err != nil {
			return err
		}
	}
	return nil
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for x := range a {
		if a[x] != b[x] {
			return false
		}
	}
	return true
}

func (race *Race) SetOptionalFields(of []string) error {
	race.Lock()
	defer race.Unlock()
	switch {
	case len(race.allEntries) == 0:
		race.optionalEntryFields = of
		for x, fn := range race.optionalEntryFields {
			if fn == config.emailField {
				race.optionalEmailIndex = x
				break
			}
		}
		return nil
	case equalStringSlices(of, race.optionalEntryFields):
		return nil
	default:
		return fmt.Errorf("Racers already created!  Cannot change the optional fields now!")
	}
}

func (race *Race) GetOptionalFields() []string {
	race.RLock()
	defer race.RUnlock()
	dst := make([]string, len(race.optionalEntryFields))
	copy(dst, race.optionalEntryFields)
	return dst
}

func (race *Race) SetPrizes(prizes []Prize) {
	race.Lock()
	defer race.Unlock()
	race.prizes = prizes
	recomputeAllPrizes(race.prizes, race.allEntries)
}

func (race *Race) Start(t *time.Time) error { // optional time
	race.Lock()
	defer race.Unlock()
	if !race.started.IsZero() && race.started != *t {
		return fmt.Errorf("Race is already started at - %s, can't start it at %s", race.started.Format(time.ANSIC), t.Format(time.ANSIC))
	}
	if t == nil {
		race.started = race.GetTime()
	} else {
		race.started = *t
	}
	race.startRaceChan <- race.started
	return nil
}

func (race *Race) ModifyEntry(nonce string, place Place, mod Entry) error {
	race.Lock()
	defer race.Unlock()
	if nonce != race.allEntries[int(place)-1].Nonce() {
		return fmt.Errorf("Error updating entry - audit record was out of date, try your change again")
	}
	err := race.normalizeEntry(&mod)
	if err != nil {
		return err
	}
	placeIndex := int(place - 1)
	if placeIndex < 0 || placeIndex >= len(race.allEntries) {
		return fmt.Errorf("placeIndex of %d is out of bounds", placeIndex)
	}
	src := race.allEntries[placeIndex]
	delete(race.bibbedEntries, src.Bib)
	dest, ok := race.bibbedEntries[mod.Bib]
	if mod.Bib == NoBib || dest == src {
		*(race.allEntries[placeIndex]) = mod
	} else if !ok {
		// bib not found, must have been changed
		race.allEntries[placeIndex] = &mod
		race.bibbedEntries[mod.Bib] = &mod
	} else {
		race.bibbedEntries[src.Bib] = src
		return fmt.Errorf("Bib #%d already assigned to %s %s", mod.Bib, dest.Fname, dest.Lname)
	}
	race.lockedSortEntries()
	recomputeAllPrizes(race.prizes, race.allEntries)
	return nil
}

type RaceHandler func(http.ResponseWriter, *http.Request, *Race)

func (rh RaceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rh(w, r, globalRace)
}

var globalRace *Race // only used in/from main(), not from testing

func init() {
	globalRace = NewRace()
	http.Handle(config.webserverHostname+"/", RaceHandler(handler))
	http.Handle(config.webserverHostname+"/dayof", RaceHandler(handler))
	http.Handle(config.webserverHostname+"/admin", RaceHandler(handler))
	http.Handle(config.webserverHostname+"/start", RaceHandler(startHandler))
	http.Handle(config.webserverHostname+"/linkBib", RaceHandler(linkBibHandler))
	http.Handle(config.webserverHostname+"/addEntry", RaceHandler(addEntryHandler))
	http.Handle(config.webserverHostname+"/modifyEntry", RaceHandler(modifyEntryHandler))
	http.Handle(config.webserverHostname+"/download", RaceHandler(downloadHandler))
	http.Handle(config.webserverHostname+"/uploadRacers", RaceHandler(uploadRacersHandler))
	http.Handle(config.webserverHostname+"/uploadPrizes", RaceHandler(uploadPrizesHandler))
	http.Handle(config.webserverHostname+"/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))
	http.Handle(config.webserverHostname+"/fonts/", http.StripPrefix("/fonts/", http.FileServer(http.Dir("fonts/"))))
	http.Handle("/", http.RedirectHandler("http://"+config.webserverHostname+"/", 307))
	req, err := uploadFile("prizes.json")
	if err == nil {
		resp := httptest.NewRecorder()
		uploadPrizesHandler(resp, req, globalRace)
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
		listener, err = net.Listen("tcp", ":8080")
		if err != nil {
			log.Fatalf("Error listening on port 8080! - %s\n", err)
			return
		}
	} else {
		go func() {
			log.Fatal(http.ListenAndServeTLS(":443", "racergo.cert", "racergo.key", nil))
		}()
	}
	port := strings.Split(listener.Addr().String(), ":")
	portNum := port[len(port)-1]
	log.Printf("Basic - http://%s:%s", config.webserverHostname, portNum)
	log.Printf("Admin - http://%s:%s/admin", config.webserverHostname, portNum)
	log.Printf("Audit - http://%s:%s/audit", config.webserverHostname, portNum)
	log.Printf("Dayof - http://%s:%s/dayof", config.webserverHostname, portNum)
	log.Printf("Mobile Scanner Linker - http://%s:%s/linkBib?bib=%%s&scanned=true", config.webserverHostname, portNum)
	log.Printf("Large Screen Live Results - http://%s:%s/results", config.webserverHostname, portNum)
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
