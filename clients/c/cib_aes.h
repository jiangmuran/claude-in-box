/*
 * cib_aes — reference C client for the claude-in-box AES envelope
 * transport. See docs/AES-TRANSPORT.md in the repo root for the wire
 * format. This file is intentionally small (~80 LOC) so a microcontroller
 * developer can read the whole thing in one sitting and port it to their
 * platform with minimal effort.
 */
#ifndef CIB_AES_H
#define CIB_AES_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Result codes. Keep matched to docs/AES-TRANSPORT.md error names where
 * possible so a device can map server errors back to the same enum. */
typedef enum {
    CIB_OK                = 0,
    CIB_ERR_BAD_ARG       = -1,
    CIB_ERR_HTTP          = -2,  /* underlying HTTP transport failed */
    CIB_ERR_UNKNOWN_KEY   = -3,
    CIB_ERR_CLOCK_DRIFT   = -4,
    CIB_ERR_REPLAY        = -5,
    CIB_ERR_BAD_TAG       = -6,  /* GCM tag mismatch on response */
    CIB_ERR_BAD_ENVELOPE  = -7,
    CIB_ERR_TOO_LARGE     = -8,
    CIB_ERR_DECODE        = -9,  /* response JSON / hex / base64 decode failed */
    CIB_ERR_BUFFER        = -10, /* caller's buffer too small */
    CIB_ERR_CRYPTO        = -11  /* mbedtls primitive failed */
} cib_status;

typedef struct cib_client cib_client;

/*
 * cib_client_new builds a client bound to the box's base URL plus this
 * device's key id and 32-byte AES master secret. The base URL must NOT
 * include a trailing slash. The secret is copied; the caller may free its
 * buffer immediately after this call returns.
 */
cib_status cib_client_new(const char *base_url,
                          const char *key_id,
                          const uint8_t secret[32],
                          cib_client **out);

void cib_client_free(cib_client *c);

/*
 * cib_get_time fetches the box's current clock so the device can correct
 * drift before signing any envelope. Cleartext endpoint, no auth.
 */
cib_status cib_get_time(cib_client *c,
                        int64_t *server_now_ms,
                        int64_t *tolerance_ms);

/*
 * cib_send_input posts encrypted input to a session.
 * `plaintext_json` should be a JSON object like {"data":"hello\n"}.
 * `out_buf` receives the (already-decrypted) response JSON body; its
 * length is written to `*out_len`. Pass out_buf=NULL/out_cap=0 to
 * discard the response.
 */
cib_status cib_send_input(cib_client *c,
                          const char *session_id,
                          const uint8_t *plaintext_json,
                          size_t plaintext_len,
                          uint8_t *out_buf,
                          size_t out_cap,
                          size_t *out_len);

/*
 * cib_poll_events long-polls for new frames. `request_json` is a JSON
 * object like {"from":<seq>,"max":<n>,"wait_ms":<ms>}. The response
 * lands in out_buf as JSON {"frames":[...],"last_seq":N}.
 */
cib_status cib_poll_events(cib_client *c,
                           const char *session_id,
                           const uint8_t *request_json,
                           size_t request_len,
                           uint8_t *out_buf,
                           size_t out_cap,
                           size_t *out_len);

#ifdef __cplusplus
}
#endif

#endif /* CIB_AES_H */
