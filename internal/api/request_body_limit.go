package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	defaultRequestBodyMaxBytes int64 = 2 << 20 // 2 MiB
	requestBodyMaxBytesEnv           = "REQUEST_BODY_MAX_BYTES"
	requestDumpEnv                   = "REQUEST_DUMP"
)

func requestDumpEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(requestDumpEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func requestBodyMaxBytesFromEnv() int64 {
	raw := strings.TrimSpace(os.Getenv(requestBodyMaxBytesEnv))
	if raw == "" {
		return defaultRequestBodyMaxBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return defaultRequestBodyMaxBytes
	}
	return n
}

func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || maxBytes <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		limited := http.MaxBytesReader(w, r.Body, maxBytes)
		body, err := io.ReadAll(limited)
		_ = limited.Close()
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				slog.Info("publisher request body rejected",
					"event", "request_body_too_large",
					"body_len", len(body),
					"body_truncated", true,
					"body_limit_bytes", maxBytes,
					"path", r.URL.Path,
					"method", r.Method,
				)
				writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE",
					"request body exceeds maximum size",
					map[string]any{"max_bytes": maxBytes})
				return
			}
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
