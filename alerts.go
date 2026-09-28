package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // alert times are shown in TZ, and the runtime image has no zoneinfo
)

// alertAfterMisses is how many polls in a row a device or port must be down before it alarms,
// so a single lost SNMP packet does not wake anyone.
const alertAfterMisses = 2

type alertConfig struct {
	DiscordEnabled bool   `json:"discordEnabled"`
	DiscordWebhook string `json:"discordWebhook,omitempty"`
	EmailEnabled   bool   `json:"emailEnabled"`
	SMTPHost       string `json:"smtpHost"`
	SMTPPort       int    `json:"smtpPort"`
	// SMTPSecurity is starttls (usually port 587), tls (465) or none.
	SMTPSecurity string   `json:"smtpSecurity"`
	SMTPUser     string   `json:"smtpUser"`
	SMTPPassword string   `json:"smtpPassword,omitempty"`
	From         string   `json:"from"`
	To           []string `json:"to"`
}

func (s *store) loadAlertConfig(ctx context.Context) (alertConfig, error) {
	cfg := alertConfig{SMTPPort: 587, SMTPSecurity: "starttls", To: []string{}}
	var value []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='alerts'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	plain, err := s.decrypt(value)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(plain), &cfg)
	if cfg.To == nil {
		cfg.To = []string{}
	}
	return cfg, err
}

func (s *store) saveAlertConfig(ctx context.Context, cfg alertConfig) error {
	encoded, _ := json.Marshal(cfg)
	encrypted, err := s.encrypt(string(encoded))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('alerts',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encrypted)
	return err
}

func validWebhook(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || !strings.HasPrefix(u.Path, "/api/webhooks/") {
		return false
	}
	switch u.Host {
	case "discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com":
		return true
	}
	return false
}

// validate normalizes the configuration and rejects anything that could not be delivered.
func (c *alertConfig) validate() error {
	c.DiscordWebhook = strings.TrimSpace(c.DiscordWebhook)
	if c.DiscordWebhook != "" && !validWebhook(c.DiscordWebhook) {
		return errors.New("Discord-webhooken ska börja med https://discord.com/api/webhooks/")
	}
	if c.DiscordEnabled && c.DiscordWebhook == "" {
		return errors.New("Ange en Discord-webhook")
	}
	c.SMTPHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.SMTPHost)), ".")
	if c.SMTPHost != "" && net.ParseIP(c.SMTPHost) == nil && !validHostname(c.SMTPHost) {
		return errors.New("Ogiltig SMTP-server")
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return errors.New("SMTP-porten ska vara 1–65535")
	}
	if c.SMTPSecurity != "starttls" && c.SMTPSecurity != "tls" && c.SMTPSecurity != "none" {
		return errors.New("Välj STARTTLS, TLS eller ingen kryptering")
	}
	c.SMTPUser = strings.TrimSpace(c.SMTPUser)
	if c.SMTPUser != "" && c.SMTPSecurity == "none" {
		return errors.New("Inloggning kräver STARTTLS eller TLS så att lösenordet inte skickas i klartext")
	}
	c.From = strings.TrimSpace(c.From)
	if c.From != "" {
		address, err := mail.ParseAddress(c.From)
		if err != nil {
			return errors.New("Ogiltig avsändaradress")
		}
		c.From = address.Address
	}
	to := []string{}
	for _, raw := range c.To {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		address, err := mail.ParseAddress(raw)
		if err != nil {
			return fmt.Errorf("Ogiltig mottagare: %s", raw)
		}
		to = append(to, address.Address)
	}
	c.To = to
	if c.EmailEnabled && (c.SMTPHost == "" || c.From == "" || len(c.To) == 0) {
		return errors.New("E-post kräver SMTP-server, avsändare och minst en mottagare")
	}
	return nil
}

type alertEvent struct {
	Down    bool
	Title   string // device name, or "device / port"
	Address string
	Since   int64 // when the alarm started
	At      int64
}

type alerter struct {
	store *store
	send  func(context.Context, alertConfig, []alertEvent) error
	wg    sync.WaitGroup

	mu          sync.Mutex
	misses      map[string]int
	lastError   string
	lastErrorAt int64
}

func newAlerter(s *store) *alerter {
	return &alerter{store: s, send: sendAlerts, misses: map[string]int{}}
}

// status reports the latest delivery failure; it is cleared by the next successful delivery.
func (a *alerter) status() (string, int64) {
	if a == nil {
		return "", 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastError, a.lastErrorAt
}

func (a *alerter) record(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		log.Printf("alert delivery: %v", err)
		a.lastError, a.lastErrorAt = err.Error(), time.Now().Unix()
	} else {
		a.lastError, a.lastErrorAt = "", 0
	}
}

type alertTarget struct {
	key, kind   string
	deviceID    int64
	interfaceID *int64
	title       string
	address     string
	down        bool
	skip        bool // state unknown, e.g. a port on a device that does not answer
}

// evaluate compares current device and port states with the open alarms after a poll cycle and
// sends everything that changed as one message, so a core switch going down is one notification.
func (a *alerter) evaluate(ctx context.Context) {
	var targets []alertTarget
	rows, err := a.store.db.QueryContext(ctx, "SELECT id,name,address,status FROM devices WHERE os!='external'")
	if err != nil {
		log.Printf("alerts: %v", err)
		return
	}
	for rows.Next() {
		var t alertTarget
		var status string
		if rows.Scan(&t.deviceID, &t.title, &t.address, &status) == nil {
			t.key, t.kind, t.down, t.skip = fmt.Sprintf("d:%d", t.deviceID), "device", status == "offline", status == "unknown"
			targets = append(targets, t)
		}
	}
	rows.Close()
	// Link ports are always watched; other ports only when someone turned the alert on.
	rows, err = a.store.db.QueryContext(ctx, `SELECT i.id,d.id,d.name,i.name,d.address,d.status,i.status FROM interfaces i JOIN devices d ON d.id=i.device_id
		WHERE d.os!='external' AND (i.alert=1 OR EXISTS (SELECT 1 FROM links l WHERE l.a_interface_id=i.id OR l.b_interface_id=i.id))`)
	if err != nil {
		log.Printf("alerts: %v", err)
		return
	}
	for rows.Next() {
		var t alertTarget
		var id int64
		var device, port, deviceStatus, status string
		if rows.Scan(&id, &t.deviceID, &device, &port, &t.address, &deviceStatus, &status) == nil {
			t.key, t.kind, t.interfaceID, t.title = fmt.Sprintf("p:%d", id), "port", &id, device+" / "+port
			t.down, t.skip = status == "down", deviceStatus != "online"
			targets = append(targets, t)
		}
	}
	rows.Close()
	type openAlarm struct{ id, started int64 }
	open := map[string]openAlarm{}
	rows, err = a.store.db.QueryContext(ctx, "SELECT id,kind,device_id,interface_id,started_at FROM alarms WHERE cleared_at IS NULL")
	if err != nil {
		log.Printf("alerts: %v", err)
		return
	}
	for rows.Next() {
		var x openAlarm
		var kind string
		var deviceID, interfaceID sql.NullInt64
		if rows.Scan(&x.id, &kind, &deviceID, &interfaceID, &x.started) == nil {
			if kind == "port" {
				open[fmt.Sprintf("p:%d", interfaceID.Int64)] = x
			} else {
				open[fmt.Sprintf("d:%d", deviceID.Int64)] = x
			}
		}
	}
	rows.Close()

	now := time.Now().Unix()
	var events []alertEvent
	watched := map[string]bool{}
	a.mu.Lock()
	for _, t := range targets {
		watched[t.key] = true
		alarm, isOpen := open[t.key]
		switch {
		case t.skip:
			delete(a.misses, t.key)
		case t.down:
			a.misses[t.key]++
			if a.misses[t.key] < alertAfterMisses || isOpen {
				continue
			}
			if _, err := a.store.db.ExecContext(ctx, "INSERT INTO alarms(kind,device_id,interface_id,title,started_at) VALUES(?,?,?,?,?)", t.kind, t.deviceID, t.interfaceID, t.title, now); err != nil {
				log.Printf("alerts: %v", err)
				continue
			}
			events = append(events, alertEvent{Down: true, Title: t.title, Address: t.address, Since: now, At: now})
		default:
			delete(a.misses, t.key)
			if !isOpen {
				continue
			}
			if _, err := a.store.db.ExecContext(ctx, "UPDATE alarms SET cleared_at=? WHERE id=?", now, alarm.id); err != nil {
				log.Printf("alerts: %v", err)
				continue
			}
			events = append(events, alertEvent{Title: t.title, Address: t.address, Since: alarm.started, At: now})
		}
	}
	// A port that is no longer watched (link removed, alert turned off) closes quietly.
	for key, alarm := range open {
		if !watched[key] {
			_, _ = a.store.db.ExecContext(ctx, "UPDATE alarms SET cleared_at=? WHERE id=?", now, alarm.id)
		}
	}
	for key := range a.misses {
		if !watched[key] {
			delete(a.misses, key)
		}
	}
	a.mu.Unlock()
	if len(events) == 0 {
		return
	}
	// Deliver in the background so a slow mail server never delays the next poll.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cfg, err := a.store.loadAlertConfig(ctx)
		if err == nil {
			err = a.send(ctx, cfg, events)
		}
		a.record(err)
	}()
}

func formatDuration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d dygn", int(d.Hours())/24)
}

func (e alertEvent) line() string {
	if e.Down {
		return fmt.Sprintf("NERE  %s (%s) sedan %s", e.Title, e.Address, time.Unix(e.Since, 0).Format("15:04"))
	}
	return fmt.Sprintf("UPPE  %s (%s) efter %s", e.Title, e.Address, formatDuration(e.At-e.Since))
}

func alertSubject(events []alertEvent) string {
	if len(events) == 1 {
		state := "uppe igen"
		if events[0].Down {
			state = "nere"
		}
		return fmt.Sprintf("[Trafficflow] %s %s", events[0].Title, state)
	}
	down := 0
	for _, e := range events {
		if e.Down {
			down++
		}
	}
	return fmt.Sprintf("[Trafficflow] %d larm: %d nere, %d uppe", len(events), down, len(events)-down)
}

func alertText(events []alertEvent) string {
	lines := make([]string, 0, len(events))
	for _, e := range events {
		lines = append(lines, e.line())
	}
	return strings.Join(lines, "\n")
}

func discordText(events []alertEvent) string {
	lines := make([]string, 0, len(events))
	for _, e := range events {
		if e.Down {
			lines = append(lines, fmt.Sprintf("🔴 **NERE** %s `%s` sedan %s", e.Title, e.Address, time.Unix(e.Since, 0).Format("15:04")))
		} else {
			lines = append(lines, fmt.Sprintf("🟢 **UPPE** %s `%s` efter %s", e.Title, e.Address, formatDuration(e.At-e.Since)))
		}
	}
	return strings.Join(lines, "\n")
}

func sendAlerts(ctx context.Context, cfg alertConfig, events []alertEvent) error {
	var errs []error
	if cfg.DiscordEnabled {
		if err := sendDiscord(ctx, cfg.DiscordWebhook, discordText(events)); err != nil {
			errs = append(errs, fmt.Errorf("Discord: %w", err))
		}
	}
	if cfg.EmailEnabled {
		if err := sendEmail(ctx, cfg, alertSubject(events), alertText(events)); err != nil {
			errs = append(errs, fmt.Errorf("E-post: %w", err))
		}
	}
	return errors.Join(errs...)
}

var alertHTTP = &http.Client{Timeout: 10 * time.Second}

func sendDiscord(ctx context.Context, webhook, content string) error {
	if runes := []rune(content); len(runes) > 2000 { // Discord's message limit
		content = string(runes[:1990]) + "\n…"
	}
	body, _ := json.Marshal(map[string]any{"content": content, "allowed_mentions": map[string]any{"parse": []string{}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := alertHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhooken svarade %s", resp.Status)
	}
	return nil
}

func sendEmail(ctx context.Context, cfg alertConfig, subject, body string) error {
	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if cfg.SMTPSecurity == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: cfg.SMTPHost}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if cfg.SMTPSecurity == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: cfg.SMTPHost}); err != nil {
			return err
		}
	}
	if cfg.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
			return err
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, to := range cfg.To {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	domain := cfg.From[strings.LastIndex(cfg.From, "@")+1:]
	headers := []string{
		"From: " + (&mail.Address{Name: "Trafficflow", Address: cfg.From}).String(),
		"To: " + strings.Join(cfg.To, ", "),
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		fmt.Sprintf("Message-ID: <%d.trafficflow@%s>", time.Now().UnixNano(), domain),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
	}
	message := strings.Join(headers, "\r\n") + "\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	if _, err := io.WriteString(w, message); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
