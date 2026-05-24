/*
 * Hazy Flow C SDK — module-side stdio protocol.
 *
 * A native C module links libhazyflow.a and calls hz_run() with a populated
 * hz_module_t. The library handles the handshake/execute message loop on
 * stdin/stdout and dispatches "execute" messages to the provided callback.
 *
 * Threading: hz_run blocks the calling thread. Modules should not write to
 * stdout themselves — use hz_progress() and the hz_result_* builders.
 */
#ifndef HAZYFLOW_MODULE_H
#define HAZYFLOW_MODULE_H

#ifdef __cplusplus
extern "C" {
#endif

typedef struct hz_job    hz_job_t;
typedef struct hz_result hz_result_t;

typedef hz_result_t* (*hz_execute_fn)(hz_job_t* job);

typedef struct {
    const char*   id;
    const char*   version;
    const char*   manifest_json; /* full Manifest JSON sent verbatim on hello */
    hz_execute_fn execute;
} hz_module_t;

/* Run the protocol loop. Blocks until stdin closes. Exit code 0 on clean
 * shutdown, non-zero on protocol error. */
int hz_run(const hz_module_t* module);

/* Emit a progress event mid-execute. percent is 0.0..1.0; pass a negative
 * value to omit it. message may be NULL. */
void hz_progress(hz_job_t* job, double percent, const char* message);

/* Read an input ref by port name. Returns NULL if absent. The returned
 * pointer is valid until the execute callback returns. */
const char* hz_input_ref(hz_job_t* job, const char* port);
const char* hz_input_mime(hz_job_t* job, const char* port);

/* Read params by key. Strings return NULL when missing or non-string;
 * integers return the default when missing or non-numeric. */
const char* hz_param_str(hz_job_t* job, const char* key);
long        hz_param_int(hz_job_t* job, const char* key, long default_val);

/* Result builders. Ownership passes to hz_run on return — do not free. */
hz_result_t* hz_result_ok(const char* port, const char* ref, const char* mime);
hz_result_t* hz_result_error(const char* code, const char* message);

#ifdef __cplusplus
}
#endif

#endif /* HAZYFLOW_MODULE_H */
