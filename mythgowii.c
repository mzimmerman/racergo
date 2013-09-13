#include "mythgowii.h"
#include "_cgo_export.h"

cwiid_mesg_callback_t* getCwiidCallback() {
        return (cwiid_mesg_callback_t*)goCwiidCallback;
}

cwiid_err_t* getErrCallback() {
	return (cwiid_err_t*)goErrCallback;
}