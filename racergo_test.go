package main

import (
	"fmt"
	"github.com/remogatto/prettytest"

	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type testSuite struct {
	prettytest.Suite
}

func TestRunner(t *testing.T) {
	prettytest.RunWithFormatter(
		t,
		new(prettytest.TDDFormatter),
		new(testSuite),
	)
}

func startRace() {
	reset()
	r, _ := http.NewRequest("get", "/start", nil)
	w := httptest.NewRecorder()
	startHandler(w, r)
}

func (t *testSuite) TestLoadRacers() {
	startRace()
	// race is started, load the racers
	req, err := uploadFile("test_runners.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w := httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	mutex.Lock()
	t.Equal(len(bibbedEntries), 8)
	t.Equal(len(unbibbedEntries), 0)
	t.Equal("G", bibbedEntries[4].Fname)
	t.Equal("H", bibbedEntries[4].Lname)
	mutex.Unlock()

	req, err = uploadFile("test_runners2.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	mutex.Lock()
	t.Equal(len(bibbedEntries), 4)
	t.Equal(len(unbibbedEntries), 4)
	mutex.Unlock()
	req, err = uploadFile("test_runners3.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	mutex.Lock()
	t.Equal(len(bibbedEntries), 0)
	t.Equal(len(unbibbedEntries), 8)
	mutex.Unlock()
}

func (t *testSuite) TestLink() { // includes removing of racers
	startRace()
	// race is started, load the racers
	req, err := uploadFile("test_runners.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w := httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	// 8 racers, test the beginning, middle, and end
	tableTests := []struct {
		place     int
		bib       int
		code      int
		confirmed bool
		remove    bool
	}{
		{1, 0, 409, false, false}, // no bib #0 in test_runners.csv
		{1, 1, 301, false, false},
		{1, 1, 301, false, true},
		{1, 1, 301, false, true},
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
		req, err := http.NewRequest("post", "", nil)
		req.ParseForm()
		req.Form.Set("bib", strconv.Itoa(x.bib))
		if x.remove {
			req.Form.Set("remove", "true")
		}
		t.Nil(err)
		w := httptest.NewRecorder()
		linkBib(w, req)
		t.Equal(x.code, w.Code, fmt.Sprintf("Iteration - %d, Expected %d, got %d - %s", i, x.code, w.Code, w.Body.Bytes()))
		if x.bib <= 0 || x.remove {
			continue
		}
		func() {
			mutex.Lock()
			defer mutex.Unlock()
			t.Equal(results[x.place-1].Confirmed, x.confirmed, fmt.Sprintf("Iteration - %d, Result %v confirmed != %v", i, results[x.place-1], x.confirmed))
			t.Equal(bibbedEntries[x.bib].Result, results[x.place-1], fmt.Sprintf("Iteration - %d, Entry %v is not in place %d", i, bibbedEntries[x.bib], x.place))
			t.Equal(int(bibbedEntries[x.bib].Result.Place), x.place, fmt.Sprintf("Iteration - %d, Bib %d is not in place %d but place %d!", i, x.bib, x.place, bibbedEntries[x.bib].Result.Place))
		}()
	}
}

func (t *testSuite) TestPrizes() {
	startRace()
	// race is started, load the racers
	req, err := uploadFile("test_prizes.json")
	t.Nil(err)
	t.Not(t.Nil(req))
	w := httptest.NewRecorder()
	uploadPrizes(w, req)
	t.Equal(w.Code, 301)
	mutex.Lock()
	t.Equal(len(prizes), 26)
	mutex.Unlock()

	req, err = uploadFile("test_runners_prizes.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	for x := 1; x <= len(bibbedEntries); x++ {
		req, err = http.NewRequest("post", "", nil)
		req.ParseForm()
		req.Form.Set("bib", strconv.Itoa(x))
		t.Nil(err)
		w = httptest.NewRecorder()
		linkBib(w, req)
		t.Equal(w.Code, 301)
		w = httptest.NewRecorder()
		linkBib(w, req)
		t.Equal(w.Code, 301)
	}
	mutex.Lock()
	t.Equal(len(prizes[0].Winners), 1) // men's overall
	t.Equal(prizes[0].Winners[0], results[0])
	t.Equal(len(prizes[2].Winners), 1) // men's u10
	t.Equal(prizes[2].Winners[0], results[6])
	t.Equal(len(prizes[4].Winners), 0)  // men's 11-15
	t.Equal(len(prizes[6].Winners), 0)  // men's 16-20
	t.Equal(len(prizes[8].Winners), 0)  // men's 21-25
	t.Equal(len(prizes[10].Winners), 0) // men's 26-30
	t.Equal(len(prizes[12].Winners), 0) // men's 31-35
	t.Equal(len(prizes[14].Winners), 1) // men's 36-40
	t.Equal(prizes[14].Winners[0], results[1])
	t.Equal(len(prizes[16].Winners), 0) // men's 41-45
	t.Equal(len(prizes[18].Winners), 0) // men's 46-50
	t.Equal(len(prizes[20].Winners), 2) // men's 51-55
	t.Equal(prizes[20].Winners[0], results[3])
	t.Equal(prizes[20].Winners[1], results[4])
	//t.Equal(prizes[20].Winners[1], results[5]) // he didn't place
	t.Equal(len(prizes[22].Winners), 0) // men's 56-60
	t.Equal(len(prizes[24].Winners), 0) // men's 61+

	t.Equal(len(prizes[1].Winners), 1) // women's overall
	t.Equal(prizes[1].Winners[0], results[2])
	t.Equal(len(prizes[3].Winners), 1) // women's u10
	t.Equal(prizes[3].Winners[0], results[7])
	t.Equal(len(prizes[5].Winners), 0)  // women's 11-15
	t.Equal(len(prizes[7].Winners), 0)  // women's 16-20
	t.Equal(len(prizes[9].Winners), 0)  // women's 21-25
	t.Equal(len(prizes[11].Winners), 0) // women's 26-30
	t.Equal(len(prizes[13].Winners), 0) // women's 31-35
	t.Equal(len(prizes[15].Winners), 0) // women's 36-40
	t.Equal(len(prizes[17].Winners), 0) // women's 41-45
	t.Equal(len(prizes[19].Winners), 0) // women's 46-50
	t.Equal(len(prizes[21].Winners), 0) // women's 51-55
	t.Equal(len(prizes[23].Winners), 0) // women's 56-60
	t.Equal(len(prizes[25].Winners), 0) // women's 61+
	mutex.Unlock()
}

func (t *testSuite) TestHumanDuration() {
	duration := HumanDuration(time.Second * 120)
	t.Equal(duration.String(), "00:02:00.00")
	t.Equal(duration.Clock(), "00:02:00")
	duration = HumanDuration(time.Second * 0)
	t.Equal(duration.String(), "00:00:00.00")
	t.Equal(duration.Clock(), "00:00:00")
	duration = HumanDuration(time.Hour)
	t.Equal(duration.String(), "01:00:00.00")
	t.Equal(duration.Clock(), "01:00:00")
	duration = HumanDuration(time.Second * 5)
	t.Equal(duration.String(), "00:00:05.00")
	t.Equal(duration.Clock(), "00:00:05")
	duration = HumanDuration(time.Second * 50)
	t.Equal(duration.String(), "00:00:50.00")
	t.Equal(duration.Clock(), "00:00:50")
	duration = HumanDuration(time.Hour + time.Minute*45 + time.Second*5)
	t.Equal(duration.String(), "01:45:05.00")
	t.Equal(duration.Clock(), "01:45:05")
	duration = HumanDuration(time.Hour + time.Minute*45 + time.Second*5 + time.Millisecond*104)
	t.Equal(duration.String(), "01:45:05.10")
	t.Equal(duration.Clock(), "01:45:05")
	duration = HumanDuration(time.Hour + time.Minute*45 + time.Second*5 + time.Millisecond*907)
	t.Equal(duration.String(), "01:45:05.91")
	t.Equal(duration.Clock(), "01:45:05")
}
