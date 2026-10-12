package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAddr(t *testing.T) {
	for _, tc := range []struct {
		bind string
		want string
	}{
		{"", "127.0.0.1:8080"},
		{"0.0.0.0", "127.0.0.1:8080"},
		{"::", "127.0.0.1:8080"},
		{"10.0.0.5", "10.0.0.5:8080"},
		{"::1", "[::1]:8080"},
		{"localhost", "localhost:8080"},
	} {
		if got := healthAddr(tc.bind, 8080); got != tc.want {
			t.Errorf("healthAddr(%q) = %q, want %q", tc.bind, got, tc.want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	defer ok.Close()
	if err := healthcheck(strings.TrimPrefix(ok.URL, "http://")); err != nil {
		t.Errorf("healthy server: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if err := healthcheck(strings.TrimPrefix(bad.URL, "http://")); err == nil {
		t.Error("500 should fail")
	}

	bad.Close()
	if err := healthcheck(strings.TrimPrefix(bad.URL, "http://")); err == nil {
		t.Error("closed port should fail")
	}
}
