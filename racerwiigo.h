#ifndef RACERWIIGO_H
#define RACERWIIGO_H

#include <cwiid.h>

extern cwiid_mesg_callback_t* getCwiidCallback();
extern cwiid_err_t* getErrCallback();

#endif // RACERWIIGO_H