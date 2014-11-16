package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func startRace() {
	reset()
	r, _ := http.NewRequest("get", "/start", nil)
	w := httptest.NewRecorder()
	startHandler(w, r)
}

func TestLoadRacers(t *testing.T) {
	startRace()
	// race is started, load the racers
	req, err := uploadFile("test_runners.csv")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w := httptest.NewRecorder()
	uploadRacers(w, req)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}
	mutex.Lock()
	if len(bibbedEntries) != 8 {
		t.Errorf("Expected 8 bibbed entries, got %d", len(bibbedEntries))
	}
	if len(unbibbedEntries) != 0 {
		t.Errorf("Expected 0 unbibbed entries, got %d", len(unbibbedEntries))
	}
	if bibbedEntries[4].Fname != "G" || bibbedEntries[4].Lname != "H" {
		t.Errorf("Expected G H as 4th indexed entry, got %s %s", bibbedEntries[4].Fname, bibbedEntries[4].Lname)
	}
	mutex.Unlock()

	req, err = uploadFile("test_runners2.csv")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}
	mutex.Lock()
	if len(bibbedEntries) != 4 {
		t.Errorf("Expected 4 bibbed entries, got %d", len(bibbedEntries))
	}
	if len(unbibbedEntries) != 4 {
		t.Errorf("Expected 4 unbibbed entries, got %d", len(unbibbedEntries))
	}
	mutex.Unlock()
	req, err = uploadFile("test_runners3.csv")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}
	mutex.Lock()
	if len(bibbedEntries) != 0 {
		t.Errorf("Expected 0 bibbed entries, got %d", len(bibbedEntries))
	}
	if len(unbibbedEntries) != 8 {
		t.Errorf("Expected 8 unbibbed entries, got %d", len(unbibbedEntries))
	}
	mutex.Unlock()
}

func TestLink(t *testing.T) { // includes removing of racers
	startRace()
	// race is started, load the racers
	req, err := uploadFile("test_runners.csv")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w := httptest.NewRecorder()
	uploadRacers(w, req)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}
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
		if err != nil {
			t.Errorf("Unexpected error - %v", err)
		}
		w := httptest.NewRecorder()
		linkBib(w, req)
		if x.code != w.Code {
			t.Errorf("Iteration - %d, Expected %d, got %d - %s", i, x.code, w.Code, w.Body.Bytes())
		}
		if x.bib <= 0 || x.remove {
			continue
		}
		func() {
			mutex.Lock()
			defer mutex.Unlock()
			if results[x.place-1].Confirmed != x.confirmed {
				t.Errorf("Iteration - %d, Result %v confirmed != %v", i, results[x.place-1], x.confirmed)
			}
			if bibbedEntries[x.bib].Result != results[x.place-1] {
				t.Errorf("Iteration - %d, Entry %v is not in place %d", i, bibbedEntries[x.bib], x.place)
			}
			if int(bibbedEntries[x.bib].Result.Place) != x.place {
				t.Errorf("Iteration - %d, Bib %d is not in place %d but place %d!", i, x.bib, x.place, bibbedEntries[x.bib].Result.Place)
			}
		}()
	}
}

func TestPrizes(t *testing.T) {
	startRace()
	// race is started, load the racers
	req, err := uploadFile("test_prizes.json")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w := httptest.NewRecorder()
	uploadPrizes(w, req)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}
	mutex.Lock()
	if len(prizes) != 26 {
		t.Errorf("Expected 26 prizes, got %d", len(prizes))
	}
	mutex.Unlock()

	req, err = uploadFile("test_runners_prizes.csv")
	if err != nil {
		t.Errorf("Unexpected error - %v", err)
	}
	if req == nil {
		t.Fatalf("Unexpected nil request")
	}
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	if w.Code != 301 {
		t.Errorf("Expected redirect, got %d", w.Code)
	}
	for x := 1; x <= len(bibbedEntries); x++ {
		req, err = http.NewRequest("post", "", nil)
		if err != nil {
			t.Errorf("Unexpected error - %v", err)
		}
		if req == nil {
			t.Fatalf("Unexpected nil request")
		}
		req.ParseForm()
		req.Form.Set("bib", strconv.Itoa(x))
		w = httptest.NewRecorder()
		linkBib(w, req)
		if w.Code != 301 {
			t.Errorf("Expected redirect, got %d", w.Code)
		}
		w = httptest.NewRecorder()
		linkBib(w, req)
		if w.Code != 301 {
			t.Errorf("Expected redirect, got %d", w.Code)
		}
	}
	mutex.Lock()
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
	mutex.Unlock()
}

func EqualInt(t *testing.T, got, expected int) {
	if got != expected {
		t.Errorf("Expected %d, got %d", expected, got)
	}
}

func EqualResult(t *testing.T, got, expected *Result) {
	if got != expected {
		t.Errorf("Expected %v, got %v", expected, got)
	}
}

func EqualString(t *testing.T, got, expected string) {
	if got != expected {
		t.Errorf("Expected %s, got %s",expected, got)
	}
}

func TestHumanDuration(t *testing.T) {
	duration := HumanDuration(time.Second * 120)
	EqualString(t,duration.String(), "00:02:00.00")
	EqualString(t,duration.Clock(), "00:02:00")
	duration = HumanDuration(time.Second * 0)
	EqualString(t,duration.String(), "00:00:00.00")
	EqualString(t,duration.Clock(), "00:00:00")
	duration = HumanDuration(time.Hour)
	EqualString(t,duration.String(), "01:00:00.00")
	EqualString(t,duration.Clock(), "01:00:00")
	duration = HumanDuration(time.Second * 5)
	EqualString(t,duration.String(), "00:00:05.00")
	EqualString(t,duration.Clock(), "00:00:05")
	duration = HumanDuration(time.Second * 50)
	EqualString(t,duration.String(), "00:00:50.00")
	EqualString(t,duration.Clock(), "00:00:50")
	duration = HumanDuration(time.Hour + time.Minute*45 + time.Second*5)
	EqualString(t,duration.String(), "01:45:05.00")
	EqualString(t,duration.Clock(), "01:45:05")
	duration = HumanDuration(time.Hour + time.Minute*45 + time.Second*5 + time.Millisecond*104)
	EqualString(t,duration.String(), "01:45:05.10")
	EqualString(t,duration.Clock(), "01:45:05")
	duration = HumanDuration(time.Hour + time.Minute*45 + time.Second*5 + time.Millisecond*907)
	EqualString(t,duration.String(), "01:45:05.91")
	EqualString(t,duration.Clock(), "01:45:05")
}
