package main

import (
	"fmt"
	"testing"
	"time"
)

func TestMaintenanceActiveUntil(t *testing.T) {
	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	saved := time.Local
	time.Local = stockholm
	t.Cleanup(func() { time.Local = saved })
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, stockholm)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Sundays 23:00–02:00; 2026-10-11 is a Sunday.
	overnight := maintenanceWindow{Days: []int{0}, StartMin: 23 * 60, EndMin: 2 * 60}
	for _, c := range []struct {
		at     string
		active bool
		until  string
	}{
		{"2026-10-11 22:59", false, ""},
		{"2026-10-11 23:00", true, "2026-10-12 02:00"},
		{"2026-10-12 01:59", true, "2026-10-12 02:00"},
		{"2026-10-12 02:00", false, ""},
		{"2026-10-12 23:30", false, ""}, // Monday
	} {
		until, ok := overnight.activeUntil(at(c.at))
		if ok != c.active || (ok && !until.Equal(at(c.until))) {
			t.Errorf("%s: got %v %v, want %v %s", c.at, ok, until, c.active, c.until)
		}
	}
	start, end := at("2026-10-11 10:00").Unix(), at("2026-10-11 11:00").Unix()
	once := maintenanceWindow{StartsAt: &start, EndsAt: &end}
	if _, ok := once.activeUntil(at("2026-10-11 09:59")); ok {
		t.Error("one-time window active before start")
	}
	if until, ok := once.activeUntil(at("2026-10-11 10:30")); !ok || until.Unix() != end {
		t.Errorf("one-time window: %v %v", until, ok)
	}
	if _, ok := once.activeUntil(at("2026-10-11 11:00")); ok {
		t.Error("one-time window active at its end")
	}
}

func TestMaintenanceValidate(t *testing.T) {
	now := time.Now().Unix()
	past, later := now-3600, now+3600
	for _, m := range []maintenanceWindow{
		{},
		{Days: []int{1}, StartMin: 60, EndMin: 60},
		{Days: []int{7}, StartMin: 60, EndMin: 120},
		{Days: []int{1}, StartMin: 60, EndMin: 1440},
		{StartsAt: &later, EndsAt: &now},
		{StartsAt: &past, EndsAt: &past},
		{StartsAt: &now},
	} {
		if err := m.validate(now); err == nil {
			t.Errorf("accepted %+v", m)
		}
	}
	ok := maintenanceWindow{Days: []int{1, 1, 3}, StartMin: 22 * 60, EndMin: 60}
	if err := ok.validate(now); err != nil || len(ok.Days) != 2 {
		t.Errorf("%v %v", err, ok.Days)
	}
}

func TestMaintenanceSilencesAlarms(t *testing.T) {
	s := testStore(t)
	secret, _ := s.encrypt(`{"community":"c"}`)
	if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential,status) VALUES('sw1','192.0.2.1','swos','2c',?,'offline'),('sw2','192.0.2.2','swos','2c',?,'offline')", secret, secret); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	start, end := now-60, now+3600
	if _, err := s.db.Exec("INSERT INTO maintenance(starts_at,ends_at) VALUES(?,?)", start, end); err != nil {
		t.Fatal(err)
	}
	a, sent := testAlerter(s)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("alarm sent during maintenance: %v", got)
	}
	var quiet int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM alarms WHERE cleared_at IS NULL AND quiet=1").Scan(&quiet)
	if quiet != 2 {
		t.Fatalf("want 2 quiet alarms, got %d", quiet)
	}
	// sw1 comes back during maintenance; nobody was told it went down.
	_, _ = s.db.Exec("UPDATE devices SET status='online' WHERE name='sw1'")
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("recovery sent for a quiet alarm: %v", got)
	}
	// Maintenance ends with sw2 still down, so it is sent once with its original start.
	_, _ = s.db.Exec("DELETE FROM maintenance")
	a.evaluate(t.Context())
	got := sent()
	if len(got) != 1 || len(got[0]) != 1 || !got[0][0].Down || got[0][0].Title != "sw2" || got[0][0].Since > now {
		t.Fatalf("want sw2 down after maintenance, got %v", got)
	}
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("repeated alarm: %v", got)
	}
	_, _ = s.db.Exec("UPDATE devices SET status='online'")
	a.evaluate(t.Context())
	if got := sent(); len(got) != 1 || got[0][0].Down {
		t.Fatalf("want sw2 recovery, got %v", got)
	}
}

func TestMaintenanceBackToBack(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	a1, a2, b2 := now.Unix()-60, now.Unix()+600, now.Unix()+1200
	_, _ = s.db.Exec("INSERT INTO maintenance(starts_at,ends_at) VALUES(?,?),(?,?)", a1, a2, a2, b2)
	windows, err := s.maintenanceWindows(t.Context())
	if until := maintenanceUntil(windows, now, 0); err != nil || until.Unix() != b2 {
		t.Fatalf("want until %d, got %v %v", b2, until.Unix(), err)
	}
}

func TestMaintenanceForOneDevice(t *testing.T) {
	s := testStore(t)
	secret, _ := s.encrypt(`{"community":"c"}`)
	// core (1) links to sw1 (2); sw2 (3) is unrelated. sw1 goes into maintenance and reboots.
	for i, name := range []string{"core", "sw1", "sw2"} {
		if _, err := s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential,status) VALUES(?,?,'swos','2c',?,'online')", name, fmt.Sprintf("192.0.2.%d", i+1), secret); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.db.Exec("INSERT INTO interfaces(device_id,if_index,name,status,first_up) VALUES(1,1,'ether1','up',1),(2,1,'ether1','up',1)")
	_, _ = s.db.Exec("INSERT INTO links(a_interface_id,b_interface_id,source) VALUES(1,2,'manual')")
	now := time.Now().Unix()
	_, _ = s.db.Exec("INSERT INTO maintenance(device_id,starts_at,ends_at) VALUES(2,?,?)", now-60, now+3600)
	_, _ = s.db.Exec("UPDATE devices SET status='offline' WHERE name IN ('sw1','sw2')")
	_, _ = s.db.Exec("UPDATE interfaces SET status='down' WHERE device_id=1")
	a, sent := testAlerter(s)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	got := sent()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0].Title != "sw2" {
		t.Fatalf("want only sw2 sent, got %v", got)
	}
	var quiet int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM alarms WHERE quiet=1").Scan(&quiet)
	if quiet != 2 { // sw1 itself and core's port towards it
		t.Fatalf("want 2 quiet alarms, got %d", quiet)
	}
	windows, _ := s.maintenanceWindows(t.Context())
	if !maintenanceUntil(windows, time.Now(), 0).IsZero() || maintenanceUntil(windows, time.Now(), 2).Unix() != now+3600 {
		t.Fatal("device window counted for the wrong device")
	}
}

func TestMaintenanceForServer(t *testing.T) {
	s := testStore(t)
	secret, _ := s.encrypt(`{"community":"c"}`)
	_, _ = s.db.Exec("INSERT INTO devices(name,address,os,snmp_version,credential,status) VALUES('core','192.0.2.1','swos','2c',?,'online')", secret)
	_, _ = s.db.Exec("INSERT INTO devices(name,address,os,kind,snmp_version,credential,status) VALUES('nas','external:1','external','server','',?,'unknown')", secret)
	_, _ = s.db.Exec("INSERT INTO interfaces(device_id,if_index,name,status) VALUES(1,1,'ether1','down'),(2,1,'Anslutning','unknown')")
	_, _ = s.db.Exec("INSERT INTO links(a_interface_id,b_interface_id,source) VALUES(1,2,'manual')")
	now := time.Now().Unix()
	_, _ = s.db.Exec("INSERT INTO maintenance(device_id,starts_at,ends_at) VALUES(2,?,?)", now-60, now+3600)
	a, sent := testAlerter(s)
	a.evaluate(t.Context())
	a.evaluate(t.Context())
	if got := sent(); len(got) != 0 {
		t.Fatalf("port towards a server in maintenance alarmed: %v", got)
	}
	var quiet int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM alarms WHERE kind='port' AND quiet=1").Scan(&quiet)
	if quiet != 1 {
		t.Fatalf("want the core port quiet, got %d", quiet)
	}
}
