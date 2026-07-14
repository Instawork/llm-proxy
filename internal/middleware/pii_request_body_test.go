package middleware

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestSetRequestBodyUpdatesContentLengthHeader(t *testing.T) {
	r, err := http.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Length", "7")
	r.ContentLength = 7

	newBody := []byte(`{"x":1,"pii":"<PII_PERSON_1>"}`)
	setRequestBody(r, newBody)

	if r.ContentLength != int64(len(newBody)) {
		t.Fatalf("ContentLength = %d, want %d", r.ContentLength, len(newBody))
	}
	want := strconv.Itoa(len(newBody))
	if got := r.Header.Get("Content-Length"); got != want {
		t.Fatalf("Content-Length header = %q, want %q", got, want)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBody) {
		t.Fatalf("body = %q, want %q", got, newBody)
	}
}
