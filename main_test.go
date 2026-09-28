package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *store {
	t.Helper()
	t.Setenv("APP_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "test.db"))
	t.Setenv("ADMIN_PASSWORD", "long-test-password-123")
	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	if err := bootstrapAdmin(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRate(t *testing.T) {
	cases := []struct {
		name          string
		before, after uint64
		is64          bool
		elapsed       time.Duration
		speed         int64
		uptime        bool
		want          float64
		valid         bool
	}{
		{"normal", 100, 1100, true, time.Second, 1000000000, true, 8000, true},
		{"reset", 1100, 100, true, time.Second, 1000000000, true, 0, false},
		{"reboot", 100, 1100, true, time.Second, 1000000000, false, 0, false},
		{"32bit wrap", (1 << 32) - 100, 100, false, time.Second, 1000000000, true, 1600, true},
		{"32bit fast port", 100, 1100, false, 15 * time.Second, 10000000000, true, 0, false},
		{"implausible", 100, 200000000, true, time.Second, 100000000, true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := rate(tc.before, tc.after, tc.is64, tc.elapsed, tc.speed, tc.uptime)
			if ok != tc.valid || got != tc.want {
				t.Fatalf("got %v %v, want %v %v", got, ok, tc.want, tc.valid)
			}
		})
	}
}

func TestCredentialsAndAuth(t *testing.T) {
	s := testStore(t)
	value, err := s.encrypt(`{"community":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(value, []byte("secret")) {
		t.Fatal("credential stored in plaintext")
	}
	plain, err := s.decrypt(value)
	if err != nil || plain != `{"community":"secret"}` {
		t.Fatalf("decrypt: %q %v", plain, err)
	}
	a := &server{store: s}
	request := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(`{"username":"admin","password":"long-test-password-123"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	a.login(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("login failed: %d %s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("session cookie is not secure")
	}
	request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	request.AddCookie(cookies[0])
	recorder = httptest.NewRecorder()
	a.authed(a.me)(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("authenticated request failed: %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	recorder = httptest.NewRecorder()
	a.authed(a.me)(recorder, request)
	if recorder.Code != 401 {
		t.Fatalf("unauthenticated request: %d", recorder.Code)
	}
}

func TestTopologyAndLink(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	secret, _ := s.encrypt(`{"community":"test"}`)
	for _, address := range []string{"192.0.2.1", "192.0.2.2"} {
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential) VALUES(?,?,'swos','2c',?)", address, address, secret); err != nil {
			t.Fatal(err)
		}
	}
	for _, device := range []int{1, 2} {
		if _, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name) VALUES(?,1,'ether1')", device); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.insertLink(t.Context(), 1, 2, "manual"); err != nil {
		t.Fatal(err)
	}
	if err := a.insertLink(t.Context(), 2, 1, "manual"); err == nil {
		t.Fatal("reverse duplicate accepted")
	}
	if err := a.insertLink(t.Context(), 1, 1, "manual"); err == nil {
		t.Fatal("self-link accepted")
	}
	recorder := httptest.NewRecorder()
	a.topology(recorder, httptest.NewRequest("GET", "/api/topology", nil))
	if recorder.Code != 200 {
		t.Fatalf("topology %d", recorder.Code)
	}
	var result struct {
		Devices []deviceView `json:"devices"`
		Links   []linkView   `json:"links"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Devices) != 2 || len(result.Links) != 1 {
		t.Fatalf("unexpected topology: %+v", result)
	}
	deleteResponse := httptest.NewRecorder()
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/links/1", nil)
	deleteRequest.SetPathValue("id", "1")
	a.deleteLink(deleteResponse, deleteRequest)
	if deleteResponse.Code != 200 {
		t.Fatalf("delete link: %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var suppressed int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM suppressed_links WHERE a_interface_id=1 AND b_interface_id=2").Scan(&suppressed); err != nil || suppressed != 1 {
		t.Fatalf("suppression missing: %d %v", suppressed, err)
	}
}

func TestHostnames(t *testing.T) {
	for _, name := range []string{"switch1", "core-01.example.lan", "sw.lan."} {
		if !validHostname(name) {
			t.Fatalf("rejected %q", name)
		}
	}
	for _, name := range []string{"", "-bad.lan", "bad-.lan", "a..b", "sw_1.lan", "sw 1", "http://sw.lan"} {
		if validHostname(name) {
			t.Fatalf("accepted %q", name)
		}
	}
	if got, err := resolveAddress(t.Context(), "192.0.2.5"); err != nil || got != "192.0.2.5" {
		t.Fatalf("ip: %q %v", got, err)
	}
	if got, err := resolveAddress(t.Context(), "localhost"); err != nil || got != "127.0.0.1" {
		t.Fatalf("localhost: %q %v", got, err)
	}
	if _, err := resolveAddress(t.Context(), "does-not-exist.invalid"); err == nil {
		t.Fatal("resolved .invalid name")
	}
}

func TestAddDeviceByName(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	add := func(address string) int {
		body := `{"address":"` + address + `","os":"swos","snmpVersion":"2c","community":"c","user":"","authPassword":"","privPassword":""}`
		request := httptest.NewRequest(http.MethodPost, "/api/devices", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.addDevice(recorder, request)
		return recorder.Code
	}
	if code := add(" LocalHost. "); code != 201 {
		t.Fatalf("add by name: %d", code)
	}
	var address, resolved string
	if err := s.db.QueryRow("SELECT address,resolved FROM devices").Scan(&address, &resolved); err != nil || address != "localhost" || resolved != "127.0.0.1" {
		t.Fatalf("stored %q %q %v", address, resolved, err)
	}
	if code := add("does-not-exist.invalid"); code != 400 {
		t.Fatalf("unresolvable name: %d", code)
	}
	if code := add("bad_name"); code != 400 {
		t.Fatalf("invalid name: %d", code)
	}
}

func TestUpdateDevice(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	for _, address := range []string{"192.0.2.1", "192.0.2.2"} {
		secret, _ := s.encrypt(`{"community":"keep-me"}`)
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential) VALUES(?,?,'swos','2c',?)", address, address, secret); err != nil {
			t.Fatal(err)
		}
	}
	update := func(id, body string) int {
		request := httptest.NewRequest(http.MethodPatch, "/api/devices/"+id, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("id", id)
		recorder := httptest.NewRecorder()
		a.updateDevice(recorder, request)
		return recorder.Code
	}
	if code := update("1", `{"address":"localhost","community":"","user":"","authPassword":"","privPassword":""}`); code != 200 {
		t.Fatalf("update: %d", code)
	}
	var address, resolved string
	var stored []byte
	if err := s.db.QueryRow("SELECT address,resolved,credential FROM devices WHERE id=1").Scan(&address, &resolved, &stored); err != nil {
		t.Fatal(err)
	}
	if plain, _ := s.decrypt(stored); address != "localhost" || resolved != "127.0.0.1" || plain != `{"community":"keep-me"}` {
		t.Fatalf("stored %q %q %q", address, resolved, plain)
	}
	if code := update("1", `{"address":"localhost","community":"new","user":"","authPassword":"","privPassword":""}`); code != 200 {
		t.Fatalf("update community: %d", code)
	}
	_ = s.db.QueryRow("SELECT credential FROM devices WHERE id=1").Scan(&stored)
	if plain, _ := s.decrypt(stored); plain != `{"community":"new"}` {
		t.Fatalf("community not changed: %q", plain)
	}
	if code := update("2", `{"address":"localhost","community":"","user":"","authPassword":"","privPassword":""}`); code != 409 {
		t.Fatalf("duplicate address: %d", code)
	}
	if code := update("9", `{"address":"192.0.2.9","community":"","user":"","authPassword":"","privPassword":""}`); code != 404 {
		t.Fatalf("missing device: %d", code)
	}
}

func TestRouterOSAuthProtocol(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	add := func(body string) int {
		request := httptest.NewRequest(http.MethodPost, "/api/devices", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.addDevice(recorder, request)
		return recorder.Code
	}
	body := func(address, protocol string) string {
		return `{"address":"` + address + `","os":"routeros","snmpVersion":"3","community":"","user":"trafficflow","authPassword":"auth-secret","privPassword":"priv-secret","authProtocol":"` + protocol + `"}`
	}
	if code := add(body("192.0.2.1", "SHA1")); code != 201 {
		t.Fatalf("SHA1: %d", code)
	}
	if code := add(body("192.0.2.2", "SHA512")); code != 400 {
		t.Fatalf("unsupported protocol: %d", code)
	}
	v2c := `{"address":"192.0.2.3","os":"routeros","snmpVersion":"2c","community":"c","user":"","authPassword":"","privPassword":"","authProtocol":""}`
	if code := add(v2c); code != 201 {
		t.Fatalf("RouterOS v2c: %d", code)
	}
	if code := add(strings.Replace(v2c, `"2c"`, `"1"`, 1)); code != 400 {
		t.Fatalf("RouterOS v1: %d", code)
	}
	swosV3 := strings.Replace(body("192.0.2.4", "SHA1"), "routeros", "swos", 1)
	if code := add(swosV3); code != 400 {
		t.Fatalf("SwOS v3: %d", code)
	}
	var stored []byte
	_ = s.db.QueryRow("SELECT credential FROM devices WHERE address='192.0.2.1'").Scan(&stored)
	plain, _ := s.decrypt(stored)
	var secret credential
	if err := json.Unmarshal([]byte(plain), &secret); err != nil || secret.AuthProtocol != "SHA1" {
		t.Fatalf("stored protocol %q %v", secret.AuthProtocol, err)
	}
	if authProtocols[""] != authProtocols["SHA256"] {
		t.Fatal("legacy credentials must keep SHA256")
	}
}

func TestSaveLayout(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	secret, _ := s.encrypt(`{"community":"c"}`)
	if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential) VALUES('a','192.0.2.1','swos','2c',?)", secret); err != nil {
		t.Fatal(err)
	}
	save := func(body string) int {
		request := httptest.NewRequest(http.MethodPut, "/api/layout", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.saveLayout(recorder, request)
		return recorder.Code
	}
	if code := save(`{"positions":[{"id":1,"x":12.5,"y":-40}]}`); code != 200 {
		t.Fatalf("save: %d", code)
	}
	var x, y float64
	if err := s.db.QueryRow("SELECT map_x,map_y FROM devices WHERE id=1").Scan(&x, &y); err != nil || x != 12.5 || y != -40 {
		t.Fatalf("stored %v %v %v", x, y, err)
	}
	if code := save(`{"positions":[{"id":1,"x":1e9,"y":0}]}`); code != 400 {
		t.Fatalf("huge position: %d", code)
	}
}

func TestDismissAndRestoreCandidate(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	secret, _ := s.encrypt(`{"community":"c"}`)
	if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential) VALUES('r','192.0.2.1','routeros','2c',?)", secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name) VALUES(1,1,'ether1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO candidates(local_interface_id,remote_port,remote_name,last_seen) VALUES(1,'ether2','other',?)", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	type result struct {
		Candidates          []candidateView `json:"candidates"`
		DismissedCandidates []dismissedView `json:"dismissedCandidates"`
	}
	topology := func() result {
		recorder := httptest.NewRecorder()
		a.topology(recorder, httptest.NewRequest("GET", "/api/topology", nil))
		var r result
		if err := json.Unmarshal(recorder.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	call := func(handler http.HandlerFunc, method, id string) int {
		request := httptest.NewRequest(method, "/", nil)
		request.SetPathValue("id", id)
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder.Code
	}
	if code := call(a.dismissCandidate, http.MethodPost, "1"); code != 200 {
		t.Fatalf("dismiss: %d", code)
	}
	if r := topology(); len(r.Candidates) != 0 || len(r.DismissedCandidates) != 1 {
		t.Fatalf("after dismiss: %+v", r)
	}
	// The suggestion is pruned and rediscovered with a new ID; the rejection must still apply.
	if _, err := s.db.Exec("DELETE FROM candidates"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO candidates(local_interface_id,remote_port,remote_name,last_seen) VALUES(1,'ether2','other',?)", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if r := topology(); len(r.Candidates) != 0 {
		t.Fatalf("rediscovered suggestion shown again: %+v", r)
	}
	if code := call(a.restoreCandidate, http.MethodDelete, "1"); code != 200 {
		t.Fatalf("restore: %d", code)
	}
	if r := topology(); len(r.Candidates) != 1 || len(r.DismissedCandidates) != 0 {
		t.Fatalf("after restore: %+v", r)
	}
	if code := call(a.dismissCandidate, http.MethodPost, "99"); code != 404 {
		t.Fatalf("missing suggestion: %d", code)
	}
}

func TestExternalCloud(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	send := func(handler http.HandlerFunc, method, id, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("id", id)
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder
	}
	if r := send(a.addExternal, http.MethodPost, "", `{"name":" ","capacityBps":0}`); r.Code != 400 {
		t.Fatalf("empty name: %d", r.Code)
	}
	if r := send(a.addExternal, http.MethodPost, "", `{"name":"Telia","capacityBps":1000000000}`); r.Code != 201 {
		t.Fatalf("add: %d %s", r.Code, r.Body.String())
	}
	var cloudPort, speed int64
	if err := s.db.QueryRow("SELECT i.id,i.speed_bps FROM interfaces i JOIN devices d ON d.id=i.device_id WHERE d.os='external'").Scan(&cloudPort, &speed); err != nil || speed != 1000000000 {
		t.Fatalf("cloud port: %v %v", speed, err)
	}
	// The cloud is the only device, so a poll that touched it would mark it offline.
	c := newCollector(s)
	c.pollAll(t.Context(), false, time.Now())
	var status string
	_ = s.db.QueryRow("SELECT status FROM devices WHERE id=1").Scan(&status)
	if status != "external" {
		t.Fatalf("cloud was polled: status %q", status)
	}
	secret, _ := s.encrypt(`{"community":"c"}`)
	if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential) VALUES('ccr','192.0.2.1','routeros','2c',?)", secret); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name,speed_bps,status,rx_bps,tx_bps,last_sample) VALUES(2,1,'sfp-upstream',10000000000,'up',300000000,100000000,?)", start.Unix())
	if err != nil {
		t.Fatal(err)
	}
	routerPort, _ := result.LastInsertId()
	if err := a.insertLink(t.Context(), routerPort, cloudPort, "manual"); err != nil {
		t.Fatal(err)
	}
	c.sampleLinks(t.Context(), start)
	var rx, tx float64
	if err := s.db.QueryRow("SELECT rx_bps,tx_bps FROM samples").Scan(&rx, &tx); err != nil {
		t.Fatalf("link not sampled from the router side: %v", err)
	}
	// The link is stored with the lower interface ID first: the cloud port, so rx/tx are seen from the cloud.
	if rx != 100000000 || tx != 300000000 {
		t.Fatalf("sample %v/%v", rx, tx)
	}
	if r := send(a.updateExternal, http.MethodPatch, "1", `{"name":"Arelion","capacityBps":0}`); r.Code != 200 {
		t.Fatalf("update: %d", r.Code)
	}
	if r := send(a.updateExternal, http.MethodPatch, "2", `{"name":"x","capacityBps":0}`); r.Code != 404 {
		t.Fatalf("update of a real device as cloud: %d", r.Code)
	}
	if r := send(a.updateDevice, http.MethodPatch, "1", `{"address":"192.0.2.9","community":"","user":"","authPassword":"","privPassword":"","authProtocol":""}`); r.Code != 400 {
		t.Fatalf("device update of a cloud: %d", r.Code)
	}
	var kind string
	if err := s.db.QueryRow("SELECT kind FROM devices WHERE id=1").Scan(&kind); err != nil || kind != "cloud" {
		t.Fatalf("kind without kind in the request: %q %v", kind, err)
	}
}

func TestExternalServer(t *testing.T) {
	s := testStore(t)
	a := &server{store: s}
	send := func(handler http.HandlerFunc, method, id, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("id", id)
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder
	}
	if r := send(a.addExternal, http.MethodPost, "", `{"name":"fil-01","kind":"printer","capacityBps":0}`); r.Code != 400 {
		t.Fatalf("unknown kind: %d", r.Code)
	}
	if r := send(a.addExternal, http.MethodPost, "", `{"name":"fil-01","kind":"server","capacityBps":1000000000}`); r.Code != 201 {
		t.Fatalf("add: %d %s", r.Code, r.Body.String())
	}
	// An update without kind keeps the server a server.
	if r := send(a.updateExternal, http.MethodPatch, "1", `{"name":"fil-02","capacityBps":10000000000}`); r.Code != 200 {
		t.Fatalf("update: %d", r.Code)
	}
	var name, kind, status string
	var speed int64
	if err := s.db.QueryRow("SELECT d.name,d.kind,d.status,i.speed_bps FROM devices d JOIN interfaces i ON i.device_id=d.id").Scan(&name, &kind, &status, &speed); err != nil {
		t.Fatal(err)
	}
	if name != "fil-02" || kind != "server" || status != "external" || speed != 10000000000 {
		t.Fatalf("server %q %q %q %d", name, kind, status, speed)
	}
	recorder := httptest.NewRecorder()
	a.topology(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(recorder.Body.String(), `"kind":"server"`) {
		t.Fatalf("topology lacks kind: %s", recorder.Body.String())
	}
}

func TestSuffixMac(t *testing.T) {
	if got := suffixMac("4.244.28.191.218.233"); got != "04:F4:1C:BF:DA:E9" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"1.2.3", "1.2.3.4.5.256", "a.b.c.d.e.f"} {
		if suffixMac(bad) != "" {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestForwardingDiscovery(t *testing.T) {
	s := testStore(t)
	secret, _ := s.encrypt(`{"community":"test"}`)
	// Chain: core(1) port 2 -> middle(2) port 1, middle(2) port 2 -> edge(3) port 1.
	for device := 1; device <= 3; device++ {
		address := "192.0.2." + string(rune('0'+device))
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential) VALUES(?,?,'swos','2c',?)", address, address, secret); err != nil {
			t.Fatal(err)
		}
		for index := 1; index <= 2; index++ {
			mac := "02:00:00:00:0" + string(rune('0'+device)) + ":0" + string(rune('0'+index))
			if _, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name,mac) VALUES(?,?,?,?)", device, index, "port", mac); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.db.Exec("INSERT INTO device_macs(device_id,mac) VALUES(3,'02:00:00:00:03:00')"); err != nil {
		t.Fatal(err)
	}
	learned := []struct {
		iface int64
		mac   string
	}{
		{2, "02:00:00:00:02:01"}, {2, "02:00:00:00:03:00"}, // core port 2 sees middle and edge
		{3, "02:00:00:00:01:01"},                           // middle port 1 sees core
		{4, "02:00:00:00:03:00"},                           // middle port 2 sees edge
		{5, "02:00:00:00:01:01"}, {5, "02:00:00:00:02:02"}, // edge port 1 sees core and middle
	}
	for _, x := range learned {
		if _, err := s.db.Exec("INSERT INTO fdb(interface_id,mac,last_seen) VALUES(?,?,?)", x.iface, x.mac, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	// A link created before the middle device was registered: core port 2 straight to edge port 1.
	if _, err := s.db.Exec("INSERT INTO links(a_interface_id,b_interface_id,source) VALUES(2,5,'mac')"); err != nil {
		t.Fatal(err)
	}
	c := newCollector(s)
	c.reconcileForwarding(t.Context())
	rows, err := s.db.Query("SELECT a_interface_id,b_interface_id,source FROM links ORDER BY a_interface_id")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var a, b int64
		var source string
		rows.Scan(&a, &b, &source)
		got = append(got, fmt.Sprintf("%d-%d-%s", a, b, source))
	}
	rows.Close()
	if strings.Join(got, ",") != "2-3-mac,4-5-mac" {
		t.Fatalf("links: %v", got)
	}
	var id string
	if err := s.db.QueryRow("SELECT id FROM links WHERE a_interface_id=2").Scan(&id); err != nil {
		t.Fatal(err)
	}
	a := &server{store: s}
	request := httptest.NewRequest(http.MethodDelete, "/api/links/"+id, nil)
	request.SetPathValue("id", id)
	response := httptest.NewRecorder()
	a.deleteLink(response, request)
	if response.Code != 200 {
		t.Fatalf("delete: %d", response.Code)
	}
	c.reconcileForwarding(t.Context())
	var links int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM links").Scan(&links)
	if links != 1 {
		t.Fatalf("removed link was recreated: %d links", links)
	}
}

func TestReciprocalDiscoveryAndSuppression(t *testing.T) {
	s := testStore(t)
	secret, _ := s.encrypt(`{"community":"test"}`)
	for _, address := range []string{"192.0.2.1", "192.0.2.2"} {
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential) VALUES(?,?,'routeros','3',?)", address, address, secret); err != nil {
			t.Fatal(err)
		}
	}
	for _, device := range []int{1, 2} {
		if _, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name) VALUES(?,1,'ether1')", device); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO candidates(local_interface_id,remote_device_id,remote_port,remote_name,last_seen) VALUES(1,2,'ether1','192.0.2.2',?)", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	c := newCollector(s)
	c.reconcile(t.Context())
	var links int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM links").Scan(&links)
	if links != 0 {
		t.Fatal("one-way discovery created a link")
	}
	if _, err := s.db.Exec("INSERT INTO candidates(local_interface_id,remote_device_id,remote_port,remote_name,last_seen) VALUES(2,1,'ether1','192.0.2.1',?)", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	c.reconcile(t.Context())
	_ = s.db.QueryRow("SELECT COUNT(*) FROM links").Scan(&links)
	if links != 1 {
		t.Fatal("reciprocal discovery did not create a link")
	}
	a := &server{store: s}
	request := httptest.NewRequest(http.MethodDelete, "/api/links/1", nil)
	request.SetPathValue("id", "1")
	response := httptest.NewRecorder()
	a.deleteLink(response, request)
	if response.Code != 200 {
		t.Fatalf("delete: %d", response.Code)
	}
	c.reconcile(t.Context())
	_ = s.db.QueryRow("SELECT COUNT(*) FROM links").Scan(&links)
	if links != 0 {
		t.Fatal("dismissed discovery recreated a link")
	}
}
