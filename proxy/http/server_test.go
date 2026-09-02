package http

import (
	"bufio"
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
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
