package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var swosHTTP = &http.Client{Timeout: 5 * time.Second}

// swosGet fetches one page of the SwOS web interface, such as "stats.b". SwOS only speaks plain
// HTTP and protects its pages with digest authentication, so the password is never sent as is.
func swosGet(ctx context.Context, target, user, password, page string) ([]byte, error) {
	url := "http://" + net.JoinHostPort(target, "80") + "/" + page
	do := func(authorization string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		return swosHTTP.Do(req)
	}
	resp, err := do("")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		authorization, err := digestAuthorization(challenge, user, password, "/"+page)
		if err != nil {
			return nil, err
		}
		if resp, err = do(authorization); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("SwOS-webben nekade inloggningen")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SwOS-webben svarade %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// digestAuthorization answers an RFC 2617 MD5 digest challenge, with or without qop=auth.
func digestAuthorization(challenge, user, password, uri string) (string, error) {
	scheme, rest, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Digest") {
		return "", fmt.Errorf("SwOS-webben kräver okänd inloggning %q", scheme)
	}
	params := map[string]string{}
	for _, part := range splitParams(rest) {
		key, value, _ := strings.Cut(part, "=")
		params[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	if algorithm := params["algorithm"]; algorithm != "" && !strings.EqualFold(algorithm, "MD5") {
		return "", fmt.Errorf("SwOS-webben kräver digest-algoritmen %s", algorithm)
	}
	hash := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	ha1 := hash(user + ":" + params["realm"] + ":" + password)
	ha2 := hash("GET:" + uri)
	header := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s"`, user, params["realm"], params["nonce"], uri)
	qop := ""
	for _, q := range strings.Split(params["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			qop = "auth"
		}
	}
	if qop == "" {
		header += fmt.Sprintf(`, response="%s"`, hash(ha1+":"+params["nonce"]+":"+ha2))
	} else {
		nonce := make([]byte, 8)
		_, _ = rand.Read(nonce)
		cnonce := hex.EncodeToString(nonce)
		response := hash(ha1 + ":" + params["nonce"] + ":00000001:" + cnonce + ":auth:" + ha2)
		header += fmt.Sprintf(`, qop=auth, nc=00000001, cnonce="%s", response="%s"`, cnonce, response)
	}
	if opaque, ok := params["opaque"]; ok {
		header += fmt.Sprintf(`, opaque="%s"`, opaque)
	}
	return header, nil
}

// splitParams splits a challenge on commas outside quotes.
func splitParams(s string) []string {
	var parts []string
	quoted, start := false, 0
	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

var swosRxPausePattern = regexp.MustCompile(`[{,]rpp:\[([0-9a-fA-Fx,]*)\]`)

// swosStatsPage remembers per address which page has the counters: SwOS serves "stats.b" and
// SwOS Lite (CSS106) "!stats.b". Either answers the other path with its web interface instead.
var swosStatsPage sync.Map

// swosRxPause reads the received pause frame counters (Rx Pauses on the Errors tab) from SwOS,
// one per port in port order, which is also the SNMP ifIndex order.
func swosRxPause(ctx context.Context, target, user, password string) ([]uint64, error) {
	pages := []string{"stats.b", "!stats.b"}
	if known, ok := swosStatsPage.Load(target); ok {
		pages = []string{known.(string)}
	}
	var err error
	for _, page := range pages {
		var body []byte
		if body, err = swosGet(ctx, target, user, password, page); err != nil {
			return nil, err
		}
		var counts []uint64
		if counts, err = parseSwosCounters(body); err == nil {
			swosStatsPage.Store(target, page)
			return counts, nil
		}
	}
	// The firmware may have changed; look at both pages again next time.
	swosStatsPage.Delete(target)
	return nil, err
}

func parseSwosCounters(body []byte) ([]uint64, error) {
	match := swosRxPausePattern.FindSubmatch(body)
	if match == nil {
		return nil, errors.New("SwOS-statistiken saknar Rx Pauses")
	}
	var counts []uint64
	for _, field := range strings.Split(string(match[1]), ",") {
		value, err := strconv.ParseUint(strings.TrimPrefix(field, "0x"), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("oväntad SwOS-räknare %q", field)
		}
		counts = append(counts, value)
	}
	return counts, nil
}
