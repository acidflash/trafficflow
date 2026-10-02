package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSwosDigestLogin(t *testing.T) {
	hash := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		params := map[string]string{}
		for _, part := range splitParams(strings.TrimPrefix(auth, "Digest ")) {
			key, value, _ := strings.Cut(part, "=")
			params[strings.TrimSpace(key)] = strings.Trim(value, `"`)
		}
		ha1, ha2 := hash("admin:Mikrotik:hemligt"), hash("GET:"+r.URL.Path)
		if params["response"] != hash(ha1+":abc:"+params["nc"]+":"+params["cnonce"]+":auth:"+ha2) {
			w.Header().Set("WWW-Authenticate", `Digest realm="Mikrotik", qop="auth", nonce="abc", stale=FALSE`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("{rxp:[0x1]}"))
	}))
	defer srv.Close()
	// swosGet always uses port 80, so point it at the test server through its transport.
	saved := swosHTTP
	defer func() { swosHTTP = saved }()
	swosHTTP = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}}
	body, err := swosGet(t.Context(), "192.0.2.1", "admin", "hemligt", "!stats.b")
	if err != nil || string(body) != "{rxp:[0x1]}" {
		t.Fatalf("got %q %v", body, err)
	}
	if _, err := swosGet(t.Context(), "192.0.2.1", "admin", "fel", "!stats.b"); err == nil || !strings.Contains(err.Error(), "nekade") {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestParseSwosCounters(t *testing.T) {
	body := []byte("{rb:[0x00000001,0x00000002],tpp:[0x00000009,0x00000000],rpp:[0x00000000,0x0000001a,0xffffffff],rov:[0x0,0x0,0x0]}")
	got, err := parseSwosCounters(body)
	if err != nil || len(got) != 3 || got[0] != 0 || got[1] != 26 || got[2] != 0xffffffff {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := parseSwosCounters([]byte("<!doctype html>")); err == nil {
		t.Fatal("login page accepted")
	}
}

func TestSwosRxPauseFindsLitePage(t *testing.T) {
	requests := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		if r.URL.Path == "/!stats.b" {
			_, _ = w.Write([]byte("{rbp:[0x1,0x2],rpp:[0x0,0x7]}"))
			return
		}
		_, _ = w.Write([]byte("<!doctype html><title>MikroTik SwOS Lite</title>"))
	}))
	defer srv.Close()
	saved := swosHTTP
	defer func() { swosHTTP = saved }()
	swosHTTP = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}}
	for range 2 {
		got, err := swosRxPause(t.Context(), "192.0.2.9", "admin", "x")
		if err != nil || len(got) != 2 || got[1] != 7 {
			t.Fatalf("got %v %v", got, err)
		}
	}
	if requests["/stats.b"] != 1 || requests["/!stats.b"] != 2 {
		t.Fatalf("the working page was not remembered: %v", requests)
	}
}
