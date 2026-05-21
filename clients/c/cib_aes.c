/*
 * cib_aes.c — reference implementation of the claude-in-box AES envelope
 * client. ~280 LOC including comments. Dependencies:
 *
 *   - mbedtls (gcm, ctr_drbg, entropy)
 *   - libcurl  (HTTP/1.1)
 *
 * The structure deliberately mirrors the wire format in
 * docs/AES-TRANSPORT.md so the code reads like documentation.
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

#define CIB_ENVELOPE_VERSION  "1"
#define CIB_NONCE_LEN         12
#define CIB_TAG_LEN           16
#define CIB_KEY_LEN           32
#define CIB_MAX_AAD           512
#define CIB_HEADER_BUF        128

/* ---- client ------------------------------------------------------------- */

struct cib_client {
    char     base_url[256];
    char     key_id[64];
    uint8_t  secret[CIB_KEY_LEN];

    mbedtls_entropy_context  entropy;
    mbedtls_ctr_drbg_context drbg;
};

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
    const char *pers = "cib-aes";
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
    /* zero the key so a core dump does not leak it */
    volatile uint8_t *p = c->secret;
    for (size_t i = 0; i < CIB_KEY_LEN; i++) p[i] = 0;
    free(c);
}

/* ---- helpers ------------------------------------------------------------ */

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

static size_t build_aad(const char *envelope_version,
                        const char *key_id,
                        int64_t     timestamp_ms,
                        const char *method,
                        const char *route,
                        uint8_t    *out,
                        size_t      out_cap) {
    char ts[32];
    snprintf(ts, sizeof(ts), "%lld", (long long)timestamp_ms);
    int n = snprintf((char*)out, out_cap,
                     "CIB%s\n%s\n%s\n%s\n%s\n",
                     envelope_version, key_id, ts, method, route);
    return (n < 0 || (size_t)n >= out_cap) ? 0 : (size_t)n;
}

/* GCM seal/open. mbedtls returns 0 on success. */
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

/* ---- curl buffer adapter ------------------------------------------------- */

struct buf {
    uint8_t *data;
    size_t   len;
    size_t   cap;
};

static size_t buf_write(void *ptr, size_t size, size_t nmemb, void *userdata) {
    struct buf *b = (struct buf*)userdata;
    size_t add = size * nmemb;
    if (b->len + add > b->cap) return 0; /* tell curl we cannot accept more */
    memcpy(b->data + b->len, ptr, add);
    b->len += add;
    return add;
}

static cib_status http_envelope_post(cib_client *c,
                                     const char *route,
                                     const uint8_t *plaintext, size_t plaintext_len,
                                     uint8_t *resp_pt, size_t resp_pt_cap, size_t *resp_pt_len) {
    if (!c || !route) return CIB_ERR_BAD_ARG;

    /* 1. Build envelope metadata for the request. */
    uint8_t nonce[CIB_NONCE_LEN];
    if (mbedtls_ctr_drbg_random(&c->drbg, nonce, CIB_NONCE_LEN) != 0) return CIB_ERR_CRYPTO;
    char nonce_hex[CIB_NONCE_LEN*2 + 1];
    hex_encode(nonce, CIB_NONCE_LEN, nonce_hex);

    int64_t ts_ms = now_unix_ms();

    uint8_t aad[CIB_MAX_AAD];
    size_t aad_len = build_aad(CIB_ENVELOPE_VERSION, c->key_id, ts_ms, "POST", route, aad, sizeof(aad));
    if (!aad_len) return CIB_ERR_BAD_ARG;

    /* 2. Seal the plaintext into ciphertext + tag. */
    size_t ct_len = plaintext_len + CIB_TAG_LEN;
    uint8_t *ct = (uint8_t*)malloc(ct_len);
    if (!ct) return CIB_ERR_BAD_ARG;
    if (gcm_seal(c->secret, nonce, aad, aad_len, plaintext, plaintext_len, ct) != 0) {
        free(ct);
        return CIB_ERR_CRYPTO;
    }

    /* 3. HTTP POST via libcurl. */
    char url[512];
    snprintf(url, sizeof(url), "%s%s", c->base_url, route);

    CURL *curl = curl_easy_init();
    if (!curl) { free(ct); return CIB_ERR_HTTP; }

    char ts_str[32];
    snprintf(ts_str, sizeof(ts_str), "%lld", (long long)ts_ms);

    char h_env[CIB_HEADER_BUF], h_key[CIB_HEADER_BUF];
    char h_non[CIB_HEADER_BUF], h_tsh[CIB_HEADER_BUF];
    snprintf(h_env, sizeof(h_env), "Sec-CIB-Envelope: %s",  CIB_ENVELOPE_VERSION);
    snprintf(h_key, sizeof(h_key), "Sec-CIB-KeyId: %s",     c->key_id);
    snprintf(h_non, sizeof(h_non), "Sec-CIB-Nonce: %s",     nonce_hex);
    snprintf(h_tsh, sizeof(h_tsh), "Sec-CIB-Timestamp: %s", ts_str);

    struct curl_slist *headers = NULL;
    headers = curl_slist_append(headers, h_env);
    headers = curl_slist_append(headers, h_key);
    headers = curl_slist_append(headers, h_non);
    headers = curl_slist_append(headers, h_tsh);
    headers = curl_slist_append(headers, "Content-Type: application/octet-stream");
    headers = curl_slist_append(headers, "Expect:");

    uint8_t resp_buf[64 * 1024];
    struct buf body  = { .data = resp_buf, .len = 0, .cap = sizeof(resp_buf) };
    struct buf hdrs  = { .data = (uint8_t*)malloc(8192), .len = 0, .cap = 8192 };
    if (!hdrs.data) { curl_slist_free_all(headers); curl_easy_cleanup(curl); free(ct); return CIB_ERR_BAD_ARG; }

    curl_easy_setopt(curl, CURLOPT_URL, url);
    curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
    curl_easy_setopt(curl, CURLOPT_POST, 1L);
    curl_easy_setopt(curl, CURLOPT_POSTFIELDS, ct);
    curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, (long)ct_len);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, buf_write);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA,   &body);
    curl_easy_setopt(curl, CURLOPT_HEADERFUNCTION, buf_write);
    curl_easy_setopt(curl, CURLOPT_HEADERDATA,   &hdrs);
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 60L);

    CURLcode rc = curl_easy_perform(curl);
    long http_status = 0;
    curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &http_status);
    curl_slist_free_all(headers);
    curl_easy_cleanup(curl);
    free(ct);

    if (rc != CURLE_OK) { free(hdrs.data); return CIB_ERR_HTTP; }

    if (http_status != 200) {
        /* Server returned a cleartext error envelope; we do not parse it
         * here (caller can inspect over the wire if needed) and surface
         * the closest matching enum. */
        free(hdrs.data);
        switch (http_status) {
            case 401: return CIB_ERR_UNKNOWN_KEY;
            case 409: return CIB_ERR_REPLAY;
            case 400: return CIB_ERR_BAD_ENVELOPE;
            case 413: return CIB_ERR_TOO_LARGE;
            default:  return CIB_ERR_HTTP;
        }
    }

    /* 4. Pull the response envelope nonce + timestamp from the headers. */
    char *hdr_str = (char*)hdrs.data;
    hdr_str[hdrs.len] = '\0';

    char resp_nonce_hex[CIB_NONCE_LEN*2 + 1] = {0};
    int64_t resp_ts_ms = 0;
    char *p = hdr_str;
    while (p && *p) {
        if (strncasecmp(p, "Sec-CIB-Nonce:", 14) == 0) {
            p += 14; while (*p == ' ') p++;
            char *end = strpbrk(p, "\r\n"); size_t n = end ? (size_t)(end - p) : strlen(p);
            if (n >= sizeof(resp_nonce_hex)) n = sizeof(resp_nonce_hex) - 1;
            memcpy(resp_nonce_hex, p, n); resp_nonce_hex[n] = '\0';
        } else if (strncasecmp(p, "Sec-CIB-Timestamp:", 18) == 0) {
            resp_ts_ms = atoll(p + 18);
        }
        char *nl = strchr(p, '\n'); if (!nl) break; p = nl + 1;
    }
    free(hdrs.data);

    if (!resp_nonce_hex[0] || !resp_ts_ms) return CIB_ERR_BAD_ENVELOPE;

    uint8_t resp_nonce[CIB_NONCE_LEN];
    if (hex_decode(resp_nonce_hex, strlen(resp_nonce_hex), resp_nonce, sizeof(resp_nonce)) != CIB_NONCE_LEN)
        return CIB_ERR_DECODE;

    /* 5. Open the response. Note the "RESPONSE" pseudo-method. */
    uint8_t resp_aad[CIB_MAX_AAD];
    size_t resp_aad_len = build_aad(CIB_ENVELOPE_VERSION, c->key_id, resp_ts_ms,
                                    "RESPONSE", route, resp_aad, sizeof(resp_aad));
    if (!resp_aad_len) return CIB_ERR_BAD_ENVELOPE;

    if (body.len < CIB_TAG_LEN) return CIB_ERR_BAD_ENVELOPE;
    size_t pt_len = body.len - CIB_TAG_LEN;
    if (resp_pt && pt_len > resp_pt_cap) return CIB_ERR_BUFFER;

    if (resp_pt) {
        if (gcm_open(c->secret, resp_nonce, resp_aad, resp_aad_len,
                     body.data, body.len, resp_pt) != 0) {
            return CIB_ERR_BAD_TAG;
        }
        if (resp_pt_len) *resp_pt_len = pt_len;
    } else if (resp_pt_len) {
        *resp_pt_len = pt_len; /* caller asked for length only */
    }
    return CIB_OK;
}

/* ---- public API ---------------------------------------------------------- */

cib_status cib_get_time(cib_client *c, int64_t *server_now_ms, int64_t *tolerance_ms) {
    if (!c) return CIB_ERR_BAD_ARG;

    char url[512];
    snprintf(url, sizeof(url), "%s/aes/time", c->base_url);

    uint8_t buf[1024];
    struct buf b = { .data = buf, .len = 0, .cap = sizeof(buf) - 1 };

    CURL *curl = curl_easy_init();
    if (!curl) return CIB_ERR_HTTP;
    curl_easy_setopt(curl, CURLOPT_URL, url);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, buf_write);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &b);
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 15L);
    CURLcode rc = curl_easy_perform(curl);
    long http_status = 0;
    curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &http_status);
    curl_easy_cleanup(curl);
    if (rc != CURLE_OK || http_status != 200) return CIB_ERR_HTTP;
    buf[b.len] = '\0';

    /* Minimal JSON extraction; the response is a single-level object with
     * two integer fields. A proper JSON parser is nicer but adds a dep. */
    const char *p_now = strstr((char*)buf, "\"server_now\"");
    const char *p_tol = strstr((char*)buf, "\"tolerance_ms\"");
    if (!p_now || !p_tol) return CIB_ERR_DECODE;
    const char *c1 = strchr(p_now, ':');
    const char *c2 = strchr(p_tol, ':');
    if (!c1 || !c2) return CIB_ERR_DECODE;
    if (server_now_ms) *server_now_ms = atoll(c1 + 1);
    if (tolerance_ms)  *tolerance_ms  = atoll(c2 + 1);
    return CIB_OK;
}

cib_status cib_send_input(cib_client *c,
                          const char *session_id,
                          const uint8_t *plaintext_json, size_t plaintext_len,
                          uint8_t *out_buf, size_t out_cap, size_t *out_len) {
    char route[256];
    snprintf(route, sizeof(route), "/aes/sessions/%s/input", session_id);
    return http_envelope_post(c, route, plaintext_json, plaintext_len, out_buf, out_cap, out_len);
}

cib_status cib_poll_events(cib_client *c,
                           const char *session_id,
                           const uint8_t *request_json, size_t request_len,
                           uint8_t *out_buf, size_t out_cap, size_t *out_len) {
    char route[256];
    snprintf(route, sizeof(route), "/aes/sessions/%s/events/poll", session_id);
    return http_envelope_post(c, route, request_json, request_len, out_buf, out_cap, out_len);
}
