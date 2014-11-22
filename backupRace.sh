#!/bin/sh

mkdir -p raceResults
while true
do
	wget http://campuslife5k/download -O "raceResults/backupResults-`date`.csv"
	sleep 30
done
