//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
#include "bridge.h"
#include <piper.h>
#include <exception>
#include <cstring>

// libpiper's C API can throw for missing or malformed model/config files.
// C++ exceptions must never unwind across the cgo boundary.
void *qs_create_piper(const char *voice, const char *config, const char *espeak, char **error) {
    try {
        return piper_create(voice, config, espeak);
    } catch (const std::exception &e) {
        *error = strdup(e.what());
    } catch (...) {
        *error = strdup("unknown Piper model load error");
    }
    return nullptr;
}
