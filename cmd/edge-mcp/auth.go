package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
)

// bearerAuth rejects requests that do not carry "Authorization: Bearer <token>".
// Tokens are hashed first so the comparison is constant time regardless of length.
func bearerAuth(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		have := sha256.Sum256([]byte(strings.TrimSpace(got)))
		if !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(want[:], have[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="edge-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkHTTPExposure refuses a non-loopback bind without a token unless insecure is set.
func checkHTTPExposure(addr, token string, insecure bool) error {
	if token != "" || insecure || isLoopbackAddr(addr) {
		return nil
	}
	return errors.New("refusing to serve HTTP on non-loopback address " + addr +
		" without authentication: set --http-token (or EDGE_MCP_TOKEN), or pass --http-insecure to override")
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
