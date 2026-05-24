/*
 * Example native C module that echoes its input ref back unchanged.
 * Build via the SDK Makefile:
 *    cd sdk/c && make examples/echo
 */
#include "hazyflow/module.h"

#include <stdio.h>
#include <stdlib.h>

static const char* MANIFEST =
    "{"
        "\"id\":\"echo\","
        "\"version\":\"1.0\","
        "\"label\":\"Echo\","
        "\"execution_model\":\"batch\","
        "\"process_model\":\"long_lived\","
        "\"inputs\":[{\"port\":\"in\",\"required\":true}],"
        "\"outputs\":[{\"port\":\"out\"}],"
        "\"idempotent\":true"
    "}";

static hz_result_t* execute(hz_job_t* job) {
    const char* ref  = hz_input_ref(job,  "in");
    const char* mime = hz_input_mime(job, "in");
    hz_progress(job, 0.5, "echoing");
    if (ref == NULL) {
        return hz_result_error("missing_input", "port 'in' has no ref");
    }
    return hz_result_ok("out", ref, mime);
}

int main(void) {
    hz_module_t m = {
        .id            = "echo",
        .version       = "1.0",
        .manifest_json = MANIFEST,
        .execute       = execute,
    };
    return hz_run(&m);
}
