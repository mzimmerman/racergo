package main

import (
	"bytes"
	"github.com/remogatto/prettytest"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
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
	useWiimote = false
	ready := make(chan bool)
	go raceFunc(ready)
	<-ready                 // ready to process
	racerChan <- time.Now() // start the race
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

func (t *testSuite) TestLoadRacers() {
	startRace()
	// race is started, load the racers
	req, err := uploadFile("test_runners.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w := httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	t.Equal(len(bibbedEntries), 8)
	t.Equal(len(unbibbedEntries), 0)

	req, err = uploadFile("test_runners2.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	t.Equal(len(bibbedEntries), 4)
	t.Equal(len(unbibbedEntries), 4)

	req, err = uploadFile("test_runners3.csv")
	t.Nil(err)
	t.Not(t.Nil(req))
	w = httptest.NewRecorder()
	uploadRacers(w, req)
	t.Equal(w.Code, 301)
	t.Equal(len(bibbedEntries), 0)
	t.Equal(len(unbibbedEntries), 8)
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
