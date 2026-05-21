/*
 * example.c — minimal demo of the cib_aes C client.
 *
 *   ./cib-example \
 *       --base    http://box.example.com:8080 \
 *       --key-id  abc123 \
 *       --secret  <64-char hex> \
 *       --session <session uuid>
 *
 * The example walks the same end-to-end flow our Go integration tests
 * walk: fetch /aes/time, send one line of input, then long-poll for new
 * frames. Print whatever comes back.
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
    if (st != CIB_OK) {
        fprintf(stderr, "cib_client_new: %d\n", st);
        return 1;
    }

    /* 1. Bootstrap: fetch server time. */
    int64_t now_ms = 0, tol_ms = 0;
    st = cib_get_time(c, &now_ms, &tol_ms);
    if (st != CIB_OK) {
        fprintf(stderr, "cib_get_time: %d\n", st);
        cib_client_free(c);
        curl_global_cleanup();
        return 1;
    }
    fprintf(stderr, "[box] server_now=%lld tolerance_ms=%lld\n",
            (long long)now_ms, (long long)tol_ms);

    /* 2. Send one line of input. */
    const char *line = "{\"data\":\"hello from the C client\\n\"}";
    uint8_t resp[16 * 1024];
    size_t  resp_len = 0;
    st = cib_send_input(c, sess, (const uint8_t*)line, strlen(line),
                        resp, sizeof(resp) - 1, &resp_len);
    if (st != CIB_OK) {
        fprintf(stderr, "cib_send_input: %d\n", st);
        cib_client_free(c);
        curl_global_cleanup();
        return 1;
    }
    resp[resp_len] = '\0';
    fprintf(stderr, "[box] input ack: %s\n", (char*)resp);

    /* 3. Long-poll for events. The first call gets the buffered frames
     *    (or waits up to 5s for new ones). */
    const char *poll = "{\"from\":0,\"max\":32,\"wait_ms\":5000}";
    st = cib_poll_events(c, sess, (const uint8_t*)poll, strlen(poll),
                         resp, sizeof(resp) - 1, &resp_len);
    if (st != CIB_OK) {
        fprintf(stderr, "cib_poll_events: %d\n", st);
        cib_client_free(c);
        curl_global_cleanup();
        return 1;
    }
    resp[resp_len] = '\0';
    fprintf(stdout, "%s\n", (char*)resp);

    cib_client_free(c);
    curl_global_cleanup();
    return 0;
}
