/*
 * Hazy Flow C SDK — stdio protocol implementation.
 *
 * Protocol (newline-delimited JSON, one message per line):
 *   ← hello      {"type":"hello","protocol":"1.0",...}
 *   → manifest   {"type":"manifest", ...verbatim manifest_json...}
 *   ← execute    {"type":"execute","job_id":"...","input":{...},"params":{...}}
 *   → progress   {"type":"progress","job_id":"...","percent":0.5,"message":"..."}
 *   → result     {"type":"result","job_id":"...","status":"ok","output":{...}}
 */
#include "hazyflow/module.h"
#include "json.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* ----------------------------------------------------------- public types */

struct hz_job {
    char*          job_id;
    hz_json_table  fields; /* dotted-path KV for input.*, params.* */
};

struct hz_result {
    int   ok;
    char* port;
    char* ref;
    char* mime;
    char* err_code;
    char* err_msg;
};

/* ------------------------------------------------------ result allocators */

hz_result_t* hz_result_ok(const char* port, const char* ref, const char* mime) {
    hz_result_t* r = calloc(1, sizeof(*r));
    r->ok   = 1;
    r->port = port ? strdup(port) : NULL;
    r->ref  = ref  ? strdup(ref)  : NULL;
    r->mime = mime ? strdup(mime) : NULL;
    return r;
}

hz_result_t* hz_result_error(const char* code, const char* message) {
    hz_result_t* r = calloc(1, sizeof(*r));
    r->ok       = 0;
    r->err_code = code    ? strdup(code)    : strdup("error");
    r->err_msg  = message ? strdup(message) : NULL;
    return r;
}

static void hz_result_free(hz_result_t* r) {
    if (!r) return;
    free(r->port);
    free(r->ref);
    free(r->mime);
    free(r->err_code);
    free(r->err_msg);
    free(r);
}

/* --------------------------------------------------------- job accessors */

const char* hz_input_ref(hz_job_t* job, const char* port) {
    char path[256];
    snprintf(path, sizeof(path), "input.%s.ref", port);
    return hz_json_table_get(&job->fields, path);
}

const char* hz_input_mime(hz_job_t* job, const char* port) {
    char path[256];
    snprintf(path, sizeof(path), "input.%s.mime", port);
    return hz_json_table_get(&job->fields, path);
}

const char* hz_param_str(hz_job_t* job, const char* key) {
    char path[256];
    snprintf(path, sizeof(path), "params.%s", key);
    return hz_json_table_get(&job->fields, path);
}

long hz_param_int(hz_job_t* job, const char* key, long default_val) {
    const char* s = hz_param_str(job, key);
    if (s == NULL) return default_val;
    char* end = NULL;
    long v = strtol(s, &end, 10);
    if (end == s) return default_val;
    return v;
}

/* ----------------------------------------------------------- progress out */

void hz_progress(hz_job_t* job, double percent, const char* message) {
    int first = 1;
    fputc('{', stdout);
    hz_json_write_kv_str(stdout, "type", "progress", &first);
    hz_json_write_kv_str(stdout, "job_id", job->job_id, &first);
    if (percent >= 0.0) {
        hz_json_write_kv_double(stdout, "percent", percent, &first);
    }
    if (message != NULL) {
        hz_json_write_kv_str(stdout, "message", message, &first);
    }
    fputs("}\n", stdout);
    fflush(stdout);
}

/* --------------------------------------------------------------- loop */

static void send_manifest(const hz_module_t* m) {
    int first = 1;
    fputc('{', stdout);
    hz_json_write_kv_str(stdout, "type", "manifest", &first);
    /* manifest_json is expected to be a complete object — strip outer braces
     * and append its body so we can co-locate the "type" tag. */
    if (m->manifest_json != NULL) {
        const char* s = m->manifest_json;
        while (*s && *s != '{') s++;
        if (*s == '{') s++;
        size_t n = strlen(s);
        while (n > 0 && (s[n - 1] == ' ' || s[n - 1] == '\n' || s[n - 1] == '\r')) n--;
        if (n > 0 && s[n - 1] == '}') n--;
        if (n > 0) {
            fputc(',', stdout);
            fwrite(s, 1, n, stdout);
        }
    }
    fputs("}\n", stdout);
    fflush(stdout);
}

static void send_result(hz_job_t* job, hz_result_t* r) {
    int first = 1;
    fputc('{', stdout);
    hz_json_write_kv_str(stdout, "type", "result", &first);
    hz_json_write_kv_str(stdout, "job_id", job->job_id, &first);

    if (r == NULL) {
        hz_json_write_kv_str(stdout, "status", "error", &first);
        hz_json_write_kv_raw(stdout, "error",
            "{\"code\":\"null_result\",\"message\":\"execute returned NULL\"}",
            &first);
    } else if (r->ok) {
        hz_json_write_kv_str(stdout, "status", "ok", &first);
        if (r->port != NULL) {
            fputs(",\"output\":{", stdout);
            hz_json_write_escaped(stdout, r->port);
            fputs(":{", stdout);
            int iFirst = 1;
            hz_json_write_kv_str(stdout, "mime",
                r->mime ? r->mime : "application/octet-stream", &iFirst);
            if (r->ref) hz_json_write_kv_str(stdout, "ref", r->ref, &iFirst);
            fputs("}}", stdout);
        }
    } else {
        hz_json_write_kv_str(stdout, "status", "error", &first);
        fputs(",\"error\":{", stdout);
        int eFirst = 1;
        hz_json_write_kv_str(stdout, "code",    r->err_code ? r->err_code : "error", &eFirst);
        hz_json_write_kv_str(stdout, "message", r->err_msg  ? r->err_msg  : "",      &eFirst);
        fputc('}', stdout);
    }
    fputs("}\n", stdout);
    fflush(stdout);
}

static char* read_line(FILE* f) {
    size_t cap = 256, len = 0;
    char* buf = malloc(cap);
    int c;
    while ((c = fgetc(f)) != EOF) {
        if (c == '\n') break;
        if (len + 1 >= cap) { cap *= 2; buf = realloc(buf, cap); }
        buf[len++] = (char)c;
    }
    if (c == EOF && len == 0) { free(buf); return NULL; }
    buf[len] = '\0';
    return buf;
}

int hz_run(const hz_module_t* module) {
    if (module == NULL || module->execute == NULL) return 2;

    char* line;
    while ((line = read_line(stdin)) != NULL) {
        hz_json_table tbl;
        if (hz_json_parse_object(line, &tbl) < 0) {
            free(line);
            continue;
        }
        const char* type = hz_json_table_get(&tbl, "type");
        if (type == NULL) {
            hz_json_table_free(&tbl);
            free(line);
            continue;
        }

        if (strcmp(type, "hello") == 0) {
            send_manifest(module);
        } else if (strcmp(type, "execute") == 0) {
            const char* job_id = hz_json_table_get(&tbl, "job_id");
            hz_job_t job = { .job_id = job_id ? strdup(job_id) : strdup(""),
                             .fields = tbl };
            tbl.keys = NULL; tbl.values = NULL; tbl.len = tbl.cap = 0; /* move */
            hz_result_t* r = module->execute(&job);
            send_result(&job, r);
            hz_result_free(r);
            hz_json_table_free(&job.fields);
            free(job.job_id);
        } else {
            /* Unknown message; ignore and continue. */
            hz_json_table_free(&tbl);
        }

        free(line);
    }
    return 0;
}
