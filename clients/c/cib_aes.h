/*
 * cib_aes — reference C client for the claude-in-box AES envelope
 * v2 record-stream transport. See docs/AES-TRANSPORT.md for the wire
 * format. This file is intentionally small so a microcontroller
 * developer can read the whole header in one sitting.
 *
 * Two call shapes, one protocol:
 *   - one-shot:  cib_call_oneshot()  — request/response with a single
 *                JSON record each direction. Use for input / chat /
 *                keyinfo style calls.
 *   - streaming: cib_stream()        — request is one JSON record;
 *                response is a sequence of records delivered to a
 *                callback as they arrive. Used for events/stream.
 */
#ifndef CIB_AES_H
#define CIB_AES_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Result codes. Mirrored to docs/AES-TRANSPORT.md error names where
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
    CIB_ERR_DECODE        = -9,
    CIB_ERR_BUFFER        = -10, /* caller's buffer too small */
    CIB_ERR_CRYPTO        = -11, /* mbedtls primitive failed */
    CIB_ERR_ABORTED       = -12  /* callback asked the stream to stop */
} cib_status;

/* Inner-record plaintext types (mirrors internal/aes/envelope.go). */
#define CIB_TYPE_HEARTBEAT   0x00
#define CIB_TYPE_JSON        0x01
#define CIB_TYPE_FRAME       0x02
#define CIB_TYPE_STREAM_END  0x7F

/* Wire constants. A record buffer of CIB_MAX_RECORD_PLAIN + 16 bytes
 * is enough to hold the largest legal record's ciphertext+tag. */
#define CIB_MAX_RECORD_PLAIN 4096
#define CIB_TAG_LEN          16
#define CIB_KEY_LEN          32
#define CIB_NONCE_LEN        12
#define CIB_STREAM_ID_LEN    16

typedef struct cib_client cib_client;

/*
 * cib_client_new builds a client bound to the box's base URL plus the
 * device's key id and 32-byte AES master secret. base_url must NOT
 * include a trailing slash. The secret is copied.
 */
cib_status cib_client_new(const char *base_url,
                          const char *key_id,
                          const uint8_t secret[CIB_KEY_LEN],
                          cib_client **out);

void cib_client_free(cib_client *c);

/*
 * cib_get_time fetches the box's clock so the device can correct drift
 * before signing any envelope. Cleartext endpoint, no auth.
 */
cib_status cib_get_time(cib_client *c,
                        int64_t *server_now_ms,
                        int64_t *tolerance_ms);

/*
 * cib_call_oneshot performs one round-trip: seal req_json as a single
 * TypeJSON record (+ terminator), POST to <base_url><route>, receive
 * the encrypted response, decrypt and concatenate every TypeJSON
 * record into resp_buf. Heartbeats / stream-end markers in the
 * response are silently dropped.
 *
 * resp_buf may be NULL to discard the response; resp_len receives the
 * plaintext length either way.
 */
cib_status cib_call_oneshot(cib_client *c,
                            const char *route,
                            const uint8_t *req_json, size_t req_len,
                            uint8_t *resp_buf, size_t resp_cap, size_t *resp_len);

/*
 * cib_record_cb is invoked for each decrypted response record during
 * cib_stream. Return CIB_OK to keep reading; any other value aborts
 * the stream and is propagated back to the cib_stream caller.
 *
 * `payload` is owned by the stream loop and is valid only for the
 * duration of the callback — copy what you need.
 */
typedef cib_status (*cib_record_cb)(uint8_t type,
                                    const uint8_t *payload,
                                    size_t len,
                                    void *ud);

/*
 * cib_stream opens a long-lived streaming response. The request body
 * is a single TypeJSON record carrying req_json (typically a stream
 * options struct like {"from":..,"wait_ms":..,"kinds":[..]}). Every
 * record the server emits is delivered to cb in order; when the
 * server writes the stream terminator cib_stream returns CIB_OK.
 *
 * Internally this uses libcurl's chunked-write callback; no body is
 * buffered beyond a single record's worth (≤ CIB_MAX_RECORD_PLAIN +
 * CIB_TAG_LEN + 2 bytes).
 */
cib_status cib_stream(cib_client *c,
                      const char *route,
                      const uint8_t *req_json, size_t req_len,
                      cib_record_cb cb, void *ud);

#ifdef __cplusplus
}
#endif

#endif /* CIB_AES_H */
