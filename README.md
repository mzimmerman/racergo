racerwiigo
==========

An implementation of RacerWii (http://github.com/mzimmerman/racerwii) but in Go with many additional features including:
* Calculate Prizes (e.g. Overall Male, Female Under 15, etc)
* Web interface for entering and displaying results to racers/spectators (http://raceresults/admin & http://raceresults/ respectively)
* Meant to be used with a hotspot configuration as all requests get directed to hostname raceresults
* Download race results in a combined CSV (Entries (names, tshirt size, etc) & Results (time, overall place))
* Remove extraneous entries (if the Wiimote is pressed accidentally) through the admin interface (The Virgil feature)
* Entering Bib # information for each racer as they cross the line

Features ported from RacerWii include:
* Displaying race time(s) (now you can display it on any device by pulling up the web interface)
* Starting the race with the Wiimote A button
* Recording racer times with the Wiimote A button

RacerWii helps track racers on long distance races like a 5k.  It's meant to be a high-tech low cost way of creating and displaying race results.  It's primary use case is for fund raiser type 
races where buying RFID chips for each runner is over the top and too costly.


[![Build Status](https://drone.io/github.com/mzimmerman/racerwiigo/status.png)](https://drone.io/github.com/mzimmerman/racerwiigo/latest)
