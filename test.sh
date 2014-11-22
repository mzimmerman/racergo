#!/bin/bash
export RACERGOHOSTNAME="campuslife5k"
#export RACERGOSENDGRIDUSER="mysendgridusername"
#export RACERGOSENDGRIDPASS="mysendgridpassword"
export RACERGORACENAME="2014 Campus Life 5k Orchard Run"
export RACERGOEMAILFIELD="Emailcurrentlydisabled"
export RACERGOFROMEMAIL="yfc@yfcmc.org"

# build the latest code
# run under sudo so we can listen to port 80, this could be addressed other ways, but this is easiest
go build && sudo -E ./racergo
