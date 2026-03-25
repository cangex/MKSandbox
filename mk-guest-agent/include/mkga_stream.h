#ifndef MKGA_STREAM_H
#define MKGA_STREAM_H

#include <stddef.h>
#include <stdint.h>

#include "mkring_stream.h"

int mkga_stream_ensure_started(void);
int mkga_stream_send_output(const char *session_id, const uint8_t *data, size_t len);
int mkga_stream_send_exit(const char *session_id, int32_t exit_code);

#endif
