#include "json.h"

#include <ctype.h>
#include <stdlib.h>
#include <string.h>

/* ---------------------------------------------------------------- writers */

void hz_json_write_escaped(FILE* f, const char* s) {
    if (s == NULL) {
        fputs("null", f);
        return;
    }
    fputc('"', f);
    for (const char* p = s; *p; p++) {
        unsigned char c = (unsigned char)*p;
        switch (c) {
            case '"':  fputs("\\\"", f); break;
            case '\\': fputs("\\\\", f); break;
            case '\b': fputs("\\b",  f); break;
            case '\f': fputs("\\f",  f); break;
            case '\n': fputs("\\n",  f); break;
            case '\r': fputs("\\r",  f); break;
            case '\t': fputs("\\t",  f); break;
            default:
                if (c < 0x20) {
                    fprintf(f, "\\u%04x", c);
                } else {
                    fputc(c, f);
                }
        }
    }
    fputc('"', f);
}

static void write_sep(FILE* f, int* first) {
    if (*first) {
        *first = 0;
    } else {
        fputc(',', f);
    }
}

void hz_json_write_kv_str(FILE* f, const char* key, const char* value, int* first) {
    write_sep(f, first);
    hz_json_write_escaped(f, key);
    fputc(':', f);
    hz_json_write_escaped(f, value);
}

void hz_json_write_kv_int(FILE* f, const char* key, long value, int* first) {
    write_sep(f, first);
    hz_json_write_escaped(f, key);
    fprintf(f, ":%ld", value);
}

void hz_json_write_kv_double(FILE* f, const char* key, double value, int* first) {
    write_sep(f, first);
    hz_json_write_escaped(f, key);
    fprintf(f, ":%g", value);
}

void hz_json_write_kv_raw(FILE* f, const char* key, const char* raw, int* first) {
    write_sep(f, first);
    hz_json_write_escaped(f, key);
    fputc(':', f);
    fputs(raw, f);
}

/* ----------------------------------------------------------------- parser */

typedef struct {
    const char* src;
    size_t      pos;
    size_t      len;
} hz_parser;

static void table_push(hz_json_table* t, const char* key, const char* value) {
    if (t->len >= t->cap) {
        int newcap = t->cap == 0 ? 16 : t->cap * 2;
        t->keys   = realloc(t->keys,   sizeof(char*) * newcap);
        t->values = realloc(t->values, sizeof(char*) * newcap);
        t->cap = newcap;
    }
    t->keys[t->len]   = strdup(key);
    t->values[t->len] = strdup(value);
    t->len++;
}

static void skip_ws(hz_parser* p) {
    while (p->pos < p->len && isspace((unsigned char)p->src[p->pos])) {
        p->pos++;
    }
}

static int peek(hz_parser* p) {
    skip_ws(p);
    if (p->pos >= p->len) return -1;
    return (unsigned char)p->src[p->pos];
}

static int expect(hz_parser* p, char c) {
    if (peek(p) != c) return -1;
    p->pos++;
    return 0;
}

/* Reads a JSON string into a freshly malloc'd buffer (caller frees). */
static char* parse_string(hz_parser* p) {
    if (peek(p) != '"') return NULL;
    p->pos++;
    size_t start = p->pos;
    size_t out_cap = 64, out_len = 0;
    char*  out = malloc(out_cap);
    while (p->pos < p->len) {
        unsigned char c = (unsigned char)p->src[p->pos];
        if (c == '"') {
            p->pos++;
            out[out_len] = '\0';
            return out;
        }
        if (c == '\\') {
            p->pos++;
            if (p->pos >= p->len) break;
            char e = p->src[p->pos++];
            char dec;
            switch (e) {
                case '"': dec = '"';  break;
                case '\\':dec = '\\'; break;
                case '/': dec = '/';  break;
                case 'b': dec = '\b'; break;
                case 'f': dec = '\f'; break;
                case 'n': dec = '\n'; break;
                case 'r': dec = '\r'; break;
                case 't': dec = '\t'; break;
                default:  dec = e;
            }
            if (out_len + 1 >= out_cap) { out_cap *= 2; out = realloc(out, out_cap); }
            out[out_len++] = dec;
            continue;
        }
        if (out_len + 1 >= out_cap) { out_cap *= 2; out = realloc(out, out_cap); }
        out[out_len++] = (char)c;
        p->pos++;
    }
    (void)start;
    free(out);
    return NULL;
}

/* Read primitive value as raw text (number/bool/null/string). For strings
 * the surrounding quotes are stripped. */
static char* parse_value_as_text(hz_parser* p) {
    int c = peek(p);
    if (c < 0) return NULL;
    if (c == '"') return parse_string(p);
    size_t start = p->pos;
    while (p->pos < p->len) {
        char ch = p->src[p->pos];
        if (ch == ',' || ch == '}' || ch == ']' || isspace((unsigned char)ch)) break;
        p->pos++;
    }
    size_t n = p->pos - start;
    char* out = malloc(n + 1);
    memcpy(out, p->src + start, n);
    out[n] = '\0';
    return out;
}

static int parse_object_recursive(hz_parser* p, const char* prefix, hz_json_table* t);
static int parse_array_recursive(hz_parser* p, const char* prefix, hz_json_table* t);

static char* path_join(const char* prefix, const char* key) {
    if (prefix == NULL || prefix[0] == '\0') return strdup(key);
    size_t n = strlen(prefix) + 1 + strlen(key) + 1;
    char* out = malloc(n);
    snprintf(out, n, "%s.%s", prefix, key);
    return out;
}

static int parse_member(hz_parser* p, const char* prefix, hz_json_table* t) {
    char* key = parse_string(p);
    if (key == NULL) return -1;
    if (expect(p, ':') < 0) { free(key); return -1; }

    char* full = path_join(prefix, key);
    free(key);

    int c = peek(p);
    int rc = 0;
    if (c == '{') {
        rc = parse_object_recursive(p, full, t);
    } else if (c == '[') {
        rc = parse_array_recursive(p, full, t);
    } else {
        char* val = parse_value_as_text(p);
        if (val == NULL) { rc = -1; }
        else {
            table_push(t, full, val);
            free(val);
        }
    }
    free(full);
    return rc;
}

static int parse_object_recursive(hz_parser* p, const char* prefix, hz_json_table* t) {
    if (expect(p, '{') < 0) return -1;
    if (peek(p) == '}') { p->pos++; return 0; }
    while (1) {
        if (parse_member(p, prefix, t) < 0) return -1;
        int c = peek(p);
        if (c == ',') { p->pos++; continue; }
        if (c == '}') { p->pos++; return 0; }
        return -1;
    }
}

static int parse_array_recursive(hz_parser* p, const char* prefix, hz_json_table* t) {
    if (expect(p, '[') < 0) return -1;
    if (peek(p) == ']') { p->pos++; return 0; }
    int idx = 0;
    while (1) {
        char idx_str[32];
        snprintf(idx_str, sizeof(idx_str), "%d", idx);
        char* full = path_join(prefix, idx_str);
        int c = peek(p);
        int rc = 0;
        if (c == '{') {
            rc = parse_object_recursive(p, full, t);
        } else if (c == '[') {
            rc = parse_array_recursive(p, full, t);
        } else {
            char* val = parse_value_as_text(p);
            if (val == NULL) { rc = -1; }
            else { table_push(t, full, val); free(val); }
        }
        free(full);
        if (rc < 0) return -1;
        idx++;
        c = peek(p);
        if (c == ',') { p->pos++; continue; }
        if (c == ']') { p->pos++; return 0; }
        return -1;
    }
}

int hz_json_parse_object(const char* src, hz_json_table* out) {
    memset(out, 0, sizeof(*out));
    hz_parser p = { .src = src, .pos = 0, .len = strlen(src) };
    return parse_object_recursive(&p, "", out);
}

const char* hz_json_table_get(const hz_json_table* t, const char* key) {
    for (int i = 0; i < t->len; i++) {
        if (strcmp(t->keys[i], key) == 0) return t->values[i];
    }
    return NULL;
}

void hz_json_table_free(hz_json_table* t) {
    for (int i = 0; i < t->len; i++) {
        free(t->keys[i]);
        free(t->values[i]);
    }
    free(t->keys);
    free(t->values);
    memset(t, 0, sizeof(*t));
}
