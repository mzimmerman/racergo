package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func addTestEntry(race *Race, t *testing.T, e *Entry, optionalEntryFields []string) {
	values := make(url.Values)
	values.Add("Bib", strconv.Itoa(int(e.Bib)))
	values.Add("Age", strconv.Itoa(int(e.Age)))
	values.Add("Fname", e.Fname)
	values.Add("Lname", e.Lname)
	values.Add("Male", strconv.FormatBool(e.Male))
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

func TestDownloadAndAudit(t *testing.T) {
	race := NewRace()
	raceStart := time.Now().Add(-time.Hour)
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
		Entry{4, "G", "H", false, 35, []string{"userG@host.com", "XSmall"}, HumanDuration(time.Millisecond), raceStart.Add(time.Millisecond), true},
	}
	for _, u := range users {
		addTestEntry(race, t, &u, optionalEntryFields)
	}
	validateDownload(t, race, fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Email,T-Shirt
A,B,15,M,1,1,--,--,userA@host.com,Large
C,D,25,F,2,2,--,--,userC@host.com,Medium
E,F,30,M,3,3,--,--,userE@host.com,Small
G,H,35,F,4,4,--,--,userG@host.com,XSmall
`))
	// link bibs, then validate
	*race.testingTime = raceStart.Add(time.Millisecond)
	linkBibTesting(t, race, 4, false)
	linkBibTesting(t, race, 4, false)
	*race.testingTime = raceStart.Add(time.Second)
	linkBibTesting(t, race, 1, false)
	linkBibTesting(t, race, 1, false)
	*race.testingTime = raceStart.Add(time.Minute)
	linkBibTesting(t, race, 2, false)
	linkBibTesting(t, race, 2, false)
	*race.testingTime = raceStart.Add(time.Hour)
	linkBibTesting(t, race, 3, false)
	linkBibTesting(t, race, 3, false)

	validateDownload(t, race, fmt.Sprintf(`Fname,Lname,Age,Gender,Bib,Overall Place,Duration,Time Finished,Email,T-Shirt
G,H,35,F,4,1,00:00:00.00,%s,userG@host.com,XSmall
A,B,15,M,1,2,00:00:01.00,%s,userA@host.com,Large
C,D,25,F,2,3,00:01:00.00,%s,userC@host.com,Medium
E,F,30,M,3,4,01:00:00.00,%s,userE@host.com,Small
`,
		raceStart.Add(time.Millisecond).Format(time.ANSIC),
		raceStart.Add(time.Second).Format(time.ANSIC),
		raceStart.Add(time.Minute).Format(time.ANSIC),
		raceStart.Add(time.Hour).Format(time.ANSIC),
	))

	// TODO: change results through audit post, validate
}

func linkBibTesting(t *testing.T, race *Race, bib int, remove bool) {
	req, err := http.NewRequest("post", "", nil)
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	req.ParseForm()
	req.Form.Set("bib", strconv.Itoa(bib))
	if remove {
		req.Form.Set("remove", "true")
	}
	w := httptest.NewRecorder()
	linkBibHandler(w, req, race)
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("Expected redirect, got %v - %s", w.Code, w.Body)
	}
}

func validateDownload(t *testing.T, race *Race, expected string) {
	r, _ := http.NewRequest("GET", "/download", nil)
	w := httptest.NewRecorder()
	downloadHandler(w, r, race)
	if w, g := expected, w.Body.String(); w != g {
		t.Errorf("Wanted:\n%s\n\nGot:\n%s", w, g)
	}
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
		t.Errorf("Expected %d, got %d, %s", expected, w.Code, w.Body.String())
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
	}{
		{1, 0, 409, false, false}, // no bib #0 in test_runners.csv
		{1, 1, 301, false, false},
		{1, 1, 301, false, true},
		{1, 1, 409, false, true},
		{1, 1, 301, false, false},
		{2, 2, 301, false, false},
		{2, 2, 301, false, true},
		{2, 2, 301, false, false},
		{3, 3, 301, false, false},
		{4, 4, 301, false, false},
		{3, 3, 301, false, true},  // remove bib 3 from place 3
		{4, 3, 301, false, false}, // re-add 3 which will swap their positions
		{5, 5, 301, false, false},
		{6, 6, 301, false, false},
		{1, 1, 301, true, false},
		{2, 2, 301, true, false},
		{4, 3, 301, true, false},
		{3, 4, 301, true, false},
		{5, 5, 301, true, false},
		{6, 6, 301, true, false},
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
