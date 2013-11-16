package main

import (
	"github.com/remogatto/prettytest"
	"net/http"
	"net/http/httptest"
	"runtime"
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

func startRace() chan bool {
	useWiimote = false
	ready := make(chan bool)
	go raceFunc(ready)
	<-ready                 // ready to process
	racerChan <- time.Now() // start the race
	return ready
}

func stopRace(ready chan bool) {
	statusChan <- Finished
	<-ready
	for {
		select {
		// need to empty all unprocessed actions left over from test to properly clean up
		case <-racerChan:
		case <-statusChan:
		default:
			return
		}
	}
}

func (t *testSuite) TestLoadRacers() {
	defer stopRace(startRace())
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

func (t *testSuite) TestRemoveRacer() {
	defer stopRace(startRace())
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
	now := time.Now()
	mutex.Lock()
	for x := 0; x < len(bibbedEntries); x++ {
		now = now.Add(time.Second)
		racerChan <- now // have everyone cross the line with different times
	}
	mutex.Unlock()
	for {
		mutex.Lock()
		if len(results) >= len(bibbedEntries) {
			mutex.Unlock()
			break
		}
		mutex.Unlock()
		runtime.Gosched()
	}
	// 8 racers, test the beginning, middle, and end
	mutex.Lock()
	tableTests := []struct {
		place  int
		code   int
		length int
		victim *Result
	}{
		{0, 409, 8, nil},
		{1, 301, 7, results[0]},
		{3, 301, 6, results[3]},
		{6, 301, 5, results[7]},
	}
	mutex.Unlock()
	for _, x := range tableTests {
		req, err := http.NewRequest("post", "", nil)
		req.ParseForm()
		req.Form.Set("place", strconv.Itoa(x.place))
		t.Nil(err)
		w := httptest.NewRecorder()
		mutex.Lock()
		if x.place != 0 {
			t.Equal(results[x.place-1], x.victim)
		}
		mutex.Unlock()
		removeRacer(w, req)
		t.Equal(w.Code, x.code)
		mutex.Lock()
		t.Equal(len(results), x.length)
		mutex.Unlock()
	}
}

func (t *testSuite) TestPrizes() {
	defer stopRace(startRace())
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
	now := time.Now()
	mutex.Lock()
	for x := 0; x < len(bibbedEntries); x++ {
		now = now.Add(time.Second)
		racerChan <- now // have everyone cross the line with different times
	}
	mutex.Unlock()
	bibslength := 0
	for {
		mutex.Lock()
		if len(results) >= len(bibbedEntries) {
			bibslength = len(bibbedEntries)
			mutex.Unlock()
			break
		}
		mutex.Unlock()
		runtime.Gosched()
	}
	for x := 1; x <= bibslength; x++ {
		req, err = http.NewRequest("post", "", nil)
		req.ParseForm()
		req.Form.Set("next", strconv.Itoa(x))
		req.Form.Set("bib", strconv.Itoa(x))
		t.Nil(err)
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
