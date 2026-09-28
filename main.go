package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web/dist
var webFiles embed.FS

type server struct {
	store     *store
	collector *collector
}

func main() {
	s, err := openStore()
	if err != nil {
		log.Fatal(err)
	}
	defer s.db.Close()
	if err := bootstrapAdmin(s); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newCollector(s)
	go c.run(ctx)
	app := &server{store: s, collector: c}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", app.login)
	mux.HandleFunc("POST /api/logout", app.authed(app.logout))
	mux.HandleFunc("GET /api/me", app.authed(app.me))
	mux.HandleFunc("GET /api/topology", app.authed(app.topology))
	mux.HandleFunc("POST /api/devices", app.authed(app.addDevice))
	mux.HandleFunc("PATCH /api/devices/{id}", app.authed(app.updateDevice))
	mux.HandleFunc("DELETE /api/devices/{id}", app.authed(app.deleteDevice))
	mux.HandleFunc("PUT /api/layout", app.authed(app.saveLayout))
	mux.HandleFunc("POST /api/externals", app.authed(app.addExternal))
	mux.HandleFunc("PATCH /api/externals/{id}", app.authed(app.updateExternal))
	mux.HandleFunc("POST /api/links", app.authed(app.addLink))
	mux.HandleFunc("DELETE /api/links/{id}", app.authed(app.deleteLink))
	mux.HandleFunc("POST /api/candidates/{id}/accept", app.authed(app.acceptCandidate))
	mux.HandleFunc("POST /api/candidates/{id}/dismiss", app.authed(app.dismissCandidate))
	mux.HandleFunc("DELETE /api/dismissed-candidates/{id}", app.authed(app.restoreCandidate))
	mux.HandleFunc("GET /api/history", app.authed(app.history))
	mux.HandleFunc("PATCH /api/interfaces/{id}", app.authed(app.updateInterface))
	mux.HandleFunc("GET /api/alarms", app.authed(app.alarms))
	mux.HandleFunc("GET /api/alert-settings", app.authed(app.alertSettings))
	mux.HandleFunc("PUT /api/alert-settings", app.authed(app.saveAlertSettings))
	mux.HandleFunc("POST /api/alert-settings/test", app.authed(app.testAlertSettings))
	static, _ := fs.Sub(webFiles, "web/dist")
	mux.Handle("/", spa(static))
	addr := ":8080"
	log.Printf("trafficflow listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, withSecurityHeaders(mux)))
}

func bootstrapAdmin(s *store) error {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	password := os.Getenv("ADMIN_PASSWORD")
	if len(password) < 10 {
		return errors.New("ADMIN_PASSWORD must be at least 10 characters on first start")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO users(username,password_hash) VALUES('admin',?)", string(hash))
	return err
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != "GET" {
			if origin := r.Header.Get("Origin"); origin != "" {
				if !strings.HasSuffix(origin, "://"+r.Host) {
					writeError(w, http.StatusForbidden, "Ogiltigt ursprung")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func spa(files fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(files, name); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Content-Type måste vara application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("Begäran måste innehålla exakt ett JSON-objekt")
	}
	return nil
}
func idParam(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("Ogiltigt ID")
	}
	return id, nil
}

func (a *server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var userID int64
	var hash string
	err := a.store.db.QueryRowContext(r.Context(), "SELECT id,password_hash FROM users WHERE username=?", input.Username).Scan(&userID, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		time.Sleep(250 * time.Millisecond)
		writeError(w, 401, "Fel användarnamn eller lösenord")
		return
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		writeError(w, 500, "Kunde inte skapa session")
		return
	}
	value := hex.EncodeToString(token)
	digest := sha256.Sum256([]byte(value))
	expires := time.Now().Add(24 * time.Hour)
	_, err = a.store.db.ExecContext(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", hex.EncodeToString(digest[:]), userID, expires.Unix())
	if err != nil {
		writeError(w, 500, "Kunde inte skapa session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "trafficflow_session", Value: value, Path: "/", Expires: expires, MaxAge: 86400, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]string{"username": input.Username})
}
func (a *server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("trafficflow_session")
		if err != nil {
			writeError(w, 401, "Inloggning krävs")
			return
		}
		digest := sha256.Sum256([]byte(cookie.Value))
		var id int64
		err = a.store.db.QueryRowContext(r.Context(), "SELECT user_id FROM sessions WHERE token_hash=? AND expires_at>?", hex.EncodeToString(digest[:]), time.Now().Unix()).Scan(&id)
		if err != nil {
			writeError(w, 401, "Sessionen har gått ut")
			return
		}
		next(w, r)
	}
}
func (a *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("trafficflow_session"); err == nil {
		digest := sha256.Sum256([]byte(cookie.Value))
		_, _ = a.store.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash=?", hex.EncodeToString(digest[:]))
	}
	http.SetCookie(w, &http.Cookie{Name: "trafficflow_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"username": "admin"})
}

type deviceView struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Resolved    string   `json:"resolved"`
	OS          string   `json:"os"`
	Kind        string   `json:"kind"`
	SNMPVersion string   `json:"snmpVersion"`
	Status      string   `json:"status"`
	LastSeen    *int64   `json:"lastSeen"`
	LastError   string   `json:"lastError"`
	X           *float64 `json:"x"`
	Y           *float64 `json:"y"`
}
type interfaceView struct {
	ID          int64    `json:"id"`
	DeviceID    int64    `json:"deviceId"`
	IfIndex     int      `json:"ifIndex"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SpeedBps    int64    `json:"speedBps"`
	Status      string   `json:"status"`
	RxBps       *float64 `json:"rxBps"`
	TxBps       *float64 `json:"txBps"`
	LastSample  *int64   `json:"lastSample"`
	Alert       bool     `json:"alert"`
}
type linkView struct {
	ID           int64  `json:"id"`
	AInterfaceID int64  `json:"aInterfaceId"`
	BInterfaceID int64  `json:"bInterfaceId"`
	Source       string `json:"source"`
}
type candidateView struct {
	ID               int64  `json:"id"`
	LocalInterfaceID int64  `json:"localInterfaceId"`
	RemoteDeviceID   *int64 `json:"remoteDeviceId"`
	RemotePort       string `json:"remotePort"`
	RemoteName       string `json:"remoteName"`
}
type dismissedView struct {
	ID               int64  `json:"id"`
	LocalInterfaceID int64  `json:"localInterfaceId"`
	RemotePort       string `json:"remotePort"`
	RemoteName       string `json:"remoteName"`
	DismissedAt      int64  `json:"dismissedAt"`
}

func (a *server) topology(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	devices := []deviceView{}
	interfaces := []interfaceView{}
	links := []linkView{}
	candidates := []candidateView{}
	rows, err := a.store.db.QueryContext(ctx, "SELECT id,name,address,resolved,os,kind,snmp_version,status,last_seen,last_error,map_x,map_y FROM devices ORDER BY name")
	if err != nil {
		writeError(w, 500, "Kunde inte läsa enheter")
		return
	}
	for rows.Next() {
		var x deviceView
		var last *int64
		if rows.Scan(&x.ID, &x.Name, &x.Address, &x.Resolved, &x.OS, &x.Kind, &x.SNMPVersion, &x.Status, &last, &x.LastError, &x.X, &x.Y) == nil {
			x.LastSeen = last
			devices = append(devices, x)
		}
	}
	rows.Close()
	rows, err = a.store.db.QueryContext(ctx, "SELECT id,device_id,if_index,name,description,speed_bps,status,rx_bps,tx_bps,last_sample,alert FROM interfaces ORDER BY device_id,if_index")
	if err != nil {
		writeError(w, 500, "Kunde inte läsa portar")
		return
	}
	for rows.Next() {
		var x interfaceView
		var rx, tx *float64
		var at *int64
		if rows.Scan(&x.ID, &x.DeviceID, &x.IfIndex, &x.Name, &x.Description, &x.SpeedBps, &x.Status, &rx, &tx, &at, &x.Alert) == nil {
			x.RxBps = rx
			x.TxBps = tx
			x.LastSample = at
			interfaces = append(interfaces, x)
		}
	}
	rows.Close()
	rows, err = a.store.db.QueryContext(ctx, "SELECT id,a_interface_id,b_interface_id,source FROM links ORDER BY id")
	if err != nil {
		writeError(w, 500, "Kunde inte läsa länkar")
		return
	}
	for rows.Next() {
		var x linkView
		if rows.Scan(&x.ID, &x.AInterfaceID, &x.BInterfaceID, &x.Source) == nil {
			links = append(links, x)
		}
	}
	rows.Close()
	rows, err = a.store.db.QueryContext(ctx, `SELECT id,local_interface_id,remote_device_id,remote_port,remote_name FROM candidates c
		WHERE NOT EXISTS (SELECT 1 FROM dismissed_candidates d WHERE d.local_interface_id=c.local_interface_id AND d.remote_name=c.remote_name AND d.remote_port=c.remote_port)
		ORDER BY last_seen DESC`)
	if err == nil {
		for rows.Next() {
			var x candidateView
			var rid *int64
			if rows.Scan(&x.ID, &x.LocalInterfaceID, &rid, &x.RemotePort, &x.RemoteName) == nil {
				x.RemoteDeviceID = rid
				candidates = append(candidates, x)
			}
		}
		rows.Close()
	}
	dismissed := []dismissedView{}
	rows, err = a.store.db.QueryContext(ctx, "SELECT id,local_interface_id,remote_port,remote_name,dismissed_at FROM dismissed_candidates ORDER BY dismissed_at DESC")
	if err == nil {
		for rows.Next() {
			var x dismissedView
			if rows.Scan(&x.ID, &x.LocalInterfaceID, &x.RemotePort, &x.RemoteName, &x.DismissedAt) == nil {
				dismissed = append(dismissed, x)
			}
		}
		rows.Close()
	}
	active, err := a.listAlarms(ctx, "cleared_at IS NULL ORDER BY started_at DESC")
	if err != nil {
		writeError(w, 500, "Kunde inte läsa larm")
		return
	}
	writeJSON(w, 200, map[string]any{"devices": devices, "interfaces": interfaces, "links": links, "candidates": candidates, "dismissedCandidates": dismissed, "alarms": active, "serverTime": time.Now().Unix()})
}

// deviceAddress normalizes an IP address or DNS name and resolves it from the server.
func deviceAddress(ctx context.Context, raw string) (address, resolved string, err error) {
	address = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if net.ParseIP(address) == nil && !validHostname(address) {
		return "", "", errors.New("Ange en giltig IP-adress eller ett DNS-namn")
	}
	resolved, err = resolveAddress(ctx, address)
	if err != nil {
		return "", "", errors.New("DNS-namnet kunde inte slås upp från servern")
	}
	return address, resolved, nil
}
func checkCredential(version string, secret credential) error {
	if version == "3" && (secret.User == "" || len(secret.AuthPassword) < 8 || len(secret.PrivPassword) < 8) {
		return errors.New("SNMPv3 kräver användare och lösenord på minst åtta tecken")
	}
	if _, ok := authProtocols[secret.AuthProtocol]; version == "3" && !ok {
		return errors.New("Välj MD5, SHA1 eller SHA256 som autentiseringsprotokoll")
	}
	if version == "2c" && secret.Community == "" {
		return errors.New("Ange SNMP-community")
	}
	return nil
}

func (a *server) addDevice(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Address      string `json:"address"`
		OS           string `json:"os"`
		SNMPVersion  string `json:"snmpVersion"`
		Community    string `json:"community"`
		User         string `json:"user"`
		AuthPassword string `json:"authPassword"`
		PrivPassword string `json:"privPassword"`
		AuthProtocol string `json:"authProtocol"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	address, resolved, err := deviceAddress(r.Context(), input.Address)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if input.OS != "routeros" && input.OS != "swos" {
		writeError(w, 400, "Välj RouterOS eller SwOS")
		return
	}
	if input.OS == "routeros" && input.SNMPVersion != "3" && input.SNMPVersion != "2c" {
		writeError(w, 400, "RouterOS kräver SNMPv3 eller SNMPv2c")
		return
	}
	if input.OS == "swos" && input.SNMPVersion != "2c" {
		writeError(w, 400, "SwOS kräver SNMPv2c")
		return
	}
	secret := credential{Community: input.Community, User: input.User, AuthPassword: input.AuthPassword, PrivPassword: input.PrivPassword, AuthProtocol: input.AuthProtocol}
	if input.SNMPVersion == "3" && secret.AuthProtocol == "" {
		secret.AuthProtocol = "SHA1"
	}
	if input.SNMPVersion != "3" {
		secret.AuthProtocol = ""
	}
	if err := checkCredential(input.SNMPVersion, secret); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	encoded, _ := json.Marshal(secret)
	encrypted, err := a.store.encrypt(string(encoded))
	if err != nil {
		writeError(w, 500, "Kunde inte lagra uppgifter")
		return
	}
	_, err = a.store.db.ExecContext(r.Context(), "INSERT INTO devices(name,address,resolved,os,snmp_version,credential) VALUES(?,?,?,?,?,?)", address, address, resolved, input.OS, input.SNMPVersion, encrypted)
	if err != nil {
		writeError(w, 409, "Enheten finns redan eller kunde inte sparas")
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}

// updateDevice changes the address and SNMP credentials of a device. Empty secret fields keep
// the stored value. The device ID is unchanged, so its ports, links and history are kept.
func (a *server) updateDevice(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var input struct {
		Address      string `json:"address"`
		Community    string `json:"community"`
		User         string `json:"user"`
		AuthPassword string `json:"authPassword"`
		PrivPassword string `json:"privPassword"`
		AuthProtocol string `json:"authProtocol"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var version, os string
	var stored []byte
	if err := a.store.db.QueryRowContext(r.Context(), "SELECT os,snmp_version,credential FROM devices WHERE id=?", id).Scan(&os, &version, &stored); err != nil {
		writeError(w, 404, "Enheten finns inte")
		return
	}
	if os == "external" {
		writeError(w, 400, "Externa enheter har ingen adress eller SNMP-uppgifter")
		return
	}
	address, resolved, err := deviceAddress(r.Context(), input.Address)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var secret credential
	plain, err := a.store.decrypt(stored)
	if err == nil {
		err = json.Unmarshal([]byte(plain), &secret)
	}
	if err != nil {
		writeError(w, 500, "Kunde inte läsa sparade uppgifter")
		return
	}
	for _, field := range []struct {
		dst   *string
		value string
	}{
		{&secret.Community, input.Community}, {&secret.User, input.User},
		{&secret.AuthPassword, input.AuthPassword}, {&secret.PrivPassword, input.PrivPassword},
		{&secret.AuthProtocol, input.AuthProtocol},
	} {
		if field.value != "" {
			*field.dst = field.value
		}
	}
	if err := checkCredential(version, secret); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	encoded, _ := json.Marshal(secret)
	encrypted, err := a.store.encrypt(string(encoded))
	if err != nil {
		writeError(w, 500, "Kunde inte lagra uppgifter")
		return
	}
	_, err = a.store.db.ExecContext(r.Context(), "UPDATE devices SET address=?,resolved=?,credential=? WHERE id=?", address, resolved, encrypted, id)
	if err != nil {
		writeError(w, 409, "Adressen används redan av en annan enhet")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// saveLayout stores map positions for one or more devices.
func (a *server) saveLayout(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Positions []struct {
			ID int64   `json:"id"`
			X  float64 `json:"x"`
			Y  float64 `json:"y"`
		} `json:"positions"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "Kunde inte spara kartan")
		return
	}
	defer tx.Rollback()
	for _, p := range input.Positions {
		if math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) || math.Abs(p.X) > 1e6 || math.Abs(p.Y) > 1e6 {
			writeError(w, 400, "Ogiltig position")
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE devices SET map_x=?,map_y=? WHERE id=?", p.X, p.Y, p.ID); err != nil {
			writeError(w, 500, "Kunde inte spara kartan")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "Kunde inte spara kartan")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// Externals are equipment we cannot poll: clouds (networks outside our control, such as an
// upstream operator) and servers. They are devices with os 'external' that are never polled, each
// with one virtual port that can be linked to real ports. Traffic comes from the real ports; the
// virtual port's speed is the contracted capacity or the server's NIC speed, so link load is
// measured against it when it is lower than the port speed.
type externalInput struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	CapacityBps int64  `json:"capacityBps"`
}

func (e *externalInput) validate() error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" || len([]rune(e.Name)) > 64 {
		return errors.New("Ange ett namn, högst 64 tecken")
	}
	// An empty kind means a cloud on create and an unchanged kind on update.
	if e.Kind != "" && e.Kind != "cloud" && e.Kind != "server" {
		return errors.New("Ogiltig typ")
	}
	if e.CapacityBps < 0 || e.CapacityBps > 10_000_000_000_000 {
		return errors.New("Ogiltig kapacitet")
	}
	return nil
}
func (a *server) addExternal(w http.ResponseWriter, r *http.Request) {
	var input externalInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := input.validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if input.Kind == "" {
		input.Kind = "cloud"
	}
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		writeError(w, 500, "Kunde inte skapa enheten")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "Kunde inte skapa enheten")
		return
	}
	defer tx.Rollback()
	// address is unique and unused for externals, so it gets a random placeholder.
	result, err := tx.ExecContext(r.Context(), "INSERT INTO devices(name,address,os,kind,snmp_version,credential,status) VALUES(?,?,'external',?,'',?,'external')",
		input.Name, "external:"+hex.EncodeToString(token), input.Kind, []byte{})
	if err != nil {
		writeError(w, 500, "Kunde inte skapa enheten")
		return
	}
	id, _ := result.LastInsertId()
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO interfaces(device_id,if_index,name,speed_bps,status) VALUES(?,0,'Anslutning',?,'up')", id, input.CapacityBps); err != nil {
		writeError(w, 500, "Kunde inte skapa enheten")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "Kunde inte skapa enheten")
		return
	}
	writeJSON(w, 201, map[string]int64{"id": id})
}
func (a *server) updateExternal(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var input externalInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := input.validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "Kunde inte spara enheten")
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), "UPDATE devices SET name=?,kind=COALESCE(NULLIF(?,''),kind) WHERE id=? AND os='external'", input.Name, input.Kind, id)
	if err != nil {
		writeError(w, 500, "Kunde inte spara enheten")
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, 404, "Enheten finns inte")
		return
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE interfaces SET speed_bps=? WHERE device_id=?", input.CapacityBps, id); err != nil {
		writeError(w, 500, "Kunde inte spara enheten")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "Kunde inte spara enheten")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	_, err = a.store.db.ExecContext(r.Context(), "DELETE FROM devices WHERE id=?", id)
	if err != nil {
		writeError(w, 500, "Kunde inte ta bort enheten")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *server) addLink(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AInterfaceID int64 `json:"aInterfaceId"`
		BInterfaceID int64 `json:"bInterfaceId"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := a.insertLink(r.Context(), input.AInterfaceID, input.BInterfaceID, "manual"); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}
func (a *server) insertLink(ctx context.Context, first, second int64, source string) error {
	if first <= 0 || second <= 0 || first == second {
		return errors.New("Välj två olika portar")
	}
	var firstDevice, secondDevice int64
	if a.store.db.QueryRowContext(ctx, "SELECT device_id FROM interfaces WHERE id=?", first).Scan(&firstDevice) != nil || a.store.db.QueryRowContext(ctx, "SELECT device_id FROM interfaces WHERE id=?", second).Scan(&secondDevice) != nil {
		return errors.New("Porten finns inte")
	}
	if firstDevice == secondDevice {
		return errors.New("Länken måste gå mellan två enheter")
	}
	if first > second {
		first, second = second, first
	}
	var existing int
	_ = a.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM links WHERE (a_interface_id=? AND b_interface_id=?) OR (a_interface_id=? AND b_interface_id=?)", first, second, second, first).Scan(&existing)
	if existing > 0 {
		return errors.New("Länken finns redan")
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO links(a_interface_id,b_interface_id,source) VALUES(?,?,?)", first, second, source); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM suppressed_links WHERE a_interface_id=? AND b_interface_id=?", first, second); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *server) deleteLink(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var first, second int64
	if err := a.store.db.QueryRowContext(r.Context(), "SELECT a_interface_id,b_interface_id FROM links WHERE id=?", id).Scan(&first, &second); err != nil {
		writeError(w, 404, "Länken finns inte")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "Kunde inte ta bort länken")
		return
	}
	defer tx.Rollback()
	if first > second {
		first, second = second, first
	}
	if _, err = tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO suppressed_links(a_interface_id,b_interface_id) VALUES(?,?)", first, second); err != nil {
		writeError(w, 500, "Kunde inte ta bort länken")
		return
	}
	if _, err = tx.ExecContext(r.Context(), "DELETE FROM links WHERE id=?", id); err != nil {
		writeError(w, 500, "Kunde inte ta bort länken")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "Kunde inte ta bort länken")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *server) acceptCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var local int64
	var remoteDevice *int64
	var remotePort string
	err = a.store.db.QueryRowContext(r.Context(), "SELECT local_interface_id,remote_device_id,remote_port FROM candidates WHERE id=?", id).Scan(&local, &remoteDevice, &remotePort)
	if err != nil || remoteDevice == nil {
		writeError(w, 400, "Ingen registrerad motpart för förslaget")
		return
	}
	var remoteInterface int64
	err = a.store.db.QueryRowContext(r.Context(), "SELECT id FROM interfaces WHERE device_id=? AND name=?", *remoteDevice, remotePort).Scan(&remoteInterface)
	if err != nil {
		writeError(w, 400, "Motpartens port kunde inte identifieras. Skapa länken manuellt.")
		return
	}
	if err := a.insertLink(r.Context(), local, remoteInterface, "confirmed"); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}
func (a *server) dismissCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	result, err := a.store.db.ExecContext(r.Context(), `INSERT OR IGNORE INTO dismissed_candidates(local_interface_id,remote_name,remote_port,dismissed_at)
		SELECT local_interface_id,remote_name,remote_port,? FROM candidates WHERE id=?`, time.Now().Unix(), id)
	if err != nil {
		writeError(w, 500, "Kunde inte neka förslaget")
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		var exists int
		_ = a.store.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM candidates WHERE id=?", id).Scan(&exists)
		if exists == 0 {
			writeError(w, 404, "Förslaget finns inte längre")
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *server) restoreCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if _, err := a.store.db.ExecContext(r.Context(), "DELETE FROM dismissed_candidates WHERE id=?", id); err != nil {
		writeError(w, 500, "Kunde inte återställa förslaget")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *server) history(w http.ResponseWriter, r *http.Request) {
	linkID, err := strconv.ParseInt(r.URL.Query().Get("link"), 10, 64)
	if err != nil || linkID <= 0 {
		writeError(w, 400, "Ogiltig länk")
		return
	}
	rangeKey := r.URL.Query().Get("range")
	seconds := int64(3600)
	table, timeCol := "samples", "ts"
	bucket := int64(15)
	switch rangeKey {
	case "1h":
	case "24h":
		seconds = 86400
		table, timeCol, bucket = "rollups", "minute", 60
	case "7d":
		seconds = 7 * 86400
		table, timeCol, bucket = "rollups", "minute", 600
	case "30d":
		seconds = 30 * 86400
		table, timeCol, bucket = "rollups", "minute", 1800
	default:
		writeError(w, 400, "Ogiltigt tidsintervall")
		return
	}
	query := fmt.Sprintf("SELECT (%s / ?) * ? AS bucket,AVG(rx_bps),AVG(tx_bps) FROM %s WHERE link_id=? AND %s>=? GROUP BY bucket ORDER BY bucket", timeCol, table, timeCol)
	rows, err := a.store.db.QueryContext(r.Context(), query, bucket, bucket, linkID, time.Now().Unix()-seconds)
	if err != nil {
		writeError(w, 500, "Kunde inte läsa historik")
		return
	}
	defer rows.Close()
	points := []map[string]any{}
	for rows.Next() {
		var ts int64
		var rx, tx float64
		if rows.Scan(&ts, &rx, &tx) == nil {
			points = append(points, map[string]any{"ts": ts, "rxBps": rx, "txBps": tx})
		}
	}
	writeJSON(w, 200, map[string]any{"points": points})
}

type alarmView struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	DeviceID    *int64 `json:"deviceId"`
	InterfaceID *int64 `json:"interfaceId"`
	Title       string `json:"title"`
	StartedAt   int64  `json:"startedAt"`
	ClearedAt   *int64 `json:"clearedAt"`
}

func (a *server) listAlarms(ctx context.Context, where string) ([]alarmView, error) {
	list := []alarmView{}
	rows, err := a.store.db.QueryContext(ctx, "SELECT id,kind,device_id,interface_id,title,started_at,cleared_at FROM alarms WHERE "+where)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x alarmView
		if err := rows.Scan(&x.ID, &x.Kind, &x.DeviceID, &x.InterfaceID, &x.Title, &x.StartedAt, &x.ClearedAt); err != nil {
			return nil, err
		}
		list = append(list, x)
	}
	return list, rows.Err()
}

func (a *server) alarms(w http.ResponseWriter, r *http.Request) {
	active, err := a.listAlarms(r.Context(), "cleared_at IS NULL ORDER BY started_at DESC")
	if err == nil {
		var recent []alarmView
		recent, err = a.listAlarms(r.Context(), "cleared_at IS NOT NULL ORDER BY cleared_at DESC LIMIT 50")
		if err == nil {
			writeJSON(w, 200, map[string]any{"active": active, "recent": recent})
			return
		}
	}
	writeError(w, 500, "Kunde inte läsa larm")
}

// updateInterface turns alerting on or off for a port. Link ports are always watched.
func (a *server) updateInterface(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var input struct {
		Alert bool `json:"alert"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	result, err := a.store.db.ExecContext(r.Context(), "UPDATE interfaces SET alert=? WHERE id=? AND device_id IN (SELECT id FROM devices WHERE os!='external')", input.Alert, id)
	if err != nil {
		writeError(w, 500, "Kunde inte spara porten")
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, 404, "Porten finns inte")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type alertSettingsView struct {
	alertConfig
	HasDiscordWebhook bool   `json:"hasDiscordWebhook"`
	HasSMTPPassword   bool   `json:"hasSmtpPassword"`
	LastError         string `json:"lastError"`
	LastErrorAt       int64  `json:"lastErrorAt"`
}

func (a *server) alerts() *alerter {
	if a.collector == nil {
		return nil
	}
	return a.collector.alerts
}

// writeAlertSettings responds with the configuration minus its secrets.
func (a *server) writeAlertSettings(w http.ResponseWriter, cfg alertConfig) {
	view := alertSettingsView{alertConfig: cfg, HasDiscordWebhook: cfg.DiscordWebhook != "", HasSMTPPassword: cfg.SMTPPassword != ""}
	view.DiscordWebhook, view.SMTPPassword = "", ""
	view.LastError, view.LastErrorAt = a.alerts().status()
	writeJSON(w, 200, view)
}

func (a *server) alertSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.store.loadAlertConfig(r.Context())
	if err != nil {
		writeError(w, 500, "Kunde inte läsa larminställningar")
		return
	}
	a.writeAlertSettings(w, cfg)
}

func (a *server) saveAlertSettings(w http.ResponseWriter, r *http.Request) {
	var input alertConfig
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	stored, err := a.store.loadAlertConfig(r.Context())
	if err != nil {
		writeError(w, 500, "Kunde inte läsa larminställningar")
		return
	}
	// Secrets are never sent to the browser, so an empty field keeps the saved value.
	if input.DiscordWebhook == "" {
		input.DiscordWebhook = stored.DiscordWebhook
	}
	if input.SMTPPassword == "" {
		input.SMTPPassword = stored.SMTPPassword
	}
	if err := input.validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := a.store.saveAlertConfig(r.Context(), input); err != nil {
		writeError(w, 500, "Kunde inte spara larminställningar")
		return
	}
	a.writeAlertSettings(w, input)
}

// testAlertSettings sends a test message through one channel, enabled or not, so each can be
// checked before alerts are turned on.
func (a *server) testAlertSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Channel string `json:"channel"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	cfg, err := a.store.loadAlertConfig(r.Context())
	if err != nil {
		writeError(w, 500, "Kunde inte läsa larminställningar")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text := "Testmeddelande från Trafficflow. Larm om enheter och portar som går ner skickas hit."
	switch input.Channel {
	case "discord":
		if cfg.DiscordWebhook == "" {
			writeError(w, 400, "Ange och spara en Discord-webhook först")
			return
		}
		err = sendDiscord(ctx, cfg.DiscordWebhook, text)
	case "email":
		if cfg.SMTPHost == "" || cfg.From == "" || len(cfg.To) == 0 {
			writeError(w, 400, "Ange SMTP-server, avsändare och mottagare först")
			return
		}
		err = sendEmail(ctx, cfg, "[Trafficflow] Testmeddelande", text)
	default:
		writeError(w, 400, "Okänd kanal")
		return
	}
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
