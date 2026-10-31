package http

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

type errWriter struct {
	err error
}

func (w *errWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

func TestShouldKeepAlive(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{
			name: "default_http11_keepalive",
			req:  &http.Request{},
			want: true,
		},
		{
			name: "request_close",
			req: &http.Request{
				Close: true,
			},
			want: false,
		},
		{
			name: "proxy_connection_keepalive_override",
			req: &http.Request{
				Close:  true,
				Header: http.Header{"Proxy-Connection": []string{" keep-alive "}},
			},
			want: true,
		},
		{
			name: "proxy_connection_close_override",
			req: &http.Request{
				Header: http.Header{"Proxy-Connection": []string{"Close"}},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldKeepAlive(tc.req); got != tc.want {
				t.Fatalf("shouldKeepAlive() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A malformed upstream response containing a bare '\n' before the real
// status line used to crash readResponseAndHandle100Continue: the first
// ReadSlice('\n') returns fewer than 4 bytes, and slicing
// ResponseHeader1xx[len(ResponseHeader1xx)-4:] panicked with a negative
// index instead of returning an error.
func TestReadResponseAndHandle100ContinueDoesNotPanicOnEarlyNewline(t *testing.T) {
	payload := "X\nHTTP/1.1 100 Continue\r\n\r\n" + strings.Repeat("A", 40)
	r := bufio.NewReader(bytes.NewReader([]byte(payload)))
	req, err := http.NewRequest("GET", "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Must not panic; a parse error for the garbage trailing bytes is fine.
	_, _ = readResponseAndHandle100Continue(r, req, io.Discard)
}

func TestReadResponseAndHandle100ContinueForwardsAndParsesFinalResponse(t *testing.T) {
	payload := "HTTP/1.1 100 Continue\r\n\r\n" +
		"HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"
	r := bufio.NewReader(iotest.OneByteReader(bytes.NewReader([]byte(payload))))
	req, err := http.NewRequest("GET", "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}

	var forwarded bytes.Buffer
	resp, err := readResponseAndHandle100Continue(r, req, &forwarded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(forwarded.String(), "100 Continue") {
		t.Fatalf("expected 1xx response to be forwarded, got %q", forwarded.String())
	}
}

func TestReadResponseAndHandle100Continue(t *testing.T) {
	stream := "" +
		"HTTP/1.1 100 Continue\r\n" +
		"Foo: bar\r\n" +
		"\r\n" +
		"HTTP/1.1 200 OK\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	req := &http.Request{Method: http.MethodPost}
	reader := bufio.NewReader(strings.NewReader(stream))
	forwarded := new(bytes.Buffer)

	resp, err := readResponseAndHandle100Continue(reader, req, forwarded)
	if err != nil {
		t.Fatalf("readResponseAndHandle100Continue() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", resp.StatusCode)
	}
	gotForwarded := forwarded.String()
	if !strings.HasPrefix(gotForwarded, "HTTP/1.1 100 Continue\r\n") {
		t.Fatalf("unexpected forwarded 1xx payload: %q", gotForwarded)
	}
}

func TestReadResponseAndHandle100ContinueWriteError(t *testing.T) {
	stream := "" +
		"HTTP/1.1 100 Continue\r\n" +
		"\r\n" +
		"HTTP/1.1 200 OK\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	req := &http.Request{Method: http.MethodPost}
	reader := bufio.NewReader(strings.NewReader(stream))
	writerErr := errors.New("write failed")

	_, err := readResponseAndHandle100Continue(reader, req, &errWriter{err: writerErr})
	if err == nil {
		t.Fatal("readResponseAndHandle100Continue() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to forward http 1xx response") {
		t.Fatalf("unexpected error: %v", err)
	}
}
