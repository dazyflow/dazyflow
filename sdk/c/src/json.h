/*
 * Minimal JSON helper used internally by the Hazy Flow C SDK.
 *
 * It supports exactly what the stdio protocol needs:
 *   - parsing an object into a flat key-value table keyed by dotted path
 *     (e.g. "input.in.ref", "params.ms")
 *   - writing escaped strings, numbers, booleans, and raw nested fragments
 *
 * It is deliberately not a general-purpose JSON library; production
 * deployments should swap in cJSON or similar before shipping modules.
 */
#ifndef HAZYFLOW_INTERNAL_JSON_H
#define HAZYFLOW_INTERNAL_JSON_H

#include <stdio.h>

typedef struct {
    char** keys;
    char** values;
    int    len;
    int    cap;
} hz_json_table;

/* Parse a JSON object. Returns 0 on success, -1 on malformed input.
 * Populates `out`; caller must hz_json_table_free when done. */
int hz_json_parse_object(const char* src, hz_json_table* out);

const char* hz_json_table_get(const hz_json_table* t, const char* key);
void        hz_json_table_free(hz_json_table* t);

/* Output helpers — write to a FILE*. */
void hz_json_write_escaped(FILE* f, const char* s);
void hz_json_write_kv_str(FILE* f, const char* key, const char* value, int* first);
void hz_json_write_kv_int(FILE* f, const char* key, long value, int* first);
void hz_json_write_kv_double(FILE* f, const char* key, double value, int* first);
void hz_json_write_kv_raw(FILE* f, const char* key, const char* raw, int* first);

#endif
