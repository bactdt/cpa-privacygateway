//go:build cgo && (linux || darwin || freebsd)

package main

/*
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

extern int PrivacyGatewayPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);

static const char* privacygateway_shared_object_path() {
	Dl_info info;
	if (dladdr((void*)&PrivacyGatewayPluginCall, &info) == 0 || info.dli_fname == NULL) {
		return NULL;
	}
	return info.dli_fname;
}
*/
import "C"

func sharedLibraryPath() string {
	path := C.privacygateway_shared_object_path()
	if path == nil {
		return ""
	}
	return C.GoString(path)
}
