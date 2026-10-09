package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The poster route only serves what a search or a request returned.
func TestRequestPosterRoute(t *testing.T) {
	h, _ := newHandler(t)
	for path, want := range map[string]int{
		"/requests/posters/" + strings.Repeat("0", 32): http.StatusNotFound,
		"/requests/posters/not-a-key":                  http.StatusNotFound,
		"/requests/posters/..%2F..%2Fsecret":           http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}
