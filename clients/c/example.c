/*
 * example.c — minimal end-to-end demo of the cib_aes v2 C client.
 *
 *   ./cib-example \
 *       --base    http://box.example.com:8080 \
 *       --key-id  abc123 \
 *       --secret  <64-char hex> \
 *       --session <session uuid>
 *
 * Flow:
 *   1. GET /aes/time to sync the clock.
 *   2. POST /aes/sessions/<id>/input with `{"data":"hello\n"}` (one-shot).
 *   3. POST /aes/sessions/<id>/events/stream and print every frame the
 *      server emits until either the stream's wait_ms elapses or we see
 *      a `stop` frame.
 *
 * The streaming reader uses a callback that fires once per record. The
 * device only ever holds one record's worth of plaintext in RAM, which
 * is the whole point of the v2 protocol.
 */

#include "cib_aes.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <curl/curl.h>

static int parse_hex_secret(const char *s, uint8_t out[32]) {
    if (!s || strlen(s) != 64) return -1;
    for (int i = 0; i < 32; i++) {
        unsigned int v;
        if (sscanf(s + 2*i, "%2x", &v) != 1) return -1;
        out[i] = (uint8_t)v;
    }
    return 0;
}

static int g_saw_stop = 0;

static cib_status print_record(uint8_t type, const uint8_t *payload, size_t len, void *ud) {
    (void)ud;
    switch (type) {
    case CIB_TYPE_FRAME:
        fwrite(payload, 1, len, stdout);
        fputc('\n', stdout);
        fflush(stdout);
        /* crude: stop the stream when a `"kind":"stop"` substring shows
         * up. A real client parses the JSON. */
        if (memmem(payload, len, "\"kind\":\"stop\"", 13)) g_saw_stop = 1;
        if (g_saw_stop) return CIB_ERR_ABORTED;
        break;
    case CIB_TYPE_HEARTBEAT:
        fprintf(stderr, "[hb]\n");
        break;
    case CIB_TYPE_STREAM_END:
        fprintf(stderr, "[stream_end %.*s]\n", (int)len, (const char*)payload);
        break;
    case CIB_TYPE_JSON:
        fwrite(payload, 1, len, stdout);
        break;
    default:
        fprintf(stderr, "[unknown type 0x%02x len=%zu]\n", type, len);
    }
    return CIB_OK;
}

int main(int argc, char **argv) {
    const char *base = NULL, *key_id = NULL, *secret_hex = NULL, *sess = NULL;
    for (int i = 1; i + 1 < argc; i += 2) {
        if      (strcmp(argv[i], "--base")    == 0) base       = argv[i+1];
        else if (strcmp(argv[i], "--key-id")  == 0) key_id     = argv[i+1];
        else if (strcmp(argv[i], "--secret")  == 0) secret_hex = argv[i+1];
        else if (strcmp(argv[i], "--session") == 0) sess       = argv[i+1];
    }
    if (!base || !key_id || !secret_hex || !sess) {
        fprintf(stderr,
            "usage: %s --base http://host:port --key-id <id> "
            "--secret <64-hex> --session <uuid>\n", argv[0]);
        return 2;
    }

    uint8_t secret[32];
    if (parse_hex_secret(secret_hex, secret) != 0) {
        fprintf(stderr, "secret must be exactly 64 hex characters\n");
        return 2;
    }

    curl_global_init(CURL_GLOBAL_DEFAULT);

    cib_client *c = NULL;
    cib_status st = cib_client_new(base, key_id, secret, &c);
    if (st != CIB_OK) { fprintf(stderr, "cib_client_new: %d\n", st); return 1; }

    /* 1. Bootstrap: clock. */
    int64_t now_ms = 0, tol_ms = 0;
    st = cib_get_time(c, &now_ms, &tol_ms);
    if (st != CIB_OK) {
        fprintf(stderr, "cib_get_time: %d\n", st);
        goto done;
    }
    fprintf(stderr, "[box] server_now=%lld tolerance_ms=%lld\n",
            (long long)now_ms, (long long)tol_ms);

    /* 2. Send a line of input (one-shot). */
    const char *line = "{\"data\":\"hello from the C client\\n\"}";
    char route_in[256];
    snprintf(route_in, sizeof(route_in), "/aes/sessions/%s/input", sess);
    uint8_t resp[8192];
    size_t  resp_len = 0;
    st = cib_call_oneshot(c, route_in,
                          (const uint8_t*)line, strlen(line),
                          resp, sizeof(resp) - 1, &resp_len);
    if (st != CIB_OK) {
        fprintf(stderr, "cib_call_oneshot(input): %d\n", st);
        goto done;
    }
    resp[resp_len] = '\0';
    fprintf(stderr, "[box] input ack: %s\n", (char*)resp);

    /* 3. Stream events. wait_ms=10s; the callback aborts on stop frame. */
    const char *stream_req = "{\"from\":0,\"wait_ms\":10000,\"idle_hb_ms\":2000}";
    char route_stream[256];
    snprintf(route_stream, sizeof(route_stream), "/aes/sessions/%s/events/stream", sess);
    st = cib_stream(c, route_stream,
                    (const uint8_t*)stream_req, strlen(stream_req),
                    print_record, NULL);
    if (st != CIB_OK && st != CIB_ERR_ABORTED) {
        fprintf(stderr, "cib_stream: %d\n", st);
        goto done;
    }

done:
    cib_client_free(c);
    curl_global_cleanup();
    return st == CIB_OK || st == CIB_ERR_ABORTED ? 0 : 1;
}
