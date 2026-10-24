package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	h := bearerAuth("s3cret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, tc := range []struct {
		name, header string
		want         int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer nope", http.StatusUnauthorized},
		{"wrong scheme", "Basic s3cret", http.StatusUnauthorized},
		{"bare token", "s3cret", http.StatusUnauthorized},
		{"valid", "Bearer s3cret", http.StatusOK},
		{"valid lowercase scheme", "bearer s3cret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestCheckHTTPExposure(t *testing.T) {
	for _, tc := range []struct {
		addr, token string
		insecure    bool
		wantErr     bool
	}{
		{"127.0.0.1:8080", "", false, false},
		{"[::1]:8080", "", false, false},
		{"localhost:8080", "", false, false},
		{"0.0.0.0:8080", "", false, true},
		{":8080", "", false, true},
		{"10.0.0.5:8080", "", false, true},
		{"0.0.0.0:8080", "tok", false, false},
		{"0.0.0.0:8080", "", true, false},
	} {
		err := checkHTTPExposure(tc.addr, tc.token, tc.insecure)
		if (err != nil) != tc.wantErr {
			t.Errorf("%+v: err=%v, wantErr=%v", tc, err, tc.wantErr)
		}
	}
}
