package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

type credential struct {
	Community    string `json:"community,omitempty"`
	User         string `json:"user,omitempty"`
	AuthPassword string `json:"authPassword,omitempty"`
	PrivPassword string `json:"privPassword,omitempty"`
	// AuthProtocol is MD5, SHA1 or SHA256. Empty means SHA256, which earlier versions always used.
	AuthProtocol string `json:"authProtocol,omitempty"`
}

var authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{"MD5": gosnmp.MD5, "SHA1": gosnmp.SHA, "SHA256": gosnmp.SHA256, "": gosnmp.SHA256}

type deviceConfig struct {
	ID                         int64
	Name, Address, OS, Version string
	Credential                 []byte
}
type counterPoint struct {
	In, Out     uint64
	In64, Out64 bool
	At          time.Time
	Uptime      uint64
}
type collector struct {
	store    *store
	mu       sync.Mutex
	previous map[int64]counterPoint
	alerts   *alerter
}

func newCollector(s *store) *collector {
	return &collector{store: s, previous: map[int64]counterPoint{}, alerts: newAlerter(s)}
}

func (c *collector) run(ctx context.Context) {
	cycle := 0
	for {
		start := time.Now()
		c.pollAll(ctx, cycle%20 == 0, start)
		cycle++
		if cycle%20 == 0 {
			if err := c.store.cleanup(ctx); err != nil {
				log.Printf("cleanup: %v", err)
			}
		}
		wait := time.Until(start.Add(15 * time.Second))
		if wait < time.Second {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *collector) pollAll(ctx context.Context, metadata bool, start time.Time) {
	rows, err := c.store.db.QueryContext(ctx, "SELECT id,name,address,os,snmp_version,credential FROM devices WHERE os!='external'")
	if err != nil {
		log.Printf("list devices: %v", err)
		return
	}
	var devices []deviceConfig
	for rows.Next() {
		var d deviceConfig
		if err := rows.Scan(&d.ID, &d.Name, &d.Address, &d.OS, &d.Version, &d.Credential); err == nil {
			devices = append(devices, d)
		}
	}
	rows.Close()
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, d := range devices {
		wg.Add(1)
		go func(d deviceConfig) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if err := c.pollDevice(ctx, d, metadata); err != nil {
				log.Printf("poll %s: %v", d.Address, err)
				_, _ = c.store.db.ExecContext(ctx, "UPDATE devices SET status='offline',last_error=? WHERE id=?", err.Error(), d.ID)
				_, _ = c.store.db.ExecContext(ctx, "UPDATE interfaces SET status='unknown',rx_bps=NULL,tx_bps=NULL WHERE device_id=?", d.ID)
			}
		}(d)
	}
	wg.Wait()
	c.alerts.evaluate(ctx)
	c.reconcile(ctx)
	c.reconcileForwarding(ctx)
	c.sampleLinks(ctx, start)
}

// validHostname reports whether name is a syntactically valid DNS name.
func validHostname(name string) bool {
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// resolveAddress returns address itself when it is an IP address, otherwise the first IPv4
// address the name resolves to (or the first IPv6 address when there is no IPv4 address).
func resolveAddress(ctx context.Context, address string) (string, error) {
	if ip := net.ParseIP(address); ip != nil {
		return ip.String(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, address)
	if err != nil {
		return "", fmt.Errorf("DNS lookup for %s failed: %w", address, err)
	}
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("DNS lookup for %s returned no addresses", address)
	}
	return addrs[0].IP.String(), nil
}

func snmpClient(target string, d deviceConfig, secret credential) (*gosnmp.GoSNMP, error) {
	g := &gosnmp.GoSNMP{Target: target, Port: 161, Timeout: 2 * time.Second, Retries: 1, MaxOids: 50}
	if d.Version == "3" {
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		g.MsgFlags = gosnmp.AuthPriv
		g.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: secret.User, AuthenticationProtocol: authProtocols[secret.AuthProtocol], AuthenticationPassphrase: secret.AuthPassword, PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: secret.PrivPassword}
	} else {
		g.Version = gosnmp.Version2c
		g.Community = secret.Community
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return g, nil
}

const (
	oidUptime         = ".1.3.6.1.2.1.1.3.0"
	oidSysName        = ".1.3.6.1.2.1.1.5.0"
	oidSysLocation    = ".1.3.6.1.2.1.1.6.0"
	oidIfDescr        = ".1.3.6.1.2.1.2.2.1.2"
	oidIfMac          = ".1.3.6.1.2.1.2.2.1.6"
	oidIfSpeed        = ".1.3.6.1.2.1.2.2.1.5"
	oidIfStatus       = ".1.3.6.1.2.1.2.2.1.8"
	oidIfIn32         = ".1.3.6.1.2.1.2.2.1.10"
	oidIfOut32        = ".1.3.6.1.2.1.2.2.1.16"
	oidIfName         = ".1.3.6.1.2.1.31.1.1.1.1"
	oidIfHighSpeed    = ".1.3.6.1.2.1.31.1.1.1.15"
	oidIfIn64         = ".1.3.6.1.2.1.31.1.1.1.6"
	oidIfOut64        = ".1.3.6.1.2.1.31.1.1.1.10"
	oidLldpLocalPort  = ".1.0.8802.1.1.2.1.3.7.1.3"
	oidLldpRemoteName = ".1.0.8802.1.1.2.1.4.1.1.9"
	oidLldpRemotePort = ".1.0.8802.1.1.2.1.4.1.1.7"
	oidBridgeAddress  = ".1.3.6.1.2.1.17.1.1.0"
	oidBasePortIndex  = ".1.3.6.1.2.1.17.1.4.1.2"
	oidFdbPort        = ".1.3.6.1.2.1.17.4.3.1.2"
	oidFdbStatus      = ".1.3.6.1.2.1.17.4.3.1.3"
	oidQFdbPort       = ".1.3.6.1.2.1.17.7.1.2.2.1.2"
)

func walk(g *gosnmp.GoSNMP, oid string) map[string]gosnmp.SnmpPDU {
	out := map[string]gosnmp.SnmpPDU{}
	values, err := g.BulkWalkAll(oid)
	if err != nil {
		values, err = g.WalkAll(oid)
	}
	if err != nil {
		return out
	}
	for _, p := range values {
		out[strings.TrimPrefix(p.Name, oid+".")] = p
	}
	return out
}
func pduString(p gosnmp.SnmpPDU) string {
	if b, ok := p.Value.([]byte); ok {
		return string(b)
	}
	if p.Value == nil {
		return ""
	}
	return fmt.Sprint(p.Value)
}
func pduNumber(p gosnmp.SnmpPDU) uint64 {
	if p.Value == nil {
		return 0
	}
	v := gosnmp.ToBigInt(p.Value)
	if v == nil || v.Sign() < 0 {
		return 0
	}
	return v.Uint64()
}
func pduMac(p gosnmp.SnmpPDU) string {
	b, ok := p.Value.([]byte)
	if !ok || len(b) != 6 {
		return ""
	}
	return strings.ToUpper(net.HardwareAddr(b).String())
}

func (c *collector) pollDevice(ctx context.Context, d deviceConfig, metadata bool) error {
	plain, err := c.store.decrypt(d.Credential)
	if err != nil {
		return err
	}
	var secret credential
	if err := json.Unmarshal([]byte(plain), &secret); err != nil {
		return err
	}
	// Resolve on every poll so a changed DNS record is picked up.
	target, err := resolveAddress(ctx, d.Address)
	if err != nil {
		return err
	}
	g, err := snmpClient(target, d, secret)
	if err != nil {
		return err
	}
	defer g.Conn.Close()
	packet, err := g.Get([]string{oidUptime, oidSysName, oidSysLocation})
	if err != nil {
		return err
	}
	if len(packet.Variables) < 3 {
		return fmt.Errorf("missing system data")
	}
	uptime := pduNumber(packet.Variables[0])
	name := pduString(packet.Variables[1])
	if name == "" {
		name = d.Name
	}
	location := strings.TrimSpace(pduString(packet.Variables[2]))
	_, err = c.store.db.ExecContext(ctx, "UPDATE devices SET name=?,location=?,resolved=?,status='online',last_seen=?,uptime=?,last_error='' WHERE id=?", name, location, target, time.Now().Unix(), uptime, d.ID)
	if err != nil {
		return err
	}
	if !metadata {
		var interfaceCount int
		if err := c.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM interfaces WHERE device_id=?", d.ID).Scan(&interfaceCount); err != nil {
			return err
		}
		metadata = interfaceCount == 0
	}
	if metadata {
		if err := c.refreshInterfaces(ctx, g, d.ID); err != nil {
			return err
		}
		if d.OS == "routeros" {
			c.refreshNeighbors(ctx, g, d.ID)
		}
		if err := c.refreshForwarding(ctx, g, d.ID); err != nil {
			log.Printf("forwarding table %s: %v", d.Address, err)
		}
	}
	in64, out64 := walk(g, oidIfIn64), walk(g, oidIfOut64)
	in32, out32 := map[string]gosnmp.SnmpPDU{}, map[string]gosnmp.SnmpPDU{}
	rows, err := c.store.db.QueryContext(ctx, "SELECT id,if_index,speed_bps FROM interfaces WHERE device_id=?", d.ID)
	if err != nil {
		return err
	}
	type iface struct {
		id    int64
		index int
		speed int64
	}
	var list []iface
	for rows.Next() {
		var x iface
		if rows.Scan(&x.id, &x.index, &x.speed) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	statuses := walk(g, oidIfStatus)
	for _, x := range list {
		status := "down"
		if pduNumber(statuses[strconv.Itoa(x.index)]) == 1 {
			status = "up"
		}
		_, _ = c.store.db.ExecContext(ctx, "UPDATE interfaces SET status=? WHERE id=?", status, x.id)
	}
	now := time.Now()
	for _, x := range list {
		key := strconv.Itoa(x.index)
		pi, okI := in64[key]
		po, okO := out64[key]
		use64 := okI && okO
		if !use64 {
			if len(in32) == 0 {
				in32, out32 = walk(g, oidIfIn32), walk(g, oidIfOut32)
			}
			pi, okI = in32[key]
			po, okO = out32[key]
		}
		if !okI || !okO {
			continue
		}
		point := counterPoint{In: pduNumber(pi), Out: pduNumber(po), In64: use64, Out64: use64, At: now, Uptime: uptime}
		c.mu.Lock()
		prev, has := c.previous[x.id]
		c.previous[x.id] = point
		c.mu.Unlock()
		if !has {
			continue
		}
		rx, okRx := rate(prev.In, point.In, prev.In64 && point.In64, now.Sub(prev.At), x.speed, uptime >= prev.Uptime)
		tx, okTx := rate(prev.Out, point.Out, prev.Out64 && point.Out64, now.Sub(prev.At), x.speed, uptime >= prev.Uptime)
		if !okRx || !okTx {
			_, _ = c.store.db.ExecContext(ctx, "UPDATE interfaces SET rx_bps=NULL,tx_bps=NULL,last_sample=? WHERE id=?", now.Unix(), x.id)
			continue
		}
		_, _ = c.store.db.ExecContext(ctx, "UPDATE interfaces SET rx_bps=?,tx_bps=?,last_sample=? WHERE id=?", rx, tx, now.Unix(), x.id)
	}
	return nil
}

func rate(before, after uint64, is64 bool, elapsed time.Duration, speed int64, uptimeOK bool) (float64, bool) {
	seconds := elapsed.Seconds()
	if !uptimeOK || seconds <= 0 || seconds > 90 {
		return 0, false
	}
	if !is64 && (speed <= 0 || float64(speed)*seconds/8 >= float64(uint64(1)<<32)) {
		return 0, false
	}
	var delta uint64
	if after >= before {
		delta = after - before
	} else if !is64 && speed > 0 && float64(speed)*seconds/8 < float64(uint64(1)<<32) {
		delta = (uint64(1) << 32) - before + after
	} else {
		return 0, false
	}
	bps := float64(delta) * 8 / seconds
	if speed > 0 && bps > float64(speed)*1.1 {
		return 0, false
	}
	return bps, true
}

func (c *collector) refreshInterfaces(ctx context.Context, g *gosnmp.GoSNMP, deviceID int64) error {
	names := walk(g, oidIfName)
	descs := walk(g, oidIfDescr)
	if len(names) == 0 {
		names = descs
	}
	if len(names) == 0 {
		return fmt.Errorf("no interfaces in IF-MIB")
	}
	macs, speeds, highs, statuses := walk(g, oidIfMac), walk(g, oidIfSpeed), walk(g, oidIfHighSpeed), walk(g, oidIfStatus)
	for key, p := range names {
		index, err := strconv.Atoi(key)
		if err != nil {
			continue
		}
		name := pduString(p)
		if name == "" {
			continue
		}
		speed := int64(pduNumber(speeds[key]))
		if h := pduNumber(highs[key]); h > 0 {
			speed = int64(h) * 1000000
		}
		status := "down"
		if pduNumber(statuses[key]) == 1 {
			status = "up"
		}
		_, err = c.store.db.ExecContext(ctx, `INSERT INTO interfaces(device_id,if_index,name,description,mac,speed_bps,status) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(device_id,if_index) DO UPDATE SET name=excluded.name,description=excluded.description,mac=excluded.mac,speed_bps=excluded.speed_bps,status=excluded.status`,
			deviceID, index, name, pduString(descs[key]), pduMac(macs[key]), speed, status)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *collector) refreshNeighbors(ctx context.Context, g *gosnmp.GoSNMP, deviceID int64) {
	localPorts := walk(g, oidLldpLocalPort)
	remoteNames := walk(g, oidLldpRemoteName)
	remotePorts := walk(g, oidLldpRemotePort)
	for suffix, namePDU := range remoteNames {
		parts := strings.Split(suffix, ".")
		if len(parts) < 3 {
			continue
		}
		localPort := pduString(localPorts[parts[1]])
		remoteName := pduString(namePDU)
		remotePort := pduString(remotePorts[suffix])
		if localPort == "" || remoteName == "" {
			continue
		}
		var ifaceID int64
		err := c.store.db.QueryRowContext(ctx, "SELECT id FROM interfaces WHERE device_id=? AND (name=? OR description=?) LIMIT 1", deviceID, localPort, localPort).Scan(&ifaceID)
		if err != nil {
			continue
		}
		var remoteID sql.NullInt64
		var matchCount int
		_ = c.store.db.QueryRowContext(ctx, "SELECT COUNT(*),MIN(id) FROM devices WHERE lower(name)=lower(?) AND id!=? AND os!='external'", remoteName, deviceID).Scan(&matchCount, &remoteID)
		if matchCount != 1 {
			remoteID = sql.NullInt64{}
		}
		_, _ = c.store.db.ExecContext(ctx, `INSERT INTO candidates(local_interface_id,remote_device_id,remote_port,remote_name,last_seen) VALUES(?,?,?,?,?)
			ON CONFLICT(local_interface_id,remote_name,remote_port) DO UPDATE SET remote_device_id=excluded.remote_device_id,last_seen=excluded.last_seen`,
			ifaceID, remoteID, remotePort, remoteName, time.Now().Unix())
	}
}

func (c *collector) reconcile(ctx context.Context) {
	// A link becomes automatic only when both devices report the matching port pair.
	rows, err := c.store.db.QueryContext(ctx, `SELECT a.local_interface_id,b.local_interface_id FROM candidates a
		JOIN interfaces ai ON ai.id=a.local_interface_id
		JOIN devices ad ON ad.id=ai.device_id
		JOIN interfaces bi ON bi.device_id=a.remote_device_id AND bi.name=a.remote_port
		JOIN candidates b ON b.local_interface_id=bi.id AND b.remote_device_id=ad.id AND b.remote_port=ai.name
		WHERE a.local_interface_id < bi.id AND NOT EXISTS (
			SELECT 1 FROM suppressed_links s WHERE s.a_interface_id=a.local_interface_id AND s.b_interface_id=bi.id)`)
	if err != nil {
		return
	}
	var pairs [][2]int64
	for rows.Next() {
		var a, b int64
		if rows.Scan(&a, &b) == nil {
			pairs = append(pairs, [2]int64{a, b})
		}
	}
	rows.Close()
	for _, p := range pairs {
		_, _ = c.store.db.ExecContext(ctx, "INSERT OR IGNORE INTO links(a_interface_id,b_interface_id,source) VALUES(?,?,'lldp')", p[0], p[1])
	}
}

// suffixMac parses a MAC address encoded as six decimal OID sub-identifiers.
func suffixMac(suffix string) string {
	parts := strings.Split(suffix, ".")
	if len(parts) != 6 {
		return ""
	}
	b := make(net.HardwareAddr, 6)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return ""
		}
		b[i] = byte(n)
	}
	return strings.ToUpper(b.String())
}

// refreshForwarding stores which MAC addresses the device has learned on each port (BRIDGE-MIB).
// SwOS has no LLDP, so this is the only way to discover links between SwOS switches.
func (c *collector) refreshForwarding(ctx context.Context, g *gosnmp.GoSNMP, deviceID int64) error {
	if packet, err := g.Get([]string{oidBridgeAddress}); err == nil && len(packet.Variables) == 1 {
		if mac := pduMac(packet.Variables[0]); mac != "" && mac != "00:00:00:00:00:00" {
			_, _ = c.store.db.ExecContext(ctx, "INSERT OR IGNORE INTO device_macs(device_id,mac) VALUES(?,?)", deviceID, mac)
		}
	}
	bridgePorts := map[string]uint64{}
	for port, p := range walk(g, oidBasePortIndex) {
		bridgePorts[port] = pduNumber(p)
	}
	entries := map[string]uint64{}
	statuses := walk(g, oidFdbStatus)
	for suffix, p := range walk(g, oidFdbPort) {
		if pduNumber(statuses[suffix]) == 4 { // self
			continue
		}
		if mac := suffixMac(suffix); mac != "" {
			entries[mac] = pduNumber(p)
		}
	}
	if len(entries) == 0 {
		for suffix, p := range walk(g, oidQFdbPort) {
			if _, macPart, ok := strings.Cut(suffix, "."); ok {
				if mac := suffixMac(macPart); mac != "" {
					entries[mac] = pduNumber(p)
				}
			}
		}
	}
	if len(entries) == 0 {
		return nil
	}
	rows, err := c.store.db.QueryContext(ctx, "SELECT id,if_index FROM interfaces WHERE device_id=?", deviceID)
	if err != nil {
		return err
	}
	byIndex := map[uint64]int64{}
	for rows.Next() {
		var id int64
		var index uint64
		if rows.Scan(&id, &index) == nil {
			byIndex[index] = id
		}
	}
	rows.Close()
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for mac, port := range entries {
		index := port
		if mapped, ok := bridgePorts[strconv.FormatUint(port, 10)]; ok {
			index = mapped
		}
		id, ok := byIndex[index]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fdb(interface_id,mac,last_seen) VALUES(?,?,?)
			ON CONFLICT(interface_id,mac) DO UPDATE SET last_seen=excluded.last_seen`, id, mac, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// reconcileForwarding links port P on device A with port Q on device B when A has learned B only
// on P, B has learned A only on Q, and no other registered device is learned on both P and Q.
// A registered device seen on both ports sits between A and B, so the link is not direct.
func (c *collector) reconcileForwarding(ctx context.Context) {
	owners := map[string]int64{}
	ambiguous := map[string]bool{}
	rows, err := c.store.db.QueryContext(ctx, "SELECT mac,device_id FROM interfaces WHERE mac!='' UNION SELECT mac,device_id FROM device_macs")
	if err != nil {
		return
	}
	for rows.Next() {
		var mac string
		var device int64
		if rows.Scan(&mac, &device) != nil {
			continue
		}
		if owner, ok := owners[mac]; ok && owner != device {
			ambiguous[mac] = true
		}
		owners[mac] = device
	}
	rows.Close()
	type port struct {
		device int64
		seen   map[int64]bool
	}
	ports := map[int64]*port{}
	rows, err = c.store.db.QueryContext(ctx, "SELECT f.interface_id,i.device_id,f.mac FROM fdb f JOIN interfaces i ON i.id=f.interface_id")
	if err != nil {
		return
	}
	for rows.Next() {
		var iface, device int64
		var mac string
		if rows.Scan(&iface, &device, &mac) != nil {
			continue
		}
		owner, ok := owners[mac]
		if !ok || ambiguous[mac] || owner == device {
			continue
		}
		p := ports[iface]
		if p == nil {
			p = &port{device: device, seen: map[int64]bool{}}
			ports[iface] = p
		}
		p.seen[owner] = true
	}
	rows.Close()
	// A discovered link is wrong once a registered device is learned on both of its ports: that
	// device sits between them, typically because it was registered after the link was created.
	rows, err = c.store.db.QueryContext(ctx, `SELECT l.id,l.a_interface_id,x.device_id,l.b_interface_id,y.device_id FROM links l
		JOIN interfaces x ON x.id=l.a_interface_id JOIN interfaces y ON y.id=l.b_interface_id WHERE l.source='mac'`)
	if err != nil {
		return
	}
	var stale []int64
	for rows.Next() {
		var id, first, firstDevice, second, secondDevice int64
		if rows.Scan(&id, &first, &firstDevice, &second, &secondDevice) != nil || ports[first] == nil || ports[second] == nil {
			continue
		}
		for other := range ports[first].seen {
			if other != secondDevice && other != firstDevice && ports[second].seen[other] {
				stale = append(stale, id)
				break
			}
		}
	}
	rows.Close()
	for _, id := range stale {
		_, _ = c.store.db.ExecContext(ctx, "DELETE FROM links WHERE id=? AND source='mac'", id)
	}
	towards := map[[2]int64][]int64{}
	for id, p := range ports {
		for other := range p.seen {
			key := [2]int64{p.device, other}
			towards[key] = append(towards[key], id)
		}
	}
	for key, aPorts := range towards {
		a, b := key[0], key[1]
		bPorts := towards[[2]int64{b, a}]
		if a >= b || len(aPorts) != 1 || len(bPorts) != 1 {
			continue
		}
		first, second := aPorts[0], bPorts[0]
		direct := true
		for other := range ports[first].seen {
			if other != b && ports[second].seen[other] {
				direct = false
				break
			}
		}
		if !direct {
			continue
		}
		if first > second {
			first, second = second, first
		}
		var existing int
		// Keep existing links between the devices, links on either port, and links the user removed.
		err := c.store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM links l JOIN interfaces x ON x.id=l.a_interface_id JOIN interfaces y ON y.id=l.b_interface_id
			WHERE (x.device_id=? AND y.device_id=?) OR (x.device_id=? AND y.device_id=?)
			OR l.a_interface_id IN (?,?) OR l.b_interface_id IN (?,?))
			+ (SELECT COUNT(*) FROM suppressed_links WHERE a_interface_id=? AND b_interface_id=?)`,
			a, b, b, a, first, second, first, second, first, second).Scan(&existing)
		if err != nil || existing > 0 {
			continue
		}
		_, _ = c.store.db.ExecContext(ctx, "INSERT OR IGNORE INTO links(a_interface_id,b_interface_id,source) VALUES(?,?,'mac')", first, second)
	}
}

func (c *collector) sampleLinks(ctx context.Context, start time.Time) {
	rows, err := c.store.db.QueryContext(ctx, `SELECT l.id,a.rx_bps,a.tx_bps,a.last_sample,b.rx_bps,b.tx_bps,b.last_sample
		FROM links l JOIN interfaces a ON a.id=l.a_interface_id JOIN interfaces b ON b.id=l.b_interface_id`)
	if err != nil {
		return
	}
	type sample struct {
		id     int64
		rx, tx float64
	}
	var samples []sample
	for rows.Next() {
		var id int64
		var ar, at, br, bt sql.NullFloat64
		var as, bs sql.NullInt64
		if rows.Scan(&id, &ar, &at, &as, &br, &bt, &bs) != nil {
			continue
		}
		if ar.Valid && at.Valid && as.Valid && as.Int64 >= start.Unix() {
			samples = append(samples, sample{id, ar.Float64, at.Float64})
		} else if br.Valid && bt.Valid && bs.Valid && bs.Int64 >= start.Unix() {
			samples = append(samples, sample{id, bt.Float64, br.Float64})
		}
	}
	rows.Close()
	ts := time.Now().Unix()
	minute := ts - ts%60
	for _, x := range samples {
		_, _ = c.store.db.ExecContext(ctx, "INSERT OR REPLACE INTO samples(link_id,ts,rx_bps,tx_bps) VALUES(?,?,?,?)", x.id, ts, x.rx, x.tx)
		_, _ = c.store.db.ExecContext(ctx, `INSERT INTO rollups(link_id,minute,rx_bps,tx_bps,count) VALUES(?,?,?,?,1)
			ON CONFLICT(link_id,minute) DO UPDATE SET rx_bps=(rollups.rx_bps*rollups.count+excluded.rx_bps)/(rollups.count+1),
			tx_bps=(rollups.tx_bps*rollups.count+excluded.tx_bps)/(rollups.count+1),count=rollups.count+1`, x.id, minute, x.rx, x.tx)
	}
}
