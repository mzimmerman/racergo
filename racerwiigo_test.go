package main

import (
	"github.com/remogatto/prettytest"
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
