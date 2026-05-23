/*
 * cib_aes.c — reference implementation of the v2 record-stream
 * envelope client. ~520 LOC including comments. Dependencies:
 *
 *   - mbedtls (gcm, ctr_drbg, entropy)
 *   - libcurl  (HTTP/1.1, chunked transfer)
 *
 * The wire format lives in docs/AES-TRANSPORT.md. The code below is
 * structured top-down (constants → crypto helpers → record codec →
 * one-shot wrapper → streaming wrapper → public API).
 */

#include "cib_aes.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <curl/curl.h>
#include <mbedtls/gcm.h>
#include <mbedtls/ctr_drbg.h>
#include <mbedtls/entropy.h>

/* ---- envelope constants -------------------------------------------------- */

#define CIB_ENVELOPE_VERSION   "2"
#define CIB_CONTENT_TYPE       "application/cib-stream-1"
#define CIB_AAD_PREFIX         "CIB2"
#define CIB_DIR_REQUEST        "REQUEST"
#define CIB_DIR_RESPONSE       "RESPONSE"

#define CIB_MAX_AAD            512
#define CIB_HEADER_BUF         128
#define CIB_MAX_ROUTE          256

/* ---- client ------------------------------------------------------------- */

struct cib_client {
    char    base_url[256];
    char    key_id[64];
    uint8_t secret[CIB_KEY_LEN];

    mbedtls_entropy_context  entropy;
    mbedtls_ctr_drbg_context drbg;
};

static void hex_encode(const uint8_t *in, size_t n, char *out) {
    static const char H[] = "0123456789abcdef";
    for (size_t i = 0; i < n; i++) {
        out[2*i]     = H[(in[i] >> 4) & 0xF];
        out[2*i + 1] = H[ in[i]       & 0xF];
    }
    out[2*n] = '\0';
}

static int hex_decode(const char *in, size_t n, uint8_t *out, size_t out_cap) {
    if (n & 1) return -1;
    if (n / 2 > out_cap) return -1;
    for (size_t i = 0; i < n / 2; i++) {
        int hi = -1, lo = -1;
        char a = in[2*i], b = in[2*i + 1];
        if      (a >= '0' && a <= '9') hi = a - '0';
        else if (a >= 'a' && a <= 'f') hi = a - 'a' + 10;
        else if (a >= 'A' && a <= 'F') hi = a - 'A' + 10;
        if      (b >= '0' && b <= '9') lo = b - '0';
        else if (b >= 'a' && b <= 'f') lo = b - 'a' + 10;
        else if (b >= 'A' && b <= 'F') lo = b - 'A' + 10;
        if (hi < 0 || lo < 0) return -1;
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return (int)(n / 2);
}

static int64_t now_unix_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec * 1000 + (int64_t)ts.tv_nsec / 1000000;
}

/* derive_nonce: nonce[0..8] = streamID[0..8]; nonce[8..12] = counter BE */
static void derive_nonce(const uint8_t stream_id[CIB_STREAM_ID_LEN],
                         uint32_t counter,
                         uint8_t out_nonce[CIB_NONCE_LEN]) {
    memcpy(out_nonce, stream_id, 8);
    out_nonce[8]  = (uint8_t)((counter >> 24) & 0xFF);
    out_nonce[9]  = (uint8_t)((counter >> 16) & 0xFF);
    out_nonce[10] = (uint8_t)((counter >>  8) & 0xFF);
    out_nonce[11] = (uint8_t)( counter        & 0xFF);
}

/* build_aad: CIB2\n<direction>\n<keyId>\n<route>\n<streamIDHex>\n<counter>\n */
static size_t build_aad(const char *direction,
                        const char *key_id,
                        const char *route,
                        const char *stream_id_hex,
                        uint32_t    counter,
                        uint8_t    *out, size_t out_cap) {
    int n = snprintf((char*)out, out_cap,
                     "%s\n%s\n%s\n%s\n%s\n%u\n",
                     CIB_AAD_PREFIX, direction, key_id, route,
                     stream_id_hex, (unsigned)counter);
    return (n < 0 || (size_t)n >= out_cap) ? 0 : (size_t)n;
}

/* gcm_seal: in-place AAD-bound GCM encryption. out_ct_plus_tag must
 * hold plain_len + CIB_TAG_LEN bytes. */
static int gcm_seal(const uint8_t *key,
                    const uint8_t  nonce[CIB_NONCE_LEN],
                    const uint8_t *aad, size_t aad_len,
                    const uint8_t *pt,  size_t pt_len,
                    uint8_t *out_ct_plus_tag) {
    mbedtls_gcm_context g;
    mbedtls_gcm_init(&g);
    int rc = mbedtls_gcm_setkey(&g, MBEDTLS_CIPHER_ID_AES, key, CIB_KEY_LEN * 8);
    if (rc == 0) {
        rc = mbedtls_gcm_crypt_and_tag(&g, MBEDTLS_GCM_ENCRYPT,
                                       pt_len, nonce, CIB_NONCE_LEN,
                                       aad, aad_len,
                                       pt, out_ct_plus_tag,
                                       CIB_TAG_LEN, out_ct_plus_tag + pt_len);
    }
    mbedtls_gcm_free(&g);
    return rc;
}

static int gcm_open(const uint8_t *key,
                    const uint8_t  nonce[CIB_NONCE_LEN],
                    const uint8_t *aad, size_t aad_len,
                    const uint8_t *ct_plus_tag, size_t total_len,
                    uint8_t *out_pt) {
    if (total_len < CIB_TAG_LEN) return -1;
    size_t pt_len = total_len - CIB_TAG_LEN;

    mbedtls_gcm_context g;
    mbedtls_gcm_init(&g);
    int rc = mbedtls_gcm_setkey(&g, MBEDTLS_CIPHER_ID_AES, key, CIB_KEY_LEN * 8);
    if (rc == 0) {
        rc = mbedtls_gcm_auth_decrypt(&g, pt_len, nonce, CIB_NONCE_LEN,
                                      aad, aad_len,
                                      ct_plus_tag + pt_len, CIB_TAG_LEN,
                                      ct_plus_tag, out_pt);
    }
    mbedtls_gcm_free(&g);
    return rc;
}

/* ---- inner frame helpers ------------------------------------------------- */

/* encode_inner builds [u8 type][u16 BE len][payload] in `out`. Returns
 * total bytes written, or -1 on overflow. */
static int encode_inner(uint8_t type, const uint8_t *payload, size_t len,
                        uint8_t *out, size_t out_cap) {
    if (len > CIB_MAX_RECORD_PLAIN - 3) return -1;
    if (out_cap < len + 3) return -1;
    out[0] = type;
    out[1] = (uint8_t)((len >> 8) & 0xFF);
    out[2] = (uint8_t)( len       & 0xFF);
    if (len) memcpy(out + 3, payload, len);
    return (int)(len + 3);
}

/* decode_inner unpacks the same layout from `plain` (which is
 * plain_len bytes). On success sets *out_type and *out_payload to
 * point into `plain`. */
static int decode_inner(const uint8_t *plain, size_t plain_len,
                        uint8_t *out_type,
                        const uint8_t **out_payload, size_t *out_payload_len) {
    if (plain_len < 3) return -1;
    uint16_t len = ((uint16_t)plain[1] << 8) | plain[2];
    if ((size_t)3 + len != plain_len) return -1;
    *out_type = plain[0];
    *out_payload = plain + 3;
    *out_payload_len = len;
    return 0;
}

/* ---- libcurl buffer adapter --------------------------------------------- */

struct dyn_buf {
    uint8_t *data;
    size_t   len;
    size_t   cap;
};

static size_t dyn_buf_write(void *ptr, size_t size, size_t nmemb, void *userdata) {
    struct dyn_buf *b = (struct dyn_buf*)userdata;
    size_t add = size * nmemb;
    if (b->len + add > b->cap) {
        size_t new_cap = b->cap ? b->cap * 2 : 4096;
        while (new_cap < b->len + add) new_cap *= 2;
        uint8_t *p = (uint8_t*)realloc(b->data, new_cap);
        if (!p) return 0;
        b->data = p;
        b->cap = new_cap;
    }
    memcpy(b->data + b->len, ptr, add);
    b->len += add;
    return add;
}

/* ---- response header parser --------------------------------------------- */

struct resp_hdrs {
    char    stream_id_hex[CIB_STREAM_ID_LEN*2 + 1];
    uint8_t stream_id[CIB_STREAM_ID_LEN];
    int64_t timestamp_ms;
    int     have_stream;
    int     have_ts;
};

static size_t parse_response_header(char *buf, size_t size, size_t nmemb, void *ud) {
    struct resp_hdrs *h = (struct resp_hdrs*)ud;
    size_t n = size * nmemb;
    if (n >= 14 && strncasecmp(buf, "Sec-CIB-Stream:", 15) == 0) {
        char *p = buf + 15;
        while (*p == ' ') p++;
        char *end = p;
        while (end < buf + n && *end != '\r' && *end != '\n') end++;
        size_t len = (size_t)(end - p);
        if (len < sizeof(h->stream_id_hex)) {
            memcpy(h->stream_id_hex, p, len);
            h->stream_id_hex[len] = '\0';
            if (hex_decode(h->stream_id_hex, len, h->stream_id, sizeof(h->stream_id)) == CIB_STREAM_ID_LEN) {
                h->have_stream = 1;
            }
        }
    } else if (n >= 17 && strncasecmp(buf, "Sec-CIB-Timestamp:", 18) == 0) {
        h->timestamp_ms = atoll(buf + 18);
        h->have_ts = 1;
    }
    return n;
}

/* ---- request construction ------------------------------------------------ */

struct req_state {
    /* The encrypted body the device sends. Built once before curl
     * starts; libcurl reads from this via CURLOPT_POSTFIELDS. */
    uint8_t req_stream_id[CIB_STREAM_ID_LEN];
    char    req_stream_hex[CIB_STREAM_ID_LEN*2 + 1];
    int64_t timestamp_ms;
    uint8_t *body;
    size_t   body_len;
};

/* build_oneshot_request seals req_json as one TypeJSON record + the
 * 2-byte terminator into `state->body`. Caller frees state->body. */
static cib_status build_oneshot_request(cib_client *c,
                                        const char *route,
                                        const uint8_t *req_json, size_t req_len,
                                        struct req_state *state) {
    if (req_len > CIB_MAX_RECORD_PLAIN - 3) return CIB_ERR_TOO_LARGE;

    if (mbedtls_ctr_drbg_random(&c->drbg, state->req_stream_id, CIB_STREAM_ID_LEN) != 0)
        return CIB_ERR_CRYPTO;
    hex_encode(state->req_stream_id, CIB_STREAM_ID_LEN, state->req_stream_hex);
    state->timestamp_ms = now_unix_ms();

    uint8_t inner[CIB_MAX_RECORD_PLAIN];
    int inner_len = encode_inner(CIB_TYPE_JSON, req_json, req_len, inner, sizeof(inner));
    if (inner_len < 0) return CIB_ERR_TOO_LARGE;

    uint8_t aad[CIB_MAX_AAD];
    size_t aad_len = build_aad(CIB_DIR_REQUEST, c->key_id, route,
                               state->req_stream_hex, 0, aad, sizeof(aad));
    if (!aad_len) return CIB_ERR_BAD_ARG;

    uint8_t nonce[CIB_NONCE_LEN];
    derive_nonce(state->req_stream_id, 0, nonce);

    /* layout: [u16 len][ct+tag][u16 0x0000] */
    size_t body_cap = 2 + (size_t)inner_len + CIB_TAG_LEN + 2;
    state->body = (uint8_t*)malloc(body_cap);
    if (!state->body) return CIB_ERR_BAD_ARG;

    state->body[0] = (uint8_t)((inner_len >> 8) & 0xFF);
    state->body[1] = (uint8_t)( inner_len       & 0xFF);
    if (gcm_seal(c->secret, nonce, aad, aad_len,
                 inner, (size_t)inner_len, state->body + 2) != 0) {
        free(state->body);
        return CIB_ERR_CRYPTO;
    }
    size_t off = 2 + (size_t)inner_len + CIB_TAG_LEN;
    state->body[off + 0] = 0x00;
    state->body[off + 1] = 0x00;
    state->body_len = off + 2;
    return CIB_OK;
}

/* apply_request_headers attaches the four envelope headers to a curl
 * slist. Caller frees the slist. */
static struct curl_slist *apply_request_headers(struct curl_slist *headers,
                                                cib_client *c,
                                                const struct req_state *s) {
    char h_env[CIB_HEADER_BUF], h_key[CIB_HEADER_BUF];
    char h_str[CIB_HEADER_BUF], h_tsh[CIB_HEADER_BUF];
    snprintf(h_env, sizeof(h_env), "Sec-CIB-Envelope: %s",  CIB_ENVELOPE_VERSION);
    snprintf(h_key, sizeof(h_key), "Sec-CIB-KeyId: %s",     c->key_id);
    snprintf(h_str, sizeof(h_str), "Sec-CIB-Stream: %s",    s->req_stream_hex);
    snprintf(h_tsh, sizeof(h_tsh), "Sec-CIB-Timestamp: %lld", (long long)s->timestamp_ms);
    headers = curl_slist_append(headers, h_env);
    headers = curl_slist_append(headers, h_key);
    headers = curl_slist_append(headers, h_str);
    headers = curl_slist_append(headers, h_tsh);
    headers = curl_slist_append(headers, "Content-Type: " CIB_CONTENT_TYPE);
    headers = curl_slist_append(headers, "Expect:");
    return headers;
}

/* ---- response record parser --------------------------------------------- */

/* A tiny streaming state machine over the response body. Feed bytes
 * with parser_push; the parser delivers fully decrypted (type,
 * payload, len) tuples to a callback. Returns 0 to keep going,
 * non-zero to stop. */
struct parser {
    cib_client       *c;
    const char       *route;
    struct resp_hdrs *hdrs;

    /* counters */
    uint32_t counter;

    /* incoming record buffer */
    uint8_t  staging[2 + CIB_MAX_RECORD_PLAIN + CIB_TAG_LEN];
    size_t   staging_len;
    size_t   record_need; /* total bytes for current record (2 + plain + 16), or 0 */
    int      done;        /* terminator received */
    int      aborted;     /* callback asked us to stop */

    /* user callback */
    cib_record_cb cb;
    void         *ud;

    /* error stash */
    cib_status   err;
};

/* parser_drain consumes complete records from the staging buffer. */
static int parser_drain(struct parser *p) {
    for (;;) {
        if (p->done || p->aborted || p->err != CIB_OK) return 0;

        if (p->record_need == 0) {
            if (p->staging_len < 2) return 0;
            uint16_t plain_len = ((uint16_t)p->staging[0] << 8) | p->staging[1];
            if (plain_len == 0) {
                /* terminator */
                p->done = 1;
                p->staging_len = 0;
                return 0;
            }
            if (plain_len > CIB_MAX_RECORD_PLAIN) {
                p->err = CIB_ERR_TOO_LARGE;
                return 1;
            }
            p->record_need = (size_t)2 + plain_len + CIB_TAG_LEN;
        }
        if (p->staging_len < p->record_need) return 0;

        size_t plain_len = p->record_need - 2 - CIB_TAG_LEN;
        uint8_t nonce[CIB_NONCE_LEN];
        derive_nonce(p->hdrs->stream_id, p->counter, nonce);

        uint8_t aad[CIB_MAX_AAD];
        size_t aad_len = build_aad(CIB_DIR_RESPONSE, p->c->key_id, p->route,
                                   p->hdrs->stream_id_hex, p->counter, aad, sizeof(aad));
        if (!aad_len) { p->err = CIB_ERR_BAD_ENVELOPE; return 1; }

        uint8_t plain[CIB_MAX_RECORD_PLAIN];
        if (gcm_open(p->c->secret, nonce, aad, aad_len,
                     p->staging + 2, p->record_need - 2, plain) != 0) {
            p->err = CIB_ERR_BAD_TAG;
            return 1;
        }

        uint8_t type;
        const uint8_t *payload;
        size_t pl_len;
        if (decode_inner(plain, plain_len, &type, &payload, &pl_len) != 0) {
            p->err = CIB_ERR_BAD_ENVELOPE;
            return 1;
        }

        if (p->cb) {
            cib_status cs = p->cb(type, payload, pl_len, p->ud);
            if (cs != CIB_OK) {
                p->aborted = 1;
                p->err = CIB_ERR_ABORTED;
                return 1;
            }
        }

        p->counter++;
        /* shift remaining bytes left */
        size_t leftover = p->staging_len - p->record_need;
        if (leftover) memmove(p->staging, p->staging + p->record_need, leftover);
        p->staging_len = leftover;
        p->record_need = 0;
    }
}

static size_t parser_push(void *ptr, size_t size, size_t nmemb, void *userdata) {
    struct parser *p = (struct parser*)userdata;
    if (p->done || p->aborted || p->err != CIB_OK) return 0; /* abort transfer */

    /* Stream IDs land in headers callback BEFORE body. If they have
     * not arrived yet, refuse to advance (server bug). */
    if (!p->hdrs->have_stream) {
        p->err = CIB_ERR_BAD_ENVELOPE;
        return 0;
    }

    size_t add = size * nmemb;
    if (p->staging_len + add > sizeof(p->staging)) {
        p->err = CIB_ERR_TOO_LARGE;
        return 0;
    }
    memcpy(p->staging + p->staging_len, ptr, add);
    p->staging_len += add;
    parser_drain(p);
    return add;
}

/* ---- one-shot accumulator callback -------------------------------------- */

struct oneshot_acc {
    uint8_t *out_buf;
    size_t   out_cap;
    size_t   out_len;
    int      overflow;
};

static cib_status oneshot_cb(uint8_t type, const uint8_t *payload, size_t len, void *ud) {
    struct oneshot_acc *a = (struct oneshot_acc*)ud;
    if (type != CIB_TYPE_JSON) return CIB_OK; /* drop heartbeats / stream-end */
    if (a->out_buf == NULL) {
        a->out_len += len;
        return CIB_OK;
    }
    if (a->out_len + len > a->out_cap) {
        a->overflow = 1;
        return CIB_OK; /* keep draining so server-side flushes do not stall */
    }
    memcpy(a->out_buf + a->out_len, payload, len);
    a->out_len += len;
    return CIB_OK;
}

/* ---- shared transport core ---------------------------------------------- */

static cib_status http_post_record_stream(cib_client *c,
                                          const char *route,
                                          const uint8_t *req_json, size_t req_len,
                                          cib_record_cb cb, void *ud) {
    if (!c || !route) return CIB_ERR_BAD_ARG;

    struct req_state state = {0};
    cib_status st = build_oneshot_request(c, route, req_json, req_len, &state);
    if (st != CIB_OK) return st;

    char url[512];
    snprintf(url, sizeof(url), "%s%s", c->base_url, route);

    CURL *curl = curl_easy_init();
    if (!curl) { free(state.body); return CIB_ERR_HTTP; }

    struct curl_slist *headers = apply_request_headers(NULL, c, &state);

    struct resp_hdrs rh = {0};
    struct parser p = {
        .c = c, .route = route, .hdrs = &rh, .cb = cb, .ud = ud, .err = CIB_OK,
    };

    curl_easy_setopt(curl, CURLOPT_URL, url);
    curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
    curl_easy_setopt(curl, CURLOPT_POST, 1L);
    curl_easy_setopt(curl, CURLOPT_POSTFIELDS, state.body);
    curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, (long)state.body_len);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, parser_push);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &p);
    curl_easy_setopt(curl, CURLOPT_HEADERFUNCTION, parse_response_header);
    curl_easy_setopt(curl, CURLOPT_HEADERDATA, &rh);
    /* Streaming responses can take a while; let the user cancel by
     * returning non-OK from the callback rather than racing a curl
     * timeout against legitimate long polls. */
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 0L);

    CURLcode rc = curl_easy_perform(curl);
    long http_status = 0;
    curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &http_status);
    curl_slist_free_all(headers);
    curl_easy_cleanup(curl);
    free(state.body);

    if (p.err != CIB_OK && p.err != CIB_ERR_ABORTED) return p.err;
    if (p.aborted) return CIB_ERR_ABORTED;
    if (rc != CURLE_OK && rc != CURLE_WRITE_ERROR) return CIB_ERR_HTTP;

    if (http_status != 200) {
        switch (http_status) {
            case 401: return CIB_ERR_UNKNOWN_KEY;
            case 409: return CIB_ERR_REPLAY;
            case 400: return CIB_ERR_BAD_ENVELOPE;
            case 413: return CIB_ERR_TOO_LARGE;
            default:  return CIB_ERR_HTTP;
        }
    }
    if (!p.done) return CIB_ERR_BAD_ENVELOPE; /* missing terminator */
    return CIB_OK;
}

/* ---- public API ---------------------------------------------------------- */

cib_status cib_client_new(const char *base_url,
                          const char *key_id,
                          const uint8_t secret[CIB_KEY_LEN],
                          cib_client **out) {
    if (!base_url || !key_id || !secret || !out) return CIB_ERR_BAD_ARG;
    if (strlen(base_url) >= sizeof(((cib_client*)0)->base_url)) return CIB_ERR_BAD_ARG;
    if (strlen(key_id)   >= sizeof(((cib_client*)0)->key_id))   return CIB_ERR_BAD_ARG;

    cib_client *c = (cib_client*)calloc(1, sizeof(cib_client));
    if (!c) return CIB_ERR_BAD_ARG;

    snprintf(c->base_url, sizeof(c->base_url), "%s", base_url);
    snprintf(c->key_id,   sizeof(c->key_id),   "%s", key_id);
    memcpy(c->secret, secret, CIB_KEY_LEN);

    mbedtls_entropy_init(&c->entropy);
    mbedtls_ctr_drbg_init(&c->drbg);
    const char *pers = "cib-aes-v2";
    if (mbedtls_ctr_drbg_seed(&c->drbg, mbedtls_entropy_func, &c->entropy,
                              (const unsigned char*)pers, strlen(pers)) != 0) {
        cib_client_free(c);
        return CIB_ERR_CRYPTO;
    }
    *out = c;
    return CIB_OK;
}

void cib_client_free(cib_client *c) {
    if (!c) return;
    mbedtls_ctr_drbg_free(&c->drbg);
    mbedtls_entropy_free(&c->entropy);
    volatile uint8_t *p = c->secret;
    for (size_t i = 0; i < CIB_KEY_LEN; i++) p[i] = 0;
    free(c);
}

cib_status cib_get_time(cib_client *c, int64_t *server_now_ms, int64_t *tolerance_ms) {
    if (!c) return CIB_ERR_BAD_ARG;

    char url[512];
    snprintf(url, sizeof(url), "%s/aes/time", c->base_url);

    struct dyn_buf body = {0};

    CURL *curl = curl_easy_init();
    if (!curl) return CIB_ERR_HTTP;
    curl_easy_setopt(curl, CURLOPT_URL, url);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, dyn_buf_write);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &body);
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 15L);
    CURLcode rc = curl_easy_perform(curl);
    long http_status = 0;
    curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &http_status);
    curl_easy_cleanup(curl);
    if (rc != CURLE_OK || http_status != 200) { free(body.data); return CIB_ERR_HTTP; }

    /* Append NUL so strstr is safe. */
    if (dyn_buf_write("\0", 1, 1, &body) == 0) { free(body.data); return CIB_ERR_BAD_ARG; }
    body.len--;

    const char *p_now = strstr((char*)body.data, "\"server_now\"");
    const char *p_tol = strstr((char*)body.data, "\"tolerance_ms\"");
    if (!p_now || !p_tol) { free(body.data); return CIB_ERR_DECODE; }
    const char *c1 = strchr(p_now, ':');
    const char *c2 = strchr(p_tol, ':');
    if (!c1 || !c2) { free(body.data); return CIB_ERR_DECODE; }
    if (server_now_ms) *server_now_ms = atoll(c1 + 1);
    if (tolerance_ms)  *tolerance_ms  = atoll(c2 + 1);
    free(body.data);
    return CIB_OK;
}

cib_status cib_call_oneshot(cib_client *c,
                            const char *route,
                            const uint8_t *req_json, size_t req_len,
                            uint8_t *resp_buf, size_t resp_cap, size_t *resp_len) {
    struct oneshot_acc acc = { .out_buf = resp_buf, .out_cap = resp_cap };
    cib_status st = http_post_record_stream(c, route, req_json, req_len, oneshot_cb, &acc);
    if (st != CIB_OK) return st;
    if (acc.overflow) return CIB_ERR_BUFFER;
    if (resp_len) *resp_len = acc.out_len;
    return CIB_OK;
}

cib_status cib_stream(cib_client *c,
                      const char *route,
                      const uint8_t *req_json, size_t req_len,
                      cib_record_cb cb, void *ud) {
    if (!cb) return CIB_ERR_BAD_ARG;
    return http_post_record_stream(c, route, req_json, req_len, cb, ud);
}
