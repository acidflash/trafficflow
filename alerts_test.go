package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testAlerter records what would have been delivered instead of sending it.
func testAlerter(s *store) (*alerter, func() [][]alertEvent) {
	a := newAlerter(s)
	var mu sync.Mutex
	var sent [][]alertEvent
	a.send = func(_ context.Context, _ alertConfig, events []alertEvent) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, events)
		return nil
	}
	return a, func() [][]alertEvent {
		a.wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		out := sent
		sent = nil
		return out
	}
}

func TestAlertDeviceHoldDownAndRecovery(t *testing.T) {
	s := testStore(t)
	secret, _ := s.encrypt(`{"community":"c"}`)
	if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential,status) VALUES('sw1','192.0.2.1','swos','2c',?,'offline')", secret); err != nil {
		t.Fatal(err)
	}
	a, sent := testAlerter(s)
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm after one miss: %v", got)
	}
	a.evaluate(t.Context())
	got := sent()
	if len(got) != 1 || len(got[0]) != 1 || !got[0][0].Down || got[0][0].Title != "sw1" {
		t.Fatalf("want one down event, got %v", got)
	}
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("repeated alarm: %v", got)
	}
	// A restart forgets the miss counters but not the open alarm.
	a, sent = testAlerter(s)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm repeated after restart: %v", got)
	}
	_, _ = s.db.Exec("UPDATE devices SET status='online'")
	a.evaluate(t.Context())
	got = sent()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0].Down {
		t.Fatalf("want one recovery event, got %v", got)
	}
	var open int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM alarms WHERE cleared_at IS NULL").Scan(&open)
	if open != 0 {
		t.Fatalf("%d alarms still open", open)
	}
}

func TestAlertWatchedPorts(t *testing.T) {
	s := testStore(t)
	srv := &server{store: s}
	secret, _ := s.encrypt(`{"community":"c"}`)
	for _, row := range []struct{ name, address, status string }{{"core", "192.0.2.1", "online"}, {"edge", "192.0.2.2", "online"}, {"dead", "192.0.2.3", "offline"}} {
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential,status) VALUES(?,?,'swos','2c',?,?)", row.name, row.address, secret, row.status); err != nil {
			t.Fatal(err)
		}
	}
	for index, row := range []struct {
		device int
		name   string
		status string
	}{
		{1, "uplink", "down"}, // 1: link port, watched
		{2, "uplink", "up"},   // 2: link peer
		{1, "access", "down"}, // 3: unwatched
		{1, "customer", "up"}, // 4: watched by hand
		{3, "uplink", "down"}, // 5: link port on a device that does not answer
		{2, "downlink", "up"}, // 6: link peer
	} {
		if _, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name,status) VALUES(?,?,?,?)", row.device, index+1, row.name, row.status); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]int64{{1, 2}, {5, 6}} {
		if err := srv.insertLink(t.Context(), pair[0], pair[1], "manual"); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/interfaces/4", strings.NewReader(`{"alert":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("id", "4")
	w := httptest.NewRecorder()
	srv.updateInterface(w, r)
	if w.Code != 200 {
		t.Fatalf("enable alert: %d %s", w.Code, w.Body.String())
	}
	_, _ = s.db.Exec("UPDATE interfaces SET status='down' WHERE id=4")
	a, sent := testAlerter(s)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	got := sent()
	if len(got) != 1 {
		t.Fatalf("want one batched message, got %d", len(got))
	}
	titles := map[string]bool{}
	for _, e := range got[0] {
		titles[e.Title] = true
	}
	want := map[string]bool{"core / uplink": true, "core / customer": true, "dead": true}
	if len(titles) != len(want) {
		t.Fatalf("events %v, want %v", titles, want)
	}
	for title := range want {
		if !titles[title] {
			t.Fatalf("missing %q in %v", title, titles)
		}
	}
	// Turning the alert off closes the alarm without a notification.
	_, _ = s.db.Exec("UPDATE interfaces SET alert=0 WHERE id=4")
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("unwatched port notified: %v", got)
	}
	var open int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM alarms WHERE cleared_at IS NULL AND interface_id=4").Scan(&open)
	if open != 0 {
		t.Fatal("alarm for unwatched port still open")
	}
}

func TestSendDiscord(t *testing.T) {
	var body map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	events := []alertEvent{{Down: true, Title: "core / uplink", Address: "192.0.2.1", Since: 1, At: 1}}
	if err := sendDiscord(t.Context(), hook.URL, discordText(events)); err != nil {
		t.Fatal(err)
	}
	if content, _ := body["content"].(string); !strings.Contains(content, "NERE") || !strings.Contains(content, "core / uplink") {
		t.Fatalf("content %q", body["content"])
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer failing.Close()
	if err := sendDiscord(t.Context(), failing.URL, "x"); err == nil {
		t.Fatal("404 from webhook not reported")
	}
}

func TestAlertSettingsSecrets(t *testing.T) {
	s := testStore(t)
	srv := &server{store: s}
	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/alert-settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.saveAlertSettings(w, r)
		return w
	}
	if w := put(`{"discordEnabled":true,"discordWebhook":"https://evil.example/api/webhooks/1/x","smtpPort":587,"smtpSecurity":"starttls","to":[]}`); w.Code != 400 {
		t.Fatalf("foreign webhook accepted: %d", w.Code)
	}
	if w := put(`{"emailEnabled":true,"smtpHost":"mail.example.com","smtpPort":587,"smtpSecurity":"none","smtpUser":"u","smtpPassword":"p","from":"a@example.com","to":["b@example.com"]}`); w.Code != 400 {
		t.Fatalf("plaintext login accepted: %d", w.Code)
	}
	w := put(`{"discordEnabled":true,"discordWebhook":"https://discord.com/api/webhooks/1/hooksecret","emailEnabled":true,"smtpHost":"mail.example.com","smtpPort":587,"smtpSecurity":"starttls","smtpUser":"u","smtpPassword":"mailsecret","from":"Ops <ops@example.com>","to":["noc@example.com"," "]}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "hooksecret") || strings.Contains(w.Body.String(), "mailsecret") {
		t.Fatalf("secret returned: %s", w.Body.String())
	}
	var raw []byte
	_ = s.db.QueryRow("SELECT value FROM settings WHERE key='alerts'").Scan(&raw)
	if bytes.Contains(raw, []byte("hooksecret")) || bytes.Contains(raw, []byte("mailsecret")) {
		t.Fatal("settings stored in plaintext")
	}
	// Saving again without the secrets keeps them.
	if w := put(`{"discordEnabled":true,"emailEnabled":true,"smtpHost":"mail.example.com","smtpPort":587,"smtpSecurity":"starttls","smtpUser":"u","from":"ops@example.com","to":["noc@example.com"]}`); w.Code != 200 {
		t.Fatalf("resave: %d %s", w.Code, w.Body.String())
	}
	cfg, err := s.loadAlertConfig(t.Context())
	if err != nil || cfg.DiscordWebhook != "https://discord.com/api/webhooks/1/hooksecret" || cfg.SMTPPassword != "mailsecret" || cfg.From != "ops@example.com" || len(cfg.To) != 1 {
		t.Fatalf("config %+v %v", cfg, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/alert-settings", nil)
	w = httptest.NewRecorder()
	srv.alertSettings(w, r)
	var view map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if view["hasDiscordWebhook"] != true || view["hasSmtpPassword"] != true || view["discordWebhook"] != nil || view["smtpPassword"] != nil {
		t.Fatalf("view %v", view)
	}
}

func TestAlertTestChannel(t *testing.T) {
	s := testStore(t)
	srv := &server{store: s}
	test := func(channel string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/alert-settings/test", strings.NewReader(`{"channel":"`+channel+`"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.testAlertSettings(w, r)
		return w
	}
	if w := test("discord"); w.Code != 400 {
		t.Fatalf("unconfigured discord: %d", w.Code)
	}
	if w := test("email"); w.Code != 400 {
		t.Fatalf("unconfigured email: %d", w.Code)
	}
	if w := test("sms"); w.Code != 400 {
		t.Fatalf("unknown channel: %d", w.Code)
	}
	calls := 0
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) }))
	defer hook.Close()
	// Stored directly: validation only accepts discord.com, and the test works even while disabled.
	if err := s.saveAlertConfig(t.Context(), alertConfig{DiscordWebhook: hook.URL, SMTPPort: 587, SMTPSecurity: "starttls"}); err != nil {
		t.Fatal(err)
	}
	if w := test("discord"); w.Code != 200 || calls != 1 {
		t.Fatalf("discord test: %d %s, %d calls", w.Code, w.Body.String(), calls)
	}
}

func TestAlertInterfaceErrors(t *testing.T) {
	s := testStore(t)
	srv := &server{store: s}
	secret, _ := s.encrypt(`{"community":"c"}`)
	for _, address := range []string{"192.0.2.1", "192.0.2.2"} {
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential,status) VALUES(?,?,'swos','2c',?,'online')", "sw-"+address[len(address)-1:], address, secret); err != nil {
			t.Fatal(err)
		}
	}
	for index, device := range []int{1, 2, 1} {
		if _, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name,status) VALUES(?,?,?,'up')", device, index+1, fmt.Sprintf("ether%d", index+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.insertLink(t.Context(), 1, 2, "manual"); err != nil {
		t.Fatal(err)
	}
	setErrors := func(rx, tx float64) {
		t.Helper()
		// Interface 3 is not watched, so its errors never alarm.
		if _, err := s.db.Exec("UPDATE interfaces SET rx_errors=?,tx_errors=?,last_sample=?", rx, tx, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	a, sent := testAlerter(s)
	setErrors(3, 2)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm below the threshold: %v", got)
	}
	setErrors(90, 0)
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm after one poll: %v", got)
	}
	a.evaluate(t.Context())
	got := sent()
	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("want both link ends in one message, got %v", got)
	}
	for _, e := range got[0] {
		if !e.Down || e.Kind != "errors" || !strings.Contains(e.Detail, "RX 90") {
			t.Fatalf("unexpected event %+v", e)
		}
	}
	if subject := alertSubject(got[0][:1]); !strings.Contains(subject, "fel") {
		t.Fatalf("subject %q", subject)
	}
	// A short pause in the errors keeps the alarm open.
	setErrors(0, 0)
	for range errorClearAfter - 1 {
		a.evaluate(t.Context())
	}
	setErrors(90, 0)
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm flapped: %v", got)
	}
	setErrors(0, 0)
	for range errorClearAfter {
		a.evaluate(t.Context())
	}
	got = sent()
	if len(got) != 1 || len(got[0]) != 2 || got[0][0].Down {
		t.Fatalf("want two recovery events, got %v", got)
	}
	// Stale measurements say nothing, and a threshold of 0 turns error alarms off.
	_, _ = s.db.Exec("UPDATE interfaces SET rx_errors=500,last_sample=?", time.Now().Unix()-600)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm on stale sample: %v", got)
	}
	if err := s.saveAlertConfig(t.Context(), alertConfig{SMTPPort: 587, SMTPSecurity: "starttls", To: []string{}}); err != nil {
		t.Fatal(err)
	}
	setErrors(500, 500)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm with error alarms off: %v", got)
	}
}

func TestAlertRxPause(t *testing.T) {
	s := testStore(t)
	srv := &server{store: s}
	secret, _ := s.encrypt(`{"community":"c"}`)
	for _, address := range []string{"192.0.2.1", "192.0.2.2"} {
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential,status) VALUES(?,?,'swos','2c',?,'online')", "sw-"+address[len(address)-1:], address, secret); err != nil {
			t.Fatal(err)
		}
	}
	for index, device := range []int{1, 2} {
		if _, err := s.db.Exec("INSERT INTO interfaces(device_id,if_index,name,status) VALUES(?,?,?,'up')", device, index+1, fmt.Sprintf("ether%d", index+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.insertLink(t.Context(), 1, 2, "manual"); err != nil {
		t.Fatal(err)
	}
	setPause := func(id int, perMinute float64) {
		t.Helper()
		if _, err := s.db.Exec("UPDATE interfaces SET rx_pause=?,last_sample=? WHERE id=?", perMinute, time.Now().Unix(), id); err != nil {
			t.Fatal(err)
		}
	}
	a, sent := testAlerter(s)
	setPause(1, 100)
	setPause(2, 0)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm below the threshold: %v", got)
	}
	setPause(1, 9000)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	got := sent()
	if len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("want one pause alarm, got %v", got)
	}
	if e := got[0][0]; !e.Down || e.Kind != "pause" || e.Title != "sw-1 / ether1" || !strings.Contains(e.Detail, "9000") {
		t.Fatalf("unexpected event %+v", e)
	}
	if subject := alertSubject(got[0]); !strings.Contains(subject, "pause") {
		t.Fatalf("subject %q", subject)
	}
	setPause(1, 0)
	for range errorClearAfter {
		a.evaluate(t.Context())
	}
	if got := sent(); len(got) != 1 || len(got[0]) != 1 || got[0][0].Down || got[0][0].Kind != "pause" {
		t.Fatalf("want one recovery event, got %v", got)
	}
}

func TestErrorRate(t *testing.T) {
	if v, ok := errorRate(100, 130, 15*time.Second, true); !ok || v != 120 {
		t.Fatalf("got %v %v, want 120/min", v, ok)
	}
	if _, ok := errorRate(100, 5, 15*time.Second, true); ok {
		t.Fatal("cleared counter accepted")
	}
	if _, ok := errorRate(100, 130, 15*time.Second, false); ok {
		t.Fatal("rebooted device accepted")
	}
}
