package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startRace(race *Race) {
	r, _ := http.NewRequest("get", "/start", nil)
	w := httptest.NewRecorder()
	startHandler(w, r, race)
}

func modifyTestEntry(race *Race, t *testing.T, place Place, e *Entry, optionalEntryFields []string) {
	values := make(url.Values)
	values.Add("Place", strconv.Itoa(int(place)))
	race.Lock()
	values.Add("Nonce", race.allEntries[place-1].Nonce())
	race.Unlock()
	values.Add("Bib", strconv.Itoa(int(e.Bib)))
	values.Add("Age", strconv.Itoa(int(e.Age)))
	values.Add("Fname", e.Fname)
	values.Add("Lname", e.Lname)
	values.Add("Duration", e.Duration.String())
	values.Add("Male", gender(e.Male))
	for x, o := range e.Optional {
		values.Add(optionalEntryFields[x], o)
	}
	r, err := http.NewRequest("GET", "/modifyEntry?"+values.Encode(), nil)
	if err != nil {
		t.Fatalf("Error creating request - %v", err)
	}
	w := httptest.NewRecorder()
	modifyEntryHandler(w, r, race)
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("Wrong status received on modify entry - %v, got %d, %s", *e, w.Code, w.Body.String())
	}
}

func addTestEntry(race *Race, t *testing.T, e *Entry, optionalEntryFields []string) {
	values := make(url.Values)
	values.Add("Bib", strconv.Itoa(int(e.Bib)))
	values.Add("Age", strconv.Itoa(int(e.Age)))
	values.Add("Fname", e.Fname)
	values.Add("Lname", e.Lname)
	values.Add("Male", gender(e.Male))
	for x, o := range e.Optional {
		values.Add(optionalEntryFields[x], o)
	}
	r, err := http.NewRequest("GET", "/addEntry?"+values.Encode(), nil)
	if err != nil {
		t.Fatalf("Error creating request - %v", err)
	}
	w := httptest.NewRecorder()
	addEntryHandler(w, r, race)
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("Wrong status received on add entry - %v, got %d, %s", *e, w.Code, w.Body.String())
	}
}

func downloadUploadCompareDownload(t *testing.T, race *Race) {
	want := downloadCurrent(t, race)
	if err := ioutil.WriteFile("auditUploadTemp", want, 0666); err != nil {
		t.Errorf("Error writing temp audit upload file - %v", err)
		return
	}
	tempRace := NewRace()
	tempRace.Start(&race.started)
	tempRace.testingTime = race.testingTime
	testUploadRacersHelper(t, "auditUploadTemp", http.StatusMovedPermanently, tempRace)

	got := downloadCurrent(t, tempRace)
	if string(want) != string(got) {
		_, filename, line, _ := runtime.Caller(1)
		t.Errorf("%s:%d - Downloaded - %s", filename, line, want)
		t.Errorf("Wanted:\n%s\nGot:\n%s", want, got)
	}
}

func TestDownloadAndAudit(t *testing.T) {
	race := NewRace()
	raceStart := time.Now().Add(-time.Hour).Round(time.Second)
	race.testingTime = &time.Time{}
	*race.testingTime = raceStart
	startRace(race)
	optionalEntryFields := []string{"Email", "T-Shirt"}
	if err := race.SetOptionalFields(optionalEntryFields); err != nil {
		t.Errorf("Error setting optional entry fields")
	}

	users := []Entry{
		Entry{1, "A", "B", true, 15, []string{"userA@host.com", "Large"}, HumanDuration(time.Second), raceStart.Add(time.Second), true},
		Entry{2, "C", "D", false, 25, []string{"userC@host.com", "Medium"}, HumanDuration(time.Minute), raceStart.Add(time.Minute), true},
		Entry{3, "E", "F", true, 30, []string{"userE@host.com", "Small"}, HumanDuration(time.Hour), raceStart.Add(time.Hour), true},
		Entry{4, "G", "H", false, 35, []string{"userG@host.com", "XSmall"}, HumanDuration(time.Millisecond * 10), raceStart.Add(time.Millisecond * 10), true},
	}
	for _, u := range users {
		addTestEntry(race, t, &u, optionalEntryFields)
	}
	downloadUploadCompareDownload(t, race)
	validateDownload(t, race, 1, fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Confirmed,Email,T-Shirt
,,,,,,,%s,,Email,T-Shirt
A,B,15,M,1,1,--,--,false,userA@host.com,Large
C,D,25,F,2,2,--,--,false,userC@host.com,Medium
E,F,30,M,3,3,--,--,false,userE@host.com,Small
G,H,35,F,4,4,--,--,false,userG@host.com,XSmall
`,
		raceStart.Format(time.ANSIC),
	))
	// link bibs, then validate
	*race.testingTime = raceStart.Add(time.Millisecond * 10)
	linkBibTesting(t, race, 4, false, false)
	downloadUploadCompareDownload(t, race)
	linkBibTesting(t, race, 4, false, true)
	downloadUploadCompareDownload(t, race)
	*race.testingTime = raceStart.Add(time.Second)
	linkBibTesting(t, race, 1, false, false)
	downloadUploadCompareDownload(t, race)
	linkBibTesting(t, race, 1, false, true)
	downloadUploadCompareDownload(t, race)
	*race.testingTime = raceStart.Add(time.Minute)
	linkBibTesting(t, race, 2, false, false)
	downloadUploadCompareDownload(t, race)
	linkBibTesting(t, race, 2, false, true)
	downloadUploadCompareDownload(t, race)
	*race.testingTime = raceStart.Add(time.Hour)
	linkBibTesting(t, race, 3, false, false)
	downloadUploadCompareDownload(t, race)
	linkBibTesting(t, race, 3, false, true)
	downloadUploadCompareDownload(t, race)

	validateDownload(t, race, 2, fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Confirmed,Email,T-Shirt
,,,,,,,%s,,Email,T-Shirt
G,H,35,F,4,1,00:00:00.01,%s,true,userG@host.com,XSmall
A,B,15,M,1,2,00:00:01.00,%s,true,userA@host.com,Large
C,D,25,F,2,3,00:01:00.00,%s,true,userC@host.com,Medium
E,F,30,M,3,4,01:00:00.00,%s,true,userE@host.com,Small
`,
		raceStart.Format(time.ANSIC),
		raceStart.Add(time.Millisecond*10).Format(time.ANSIC),
		raceStart.Add(time.Second).Format(time.ANSIC),
		raceStart.Add(time.Minute).Format(time.ANSIC),
		raceStart.Add(time.Hour).Format(time.ANSIC),
	))
	downloadUploadCompareDownload(t, race)
	// now upload modified results in a new race
	race = NewRace()
	race.testingTime = &time.Time{}
	*race.testingTime = raceStart
	startRace(race)
	if err := ioutil.WriteFile("auditUploadTemp", []byte(fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Confirmed,Email,T-Shirt
,,,,,,,%s,,Email,T-Shirt
G,H,35,F,4,1,--,--,false,userG@host.com,GT
A,B,15,M,1,2,00:00:01.00,%s,true,userA@host.com,AT
C,D,25,F,2,3,--,--,true,userC@host.com,CT
E,F,30,M,3,4,01:00:00.00,%s,true,userE@host.com,ET
`,
		raceStart.Format(time.ANSIC),
		raceStart.Add(time.Second).Format(time.ANSIC),
		raceStart.Add(time.Hour).Format(time.ANSIC),
	)), 0666); err != nil {
		t.Errorf("Error writing file - %v", err)
	}
	testUploadRacersHelper(t, "auditUploadTemp", 301, race)

	validateDownload(t, race, 3, fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Confirmed,Email,T-Shirt
,,,,,,,%s,,Email,T-Shirt
A,B,15,M,1,1,00:00:01.00,%s,true,userA@host.com,AT
E,F,30,M,3,2,01:00:00.00,%s,true,userE@host.com,ET
C,D,25,F,2,3,--,--,false,userC@host.com,CT
G,H,35,F,4,4,--,--,false,userG@host.com,GT
`,
		raceStart.Format(time.ANSIC),
		raceStart.Add(time.Second).Format(time.ANSIC),
		raceStart.Add(time.Hour).Format(time.ANSIC),
	))
	downloadUploadCompareDownload(t, race)

	// link them again
	*race.testingTime = raceStart.Add(time.Millisecond * 10 * 2)
	linkBibTesting(t, race, 2, false, false)
	downloadUploadCompareDownload(t, race)
	linkBibTesting(t, race, 2, false, true)
	downloadUploadCompareDownload(t, race)
	*race.testingTime = raceStart.Add(time.Minute * 2)
	linkBibTesting(t, race, 4, false, false)
	downloadUploadCompareDownload(t, race)
	linkBibTesting(t, race, 4, false, true)
	downloadUploadCompareDownload(t, race)

	validateDownload(t, race, 4, fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Confirmed,Email,T-Shirt
,,,,,,,%s,,Email,T-Shirt
C,D,25,F,2,1,00:00:00.02,%s,true,userC@host.com,CT
A,B,15,M,1,2,00:00:01.00,%s,true,userA@host.com,AT
G,H,35,F,4,3,00:02:00.00,%s,true,userG@host.com,GT
E,F,30,M,3,4,01:00:00.00,%s,true,userE@host.com,ET
`,
		raceStart.Format(time.ANSIC),
		raceStart.Add(time.Millisecond*10*2).Format(time.ANSIC),
		raceStart.Add(time.Second).Format(time.ANSIC),
		raceStart.Add(time.Minute*2).Format(time.ANSIC),
		raceStart.Add(time.Hour).Format(time.ANSIC),
	))
	downloadUploadCompareDownload(t, race)

	moddedEntry := &Entry{
		Age:      10,
		Bib:      5,
		Fname:    "I",
		Lname:    "J",
		Male:     false,
		Duration: HumanDuration(time.Millisecond * 10 * 1),
		Optional: []string{"userI@host.com", "IJ"},
	}
	err := race.Start(&raceStart)
	if err != nil {
		t.Errorf("Error starting race - %v", err)
	}

	modifyTestEntry(race, t, Place(3), moddedEntry, optionalEntryFields)
	validateDownload(t, race, 5, fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Confirmed,Email,T-Shirt
,,,,,,,%s,,Email,T-Shirt
I,J,10,F,5,1,00:00:00.01,%s,true,userI@host.com,IJ
C,D,25,F,2,2,00:00:00.02,%s,true,userC@host.com,CT
A,B,15,M,1,3,00:00:01.00,%s,true,userA@host.com,AT
E,F,30,M,3,4,01:00:00.00,%s,true,userE@host.com,ET
`,
		raceStart.Format(time.ANSIC),
		raceStart.Add(time.Millisecond*10*1).Format(time.ANSIC),
		raceStart.Add(time.Millisecond*10*2).Format(time.ANSIC),
		raceStart.Add(time.Second).Format(time.ANSIC),
		raceStart.Add(time.Hour).Format(time.ANSIC),
	))
}

func linkBibTesting(t *testing.T, race *Race, bib int, remove, confirm bool) {
	req, err := http.NewRequest("post", "", nil)
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	req.ParseForm()
	req.Form.Set("bib", strconv.Itoa(bib))
	if remove {
		req.Form.Set("remove", "true")
	}
	if confirm {
		req.Form.Set("scanned", "true")
	}
	w := httptest.NewRecorder()
	linkBibHandler(w, req, race)
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("%d - Expected redirect, got %v - %s", bib, w.Code, w.Body)
	}
}

func downloadCurrent(t *testing.T, race *Race) []byte {
	r, _ := http.NewRequest("GET", "/download", nil)
	w := httptest.NewRecorder()
	downloadHandler(w, r, race)
	if w.Code != http.StatusOK {
		t.Errorf("Error downloading data, wanted status %d, got %d", http.StatusOK, w.Code)
	}
	return w.Body.Bytes()
}

func validateDownload(t *testing.T, race *Race, testnum int, expected string) {
	current := downloadCurrent(t, race)
	expectedLines, err := csv.NewReader(strings.NewReader(expected)).ReadAll()
	if err != nil {
		t.Errorf("Error parsing expected - %v", err)
	}
	currentLines, err := csv.NewReader(bytes.NewReader(current)).ReadAll()
	if err != nil {
		t.Errorf("Error parsing current - %v", err)
	}
NextCurrent:
	for _, cur := range currentLines {
		for _, exp := range expectedLines {
			if reflect.DeepEqual(cur, exp) {
				continue NextCurrent
			}
		}
		t.Errorf("Got current: %s", strings.Join(cur, ","))
	}
NextExpected:
	for _, exp := range expectedLines {
		for _, cur := range currentLines {
			if reflect.DeepEqual(cur, exp) {
				continue NextExpected
			}
		}
		t.Errorf("Got expected: %s", strings.Join(exp, ","))
	}
}

func TestRestoreTime(t *testing.T) {
	now := time.Now().Round(time.Second)
	race := NewRace()
	race.testingTime = &now
	want := fmt.Sprintf("%s\n", strings.Join(headers, ","))
	got := downloadCurrent(t, race)
	f, err := ioutil.TempFile("/tmp", "racergorestoretime")
	if err != nil {
		t.Errorf("Error writing temp file - %v", err)
	}
	f.Write(got)
	f.Close()
	nonStartedOutput := f.Name()
	if want != string(got) {
		t.Errorf("Error restoring time - wanted %s, got %s", want, got)
	}

	// now start the race
	startRace(race)
	//	const headers = []string{"Fname", "Lname", "Age", "Gender", "Bib", "Overall Place", "Duration", "Time Finished", "Confirmed"}
	race.AddEntry(Entry{
		Fname: "matt",
		Lname: "z",
		Age:   34,
		Male:  true,
		Bib:   1,
	})
	*race.testingTime = race.testingTime.Add(time.Minute)
	race.RecordTimeForBib(1)
	race.ConfirmTimeForBib(1)
	want = fmt.Sprintf("%s\n,,,,,,,%s,\nmatt,z,34,M,1,1,00:01:00.00,%s,true\n", strings.Join(headers, ","), now.Add(-time.Minute).Format(time.ANSIC), now.Format(time.ANSIC))
	got = downloadCurrent(t, race)
	f, err = ioutil.TempFile("/tmp", "racergorestoretime")
	if err != nil {
		t.Errorf("Error writing temp file - %v", err)
	}
	f.Write(got)
	f.Close()
	startedOutput := f.Name()
	if want != string(got) {
		t.Errorf("Error restoring time")
		t.Errorf("wan - %q", want)
		t.Errorf("got - %q", got)
	}

	// upload a non-started race output
	race = NewRace()
	race.testingTime = &time.Time{}
	*race.testingTime = now.Add(10 * time.Second)
	testUploadRacersHelper(t, nonStartedOutput, 409, race)
	race.Lock()
	if !race.started.IsZero() {
		t.Errorf("Race should not be started!")
	}
	race.Unlock()

	// upload a started race output
	race = NewRace()
	race.testingTime = &time.Time{}
	*race.testingTime = now.Add(10 * time.Second)
	testUploadRacersHelper(t, startedOutput, 301, race)
	race.Lock()
	now = now.Add(-time.Minute)
	if race.started != now {
		t.Errorf("Race should be started and equal now! - got %s, want %s", race.started, now)
	}
	race.Unlock()

	// do it again, race is already started!
	race = NewRace()
	fakeStart := now.Add(time.Second * 5)
	race.Start(&fakeStart)
	testUploadRacersHelper(t, startedOutput, 409, race)
}

func TestLoadDuplicates(t *testing.T) {
	race := NewRace()
	startRace(race)
	// race is started, load the racers
	if !testUploadRacersHelper(t, "test_dupes.csv", 409, race) {
		t.Error()
	}

	race = NewRace()
	startRace(race)
	// race is started, load the racers
	if !testUploadRacersHelper(t, "test_one_entry.csv", 301, race) {
		t.Error()
	}

	// upload the same bib to get duplicates
	if !testUploadRacersHelper(t, "test_one_entry.csv", 409, race) {
		t.Error()
	}
}

func TestLoadDuplicateOptionals(t *testing.T) {
	race := NewRace()
	startRace(race)
	// race is started, load the racers
	if !testUploadRacersHelper(t, "test_one_entry.csv", 301, race) {
		t.Error()
	}
	if !testUploadRacersHelper(t, "test_two_entry.csv", 301, race) {
		t.Error()
	}
	if !testUploadRacersHelper(t, "test_three_entry.csv", 409, race) {
		t.Error()
	}
}

func TestRescoreOnChange(t *testing.T) {
	race := NewRace()
	req, err := uploadFile("test_prizes.json")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w := httptest.NewRecorder()
	uploadPrizesHandler(w, req, race)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}
	now := time.Now()
	if err := race.AddEntry(Entry{
		Fname: "A",
		Lname: "A",
		Bib:   1,
		Age:   15,
		Male:  true,
	}); err != nil {
		t.Errorf("Error adding entry - %v", err)
	}
	if err := race.AddEntry(Entry{
		Fname: "B",
		Lname: "B",
		Bib:   2,
		Age:   15,
		Male:  true,
	}); err != nil {
		t.Errorf("Error adding entry - %v", err)
	}
	race.Start(&now)
	if err := race.RecordTimeForBib(1); err != nil {
		t.Errorf("Error linking bib - %v", err)
	}
	if err := race.ConfirmTimeForBib(1); err != nil {
		t.Errorf("Error linking bib - %v", err)
	}
	if err := race.RecordTimeForBib(2); err != nil {
		t.Errorf("Error linking bib - %v", err)
	}
	if err := race.ConfirmTimeForBib(2); err != nil {
		t.Errorf("Error linking bib - %v", err)
	}
	race.RLock()

	if race.prizes[0].Winners[0].Fname != "A" {
		t.Errorf("Wrong winner, expected A but got %s", race.prizes[0].Winners[0].Fname)
	}

	entry := *(race.allEntries[0])
	nonce := entry.Nonce()
	race.RUnlock()

	// change A to 1 second later
	entry.Duration = entry.Duration + HumanDuration(time.Second*2)
	race.ModifyEntry(nonce, 1, entry)

	race.Lock()
	if race.prizes[0].Winners[0].Fname != "B" {
		t.Errorf("Wrong winner, expected B but got %s", race.prizes[0].Winners[0].Fname)
	}
	race.Unlock()

	race = NewRace()
	req, err = uploadFile("test_prizes.json")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w = httptest.NewRecorder()
	uploadPrizesHandler(w, req, race)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}

	tmpFile, err := ioutil.TempFile("/tmp", "testRescore")
	if err != nil {
		t.Errorf("Error opening temp file - %v", err)
	}
	if _, err := tmpFile.WriteString(fmt.Sprintf("Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Confirmed\n,,,,,,,%s,\nA,A,15,M,1,1,%s,%s,true\nB,B,15,M,2,2,%s,%s,true\n", now.Format(time.ANSIC), HumanDuration(time.Second*2), now.Add(time.Second*2).Format(time.ANSIC), HumanDuration(time.Second), now.Add(time.Second).Format(time.ANSIC))); err != nil {
		t.Errorf("Error writing temp file - %v", err)
	}
	fname := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		t.Errorf("Error closing temp file - %v", err)
	}
	testUploadRacersHelper(t, fname, http.StatusMovedPermanently, race)

	race.Lock()
	if race.prizes[0].Winners[0].Fname != "B" {
		t.Errorf("Wrong winner, expected B but got %s", race.prizes[0].Winners[0].Fname)
	}
	race.Unlock()
}

// returns true if expected matches and no error
func testUploadRacersHelper(t *testing.T, filename string, expected int, race *Race) bool {
	req, err := uploadFile(filename)
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
		return false
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
		return false
	}
	w := httptest.NewRecorder()
	uploadRacersHandler(w, req, race)
	if w.Code != expected {
		_, filename, line, _ := runtime.Caller(1)
		t.Errorf("Expected %d, got %d, %s - from line - %s:%d", expected, w.Code, w.Body.String(), filename, line)
		return false
	}
	return true
}

func TestLoadRacers(t *testing.T) {
	race := NewRace()
	startRace(race)
	// race is started, load the racers
	if !testUploadRacersHelper(t, "test_runners.csv", 301, race) {
		t.Error()
	}
	race = NewRace()
	startRace(race)
	if !testUploadRacersHelper(t, "test_runners2.csv", 409, race) {
		t.Error()
	}

	race = NewRace()
	if !testUploadRacersHelper(t, "test_runners2.csv", 301, race) {
		t.Error()
	}

	race = NewRace()
	startRace(race)
	if !testUploadRacersHelper(t, "test_runners3.csv", 409, race) {
		t.Error()
	}

	race = NewRace()
	if !testUploadRacersHelper(t, "test_runners2.csv", 301, race) {
		t.Error()
	}

}

func TestTemplates(t *testing.T) {
	race := NewRace()
	urls := []string{
		"/",
		"/audit",
		"/results",
		"/admin",
	}
	for _, u := range urls {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("get", u, nil)
		handler(w, r, race)
		if w.Code != http.StatusOK {
			t.Log(w.Body.String())
			t.Errorf("Error fetching template - %s, expected %d, got %d", u, http.StatusOK, w.Code)
		}
	}
	optionalEntryFields := []string{"Email", "Large"}
	err := race.SetOptionalFields(optionalEntryFields)
	if err != nil {
		t.Errorf("Nil expected, got %v", err)
	}
	users := []Entry{
		Entry{-1, "A", "B", true, 15, []string{"userA@host.com", "Large"}, 0, time.Time{}, true},
		Entry{-1, "C", "D", false, 25, []string{"userC@host.com", "Medium"}, 0, time.Time{}, true},
		Entry{-1, "E", "F", true, 30, []string{"userE@host.com", "Small"}, 0, time.Time{}, true},
		Entry{5, "G", "H", false, 35, []string{"userG@host.com", "XSmall"}, 0, time.Time{}, true},
	}
	for _, u := range users {
		t.Logf("Adding entry - %v", u)
		addTestEntry(race, t, &u, optionalEntryFields)
	}
	for _, u := range urls {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("get", u, nil)
		handler(w, r, race)
		if w.Code != http.StatusOK {
			t.Log(w.Body.String())
			t.Errorf("Error fetching template - %s, expected %d, got %d", u, http.StatusOK, w.Code)
		}
	}
	startRace(race)
	for _, u := range urls {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("get", u, nil)
		handler(w, r, race)
		if w.Code != http.StatusOK {
			t.Log(w.Body.String())
			t.Errorf("Error fetching template - %s, expected %d, got %d", u, http.StatusOK, w.Code)
		}
	}
	users = []Entry{
		Entry{1, "H", "I", true, 15, []string{"userA@host.com", "Large"}, 0, time.Time{}, true},
		Entry{2, "J", "K", false, 25, []string{"userC@host.com", "Medium"}, 0, time.Time{}, true},
		Entry{3, "L", "M", true, 30, []string{"userE@host.com", "Small"}, 0, time.Time{}, true},
		Entry{4, "N", "O", false, 35, []string{"userG@host.com", "XSmall"}, 0, time.Time{}, true},
	}
	for _, u := range users {
		t.Logf("Adding entry - %v", u)
		addTestEntry(race, t, &u, optionalEntryFields)
	}
	for _, u := range urls {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("get", u, nil)
		handler(w, r, race)
		if w.Code != http.StatusOK {
			t.Log(w.Body.String())
			t.Errorf("Error fetching template - %s, expected %d, got %d", u, http.StatusOK, w.Code)
		}
		if u == "/admin" && !strings.Contains(w.Body.String(), "Email") {
			t.Log(w.Body.String())
			t.Errorf("Expected to see Email optional field in template output but did not get it")
		}
	}
}

func TestLink(t *testing.T) { // includes removing of racers
	race := NewRace()
	startRace(race)
	// race is started, load the racers
	if !testUploadRacersHelper(t, "test_runners.csv", 301, race) {
		t.Error()
	}
	// test the beginning, middle, and end
	tableTests := []struct {
		place     int
		bib       Bib
		code      int
		confirmed bool
		remove    bool
		scanned   bool
	}{
		{1, 0, 409, false, false, false}, // no bib #0 in test_runners.csv
		{1, 1, 301, false, false, false},
		{1, 1, 301, false, true, false},
		{1, 1, 409, false, true, false},
		{1, 1, 301, false, false, false},
		{2, 2, 301, false, false, false},
		{2, 2, 301, false, true, false},
		{2, 2, 301, false, false, false},
		{3, 3, 301, false, false, false},
		{4, 4, 301, false, false, false},
		{3, 3, 301, false, true, false},  // remove bib 3 from place 3
		{4, 3, 301, false, false, false}, // re-add 3 which will swap their positions
		{5, 5, 301, false, false, false},
		{6, 6, 301, false, false, false},
		{1, 1, 301, true, false, true},
		{2, 2, 301, true, false, true},
		{4, 3, 301, true, false, true},
		{3, 4, 301, true, false, true},
		{5, 5, 301, true, false, true},
		{6, 6, 301, true, false, true},
	}
	for i, x := range tableTests {
		t.Logf("Iteration %d", i)
		req, err := http.NewRequest("post", "", nil)
		if err != nil {
			t.Errorf("Unexpected error - %v", err)
		}
		req.ParseForm()
		req.Form.Set("bib", strconv.Itoa(int(x.bib)))
		if x.remove {
			req.Form.Set("remove", "true")
		}
		if x.scanned {
			req.Form.Set("scanned", "true")
		}
		w := httptest.NewRecorder()
		linkBibHandler(w, req, race)
		if x.code != w.Code {
			t.Errorf("Iteration - %d, Expected %d, got %d - %s", i, x.code, w.Code, w.Body.Bytes())
		}
		if x.bib <= 0 || x.remove {
			continue
		}
		race.RLock()
		results := race.allEntries
		if results[x.place-1].Confirmed != x.confirmed {
			t.Errorf("Iteration - %d, Result %v confirmed != %v", i, results[x.place-1], x.confirmed)
		}
		if results[x.place-1].Bib != x.bib {
			t.Errorf("Iteration - %d, Entry %v is not in place %d", i, results[x.place-1], x.place)
		}
		race.RUnlock()
	}
}

func TestPrizes(t *testing.T) {
	race := NewRace()
	startRace(race)
	// race is started, load the racers
	req, err := uploadFile("test_prizes.json")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w := httptest.NewRecorder()
	uploadPrizesHandler(w, req, race)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}

	if !testUploadRacersHelper(t, "test_runners_prizes.csv", 301, race) {
		t.Error()
	}
	race.RLock()
	entries := make([]Entry, len(race.allEntries))
	for x := range race.allEntries {
		entries[x] = *race.allEntries[x]
	}
	race.RUnlock()
	for _, entry := range entries {
		t.Logf("Iterating on entry - %#v", entry)
		req, err = http.NewRequest("post", "", nil)
		if err != nil {
			t.Errorf("Unexpected error - %v", err)
		}
		if req == nil {
			t.Fatalf("Unexpected nil request")
		}
		req.ParseForm()
		req.Form.Set("bib", strconv.Itoa(int(entry.Bib)))
		w = httptest.NewRecorder()
		linkBibHandler(w, req, race)
		if w.Code != 301 {
			t.Errorf("Expected redirect, got %s", w.Body)
		}
		req.Form.Set("scanned", "true")
		w = httptest.NewRecorder()
		linkBibHandler(w, req, race)
		if w.Code != 301 {
			t.Errorf("Expected redirect, got %s", w.Body)
		}
	}
	race.RLock()
	results := race.allEntries
	prizes := race.prizes
	for x := range results {
		t.Logf("Place #%d - %#v", x+1, results[x])
	}
	for x := range prizes {
		t.Logf("--Prize %s--", prizes[x].Title)
		for y := range prizes[x].Winners {
			t.Logf("%v", prizes[x].Winners[y])
		}
	}
	EqualInt(t, len(prizes[0].Winners), 1) // men's overall
	if prizes[0].Winners[0] != results[0] {
		t.Errorf("Wrong winner i")
	}
	EqualInt(t, len(prizes[2].Winners), 1) // men's u10
	EqualResult(t, prizes[2].Winners[0], results[6])
	EqualInt(t, len(prizes[4].Winners), 0)  // men's 11-15
	EqualInt(t, len(prizes[6].Winners), 0)  // men's 16-20
	EqualInt(t, len(prizes[8].Winners), 0)  // men's 21-25
	EqualInt(t, len(prizes[10].Winners), 0) // men's 26-30
	EqualInt(t, len(prizes[12].Winners), 0) // men's 31-35
	EqualInt(t, len(prizes[14].Winners), 1) // men's 36-40
	EqualResult(t, prizes[14].Winners[0], results[1])
	EqualInt(t, len(prizes[16].Winners), 0) // men's 41-45
	EqualInt(t, len(prizes[18].Winners), 0) // men's 46-50
	EqualInt(t, len(prizes[20].Winners), 2) // men's 51-55
	EqualResult(t, prizes[20].Winners[0], results[3])
	EqualResult(t, prizes[20].Winners[1], results[4])
	//EqualInt(t,prizes[20].Winners[1], results[5]) // he didn't place
	EqualInt(t, len(prizes[22].Winners), 0) // men's 56-60
	EqualInt(t, len(prizes[24].Winners), 0) // men's 61+

	EqualInt(t, len(prizes[1].Winners), 1) // women's overall
	EqualResult(t, prizes[1].Winners[0], results[2])
	EqualInt(t, len(prizes[3].Winners), 1) // women's u10
	EqualResult(t, prizes[3].Winners[0], results[7])
	EqualInt(t, len(prizes[5].Winners), 0)  // women's 11-15
	EqualInt(t, len(prizes[7].Winners), 0)  // women's 16-20
	EqualInt(t, len(prizes[9].Winners), 0)  // women's 21-25
	EqualInt(t, len(prizes[11].Winners), 0) // women's 26-30
	EqualInt(t, len(prizes[13].Winners), 0) // women's 31-35
	EqualInt(t, len(prizes[15].Winners), 0) // women's 36-40
	EqualInt(t, len(prizes[17].Winners), 0) // women's 41-45
	EqualInt(t, len(prizes[19].Winners), 0) // women's 46-50
	EqualInt(t, len(prizes[21].Winners), 0) // women's 51-55
	EqualInt(t, len(prizes[23].Winners), 0) // women's 56-60
	EqualInt(t, len(prizes[25].Winners), 0) // women's 61+
	race.RUnlock()
}

func EqualInt(t *testing.T, got, expected int) {
	if got != expected {
		t.Errorf("Expected %d, got %d", expected, got)
	}
}

func EqualResult(t *testing.T, got, expected *Entry) {
	if got != expected {
		t.Errorf("Expected %v, got %v", expected, got)
	}
}

func TestSortResults(t *testing.T) {
	results := []*Entry{
		{Duration: HumanDuration(time.Second)},
		{Duration: HumanDuration(time.Minute)},
		{Duration: HumanDuration(time.Hour)},
	}
	expected := []HumanDuration{
		HumanDuration(time.Second),
		HumanDuration(time.Minute),
		HumanDuration(time.Hour),
	}
	sort.Sort((*EntrySort)(&results))
	for x := range results {
		if want, got := expected[x], results[x].Duration; want != got {
			t.Errorf("[%d] - Wanted %s, got %s", x, want, got)
		}
	}
	results = []*Entry{
		{Duration: HumanDuration(time.Minute)},
		{Duration: HumanDuration(time.Second)},
		{Duration: HumanDuration(0)},
		{Duration: HumanDuration(time.Hour)},
	}
	expected = []HumanDuration{
		HumanDuration(time.Second),
		HumanDuration(time.Minute),
		HumanDuration(time.Hour),
		HumanDuration(0),
	}
	sort.Sort((*EntrySort)(&results))
	for x := range results {
		if want, got := expected[x], results[x].Duration; want != got {
			t.Errorf("[%d] - Wanted %s, got %s", x, want, got)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		duration HumanDuration
		time     string
		clock    string
	}{
		{HumanDuration(time.Second * 120), "00:02:00.00", "00:02:00"},
		{HumanDuration(time.Second * 0), "--", "--"},
		{HumanDuration(time.Hour), "01:00:00.00", "01:00:00"},
		{HumanDuration(time.Second * 5), "00:00:05.00", "00:00:05"},
		{HumanDuration(time.Second * 50), "00:00:50.00", "00:00:50"},
		{HumanDuration(time.Hour + time.Minute*45 + time.Second*5), "01:45:05.00", "01:45:05"},
		{HumanDuration(time.Hour + time.Minute*45 + time.Second*5 + time.Millisecond*104), "01:45:05.10", "01:45:05"},
		{HumanDuration(time.Hour + time.Minute*45 + time.Second*5 + time.Millisecond*907), "01:45:05.91", "01:45:05"},
	}
	for _, val := range tests {
		if val.duration.String() != val.time {
			t.Errorf("Expected %s, got %d", val.time, val.duration.String())
		}
		if val.duration.Clock() != val.clock {
			t.Errorf("Expected %s, got %d", val.clock, val.duration.Clock())
		}
		newDuration, err := ParseHumanDuration(val.time)
		if err != nil {
			t.Errorf("Unexpected error - %v", err)
		}
		if newDuration-val.duration >= HumanDuration(time.Millisecond*10) { // rounding errors are okay
			t.Errorf("Expected %s, got %s", val.duration, newDuration)
		}
	}
}
