package relayclient

import (
	"net/http"
	"reflect"
	"testing"
)

// TestStripPairKeyResponseHeaders pins the regression that broke every
// pairkey RPC on 2026-05-02. handlePairKeyRPC propagates the local server's
// resp.Header to the relay verbatim; the relay forwards those headers as the
// outer HTTP response. The local server's Content-Length describes the
// CLEARTEXT body, but handlePairKeyRPC writes nonce(12) || ciphertext to the
// stream — i.e. cleartext_len + 28 bytes. With Content-Length pointing at
// the cleartext length, Caddy in front of the relay aborts the response
// with "reading: unexpected EOF" the moment byte counts diverge, and the
// mobile sees a generic "Network request failed" with no useful detail
// (the GIN log on the relay still shows 200 because the status line went
// out fine — only the body delivery is truncated).
//
// stripPairKeyResponseHeaders must remove Content-Length, Transfer-Encoding,
// and Content-Encoding so net/http re-frames against the actual encrypted
// body length. It must NOT mutate its input (the caller may still need the
// original headers for billing/logging).
func TestStripPairKeyResponseHeaders(t *testing.T) {
	cases := []struct {
		name string
		in   http.Header
		want http.Header
	}{
		{
			name: "strips Content-Length",
			in: http.Header{
				"Content-Type":   {"application/json"},
				"Content-Length": {"42"},
			},
			want: http.Header{
				"Content-Type": {"application/json"},
			},
		},
		{
			name: "strips Transfer-Encoding",
			in: http.Header{
				"Content-Type":      {"application/json"},
				"Transfer-Encoding": {"chunked"},
			},
			want: http.Header{
				"Content-Type": {"application/json"},
			},
		},
		{
			name: "strips Content-Encoding (relay would mis-decode)",
			in: http.Header{
				"Content-Type":     {"application/octet-stream"},
				"Content-Encoding": {"gzip"},
			},
			want: http.Header{
				"Content-Type": {"application/octet-stream"},
			},
		},
		{
			name: "case-insensitive (CanonicalHeaderKey)",
			in: http.Header{
				"content-length":    {"42"},
				"TRANSFER-ENCODING": {"chunked"},
				"x-keep":            {"yes"},
			},
			want: http.Header{
				"x-keep": {"yes"},
			},
		},
		{
			name: "strips all three at once",
			in: http.Header{
				"Content-Type":      {"application/json"},
				"Content-Length":    {"42"},
				"Transfer-Encoding": {"chunked"},
				"Content-Encoding":  {"gzip"},
				"X-Custom":          {"yes"},
			},
			want: http.Header{
				"Content-Type": {"application/json"},
				"X-Custom":     {"yes"},
			},
		},
		{
			name: "passes through unrelated headers",
			in: http.Header{
				"Set-Cookie":      {"a=1", "b=2"},
				"Cache-Control":   {"no-store"},
				"X-Niuniu-Custom": {"value"},
			},
			want: http.Header{
				"Set-Cookie":      {"a=1", "b=2"},
				"Cache-Control":   {"no-store"},
				"X-Niuniu-Custom": {"value"},
			},
		},
		{
			name: "empty input returns same",
			in:   http.Header{},
			want: http.Header{},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Snapshot input to detect accidental mutation.
			before := http.Header{}
			for k, v := range tc.in {
				cp := make([]string, len(v))
				copy(cp, v)
				before[k] = cp
			}
			got := stripPairKeyResponseHeaders(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.in, before) {
				t.Fatalf("input mutated: got %v, want %v", tc.in, before)
			}
		})
	}
}

// TestStripPairKeyResponseHeaders_NilSafe ensures the helper handles a nil
// http.Header (which is what http.Response carries when the local server
// emitted a response with no headers set).
func TestStripPairKeyResponseHeaders_NilSafe(t *testing.T) {
	got := stripPairKeyResponseHeaders(nil)
	if got != nil && len(got) != 0 {
		t.Fatalf("expected nil or empty header from nil input, got %v", got)
	}
}
