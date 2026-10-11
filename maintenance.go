package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// A maintenance window silences new alarms, for one device or all of them. A one-time window runs from StartsAt to EndsAt; a
// scheduled one repeats on the weekdays in Days from StartMin to EndMin minutes past midnight
// local time, and ends the next day when EndMin is not after StartMin.
type maintenanceWindow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	DeviceID *int64 `json:"deviceId"` // nil covers every device
	StartsAt *int64 `json:"startsAt"`
	EndsAt   *int64 `json:"endsAt"`
	Days     []int  `json:"days"` // 0 = Sunday, as time.Weekday
	StartMin int    `json:"startMin"`
	EndMin   int    `json:"endMin"`
}

func (m *maintenanceWindow) validate(now int64) error {
	m.Name = strings.TrimSpace(m.Name)
	if len([]rune(m.Name)) > 80 {
		return errors.New("Namnet får vara högst 80 tecken")
	}
	if m.StartsAt != nil || m.EndsAt != nil {
		if m.StartsAt == nil || m.EndsAt == nil || *m.EndsAt <= *m.StartsAt {
			return errors.New("Slutet ska vara efter starten")
		}
		if *m.EndsAt <= now {
			return errors.New("Fönstret har redan passerat")
		}
		if *m.EndsAt-*m.StartsAt > 31*86400 {
			return errors.New("Ett underhållsfönster får vara högst 31 dygn")
		}
		m.Days, m.StartMin, m.EndMin = []int{}, 0, 0
		return nil
	}
	seen := map[int]bool{}
	days := []int{}
	for _, d := range m.Days {
		if d < 0 || d > 6 {
			return errors.New("Ogiltig veckodag")
		}
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	if len(days) == 0 {
		return errors.New("Välj minst en veckodag")
	}
	if m.StartMin < 0 || m.StartMin >= 1440 || m.EndMin < 0 || m.EndMin >= 1440 || m.StartMin == m.EndMin {
		return errors.New("Ange en start- och sluttid som skiljer sig åt")
	}
	m.Days = days
	return nil
}

// activeUntil reports when the window ends if it covers t.
func (m maintenanceWindow) activeUntil(t time.Time) (time.Time, bool) {
	if m.StartsAt != nil && m.EndsAt != nil {
		if t.Unix() >= *m.StartsAt && t.Unix() < *m.EndsAt {
			return time.Unix(*m.EndsAt, 0), true
		}
		return time.Time{}, false
	}
	t = t.In(time.Local)
	// An overnight window that started yesterday may still be running.
	for _, offset := range []int{0, -1} {
		day := time.Date(t.Year(), t.Month(), t.Day()+offset, 0, 0, 0, 0, time.Local)
		if !containsDay(m.Days, int(day.Weekday())) {
			continue
		}
		start := time.Date(day.Year(), day.Month(), day.Day(), m.StartMin/60, m.StartMin%60, 0, 0, time.Local)
		end := time.Date(day.Year(), day.Month(), day.Day(), m.EndMin/60, m.EndMin%60, 0, 0, time.Local)
		if m.EndMin <= m.StartMin {
			end = time.Date(day.Year(), day.Month(), day.Day()+1, m.EndMin/60, m.EndMin%60, 0, 0, time.Local)
		}
		if !t.Before(start) && t.Before(end) {
			return end, true
		}
	}
	return time.Time{}, false
}

func containsDay(days []int, d int) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

func (s *store) maintenanceWindows(ctx context.Context) ([]maintenanceWindow, error) {
	list := []maintenanceWindow{}
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,device_id,starts_at,ends_at,days,start_min,end_min FROM maintenance ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m maintenanceWindow
		var days int
		if err := rows.Scan(&m.ID, &m.Name, &m.DeviceID, &m.StartsAt, &m.EndsAt, &days, &m.StartMin, &m.EndMin); err != nil {
			return nil, err
		}
		m.Days = []int{}
		for d := 0; d < 7; d++ {
			if days&(1<<d) != 0 {
				m.Days = append(m.Days, d)
			}
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// maintenanceUntil reports when maintenance covering deviceID ends, or zero when none is running.
// Device 0 means only windows for every device count. Overlapping or back-to-back windows count
// as one, so the end is where alarms resume.
func maintenanceUntil(windows []maintenanceWindow, now time.Time, deviceID int64) time.Time {
	var until time.Time
	at := now
	for range 32 {
		extended := false
		for _, m := range windows {
			if m.DeviceID != nil && *m.DeviceID != deviceID {
				continue
			}
			if end, ok := m.activeUntil(at); ok && end.After(until) {
				until, extended = end, true
			}
		}
		if !extended {
			break
		}
		at = until
	}
	return until
}

type maintenanceView struct {
	Windows []maintenanceWindow `json:"windows"`
	Until   int64               `json:"until"` // 0 when no maintenance is running
}

func (a *server) maintenance(w http.ResponseWriter, r *http.Request) {
	windows, err := a.store.maintenanceWindows(r.Context())
	if err != nil {
		writeError(w, 500, "Kunde inte läsa underhållsfönster")
		return
	}
	view := maintenanceView{Windows: windows}
	if until := maintenanceUntil(windows, time.Now(), 0); !until.IsZero() {
		view.Until = until.Unix()
	}
	writeJSON(w, 200, view)
}

func (a *server) addMaintenance(w http.ResponseWriter, r *http.Request) {
	var input maintenanceWindow
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := input.validate(time.Now().Unix()); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if input.DeviceID != nil {
		var exists bool
		if err := a.store.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM devices WHERE id=? AND os!='external')", *input.DeviceID).Scan(&exists); err != nil || !exists {
			writeError(w, 400, "Enheten finns inte")
			return
		}
	}
	days := 0
	for _, d := range input.Days {
		days |= 1 << d
	}
	if _, err := a.store.db.ExecContext(r.Context(), "INSERT INTO maintenance(name,device_id,starts_at,ends_at,days,start_min,end_min) VALUES(?,?,?,?,?,?,?)",
		input.Name, input.DeviceID, input.StartsAt, input.EndsAt, days, input.StartMin, input.EndMin); err != nil {
		writeError(w, 500, "Kunde inte spara underhållsfönstret")
		return
	}
	a.maintenance(w, r)
}

func (a *server) deleteMaintenance(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if _, err := a.store.db.ExecContext(r.Context(), "DELETE FROM maintenance WHERE id=?", id); err != nil {
		writeError(w, 500, "Kunde inte ta bort underhållsfönstret")
		return
	}
	a.maintenance(w, r)
}
