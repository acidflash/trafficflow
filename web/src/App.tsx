import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { ReactFlow, Background, BaseEdge, Controls, EdgeLabelRenderer, getSmoothStepPath, Handle, Position, useNodesState, useStore, type Edge, type EdgeProps, type Node, type NodeProps, type ReactFlowInstance } from '@xyflow/react'
import { AlertTriangle, ArrowDownRight, ArrowUpRight, Bell, BellOff, BellRing, Cable, ChevronRight, Clock3, Cloud, LogOut, MousePointerClick, Network, Pencil, Plus, RefreshCw, Search, Send, Trash2, X } from 'lucide-react'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import '@xyflow/react/dist/style.css'
import { arrangeMap } from './layout'
import { DeviceIcon, TopologyMark, deviceKind } from './icons'
import { api, type Alarm, type AlertSettings, type Candidate, type DismissedCandidate, type Device, type Interface, type Point, type Topology } from './api'

type Selection = { kind: 'device' | 'link'; id: number } | null
type CloudTraffic = { inbound: number | null; outbound: number | null; capacity: number }
type NetworkNode = Node<{ device: Device; traffic: number | null; cloud?: CloudTraffic }>

function formatRate(value: number | null | undefined) {
  if (value == null || !Number.isFinite(value)) return '–'
  const units = ['bit/s', 'kbit/s', 'Mbit/s', 'Gbit/s', 'Tbit/s']
  let n = value, unit = 0
  while (n >= 1000 && unit < units.length - 1) { n /= 1000; unit++ }
  return `${n >= 100 ? n.toFixed(0) : n >= 10 ? n.toFixed(1) : n.toFixed(2)} ${units[unit]}`
}
function formatTime(value: number | null | undefined) { return value ? new Date(value * 1000).toLocaleString('sv-SE') : 'Ingen mätning' }
function addressLabel(device: Device | undefined) { if (device?.os === 'external') return 'Extern operatör'; return device && device.resolved && device.resolved !== device.address ? `${device.address} (${device.resolved})` : device?.address }

// Load bands of 20 %. The lowest band is deliberately muted: a healthy network should look calm,
// and colour should only draw the eye as a link fills up.
const loadBands = [
  { max: 20, color: 'oklch(56% .04 175)', label: '0–20 %' },
  { max: 40, color: 'oklch(74% .12 170)', label: '21–40 %' },
  { max: 60, color: 'oklch(84% .15 100)', label: '41–60 %' },
  { max: 80, color: 'oklch(75% .17 58)', label: '61–80 %' },
  { max: Infinity, color: 'oklch(66% .21 27)', label: '81–100 %' },
]
const noDataColor = 'oklch(42% .012 165)', downColor = 'oklch(62% .19 27)', selectColor = 'oklch(92% .03 200)'
const chartIn = 'oklch(80% .09 225)', chartOut = 'oklch(82% .1 78)'
function loadColor(load: number | null) { return load == null ? noDataColor : loadBands.find(b => Math.round(load) <= b.max)!.color }

function NetworkDevice({ data, selected }: NodeProps<NetworkNode>) {
  const { device, traffic, cloud } = data
  const offline = device.status === 'offline'
  return <div className={`node node-${device.os} ${offline ? 'is-offline' : ''} ${selected ? 'is-selected' : ''}`}>
    <Handle type="target" position={Position.Top} className="flow-handle" />
    <div className="node-head">
      <span className="node-icon"><DeviceIcon os={device.os} size={17}/>{device.os !== 'external' && <span className={`status-dot ${device.status}`}/>}</span>
      <strong title={device.name}>{device.name}</strong>
    </div>
    <div className="node-meta"><span>{deviceKind(device.os)}</span>{cloud ? cloud.capacity > 0 && <span>avtal {formatRate(cloud.capacity)}</span> : <span className="mono" title={addressLabel(device)}>{device.address}</span>}</div>
    <div className="node-rate">{offline ? <span className="node-fault"><AlertTriangle size={12}/> Svarar inte</span> : cloud
      ? <><span title="In till nätet">↓ {formatRate(cloud.inbound)}</span><span title="Ut från nätet">↑ {formatRate(cloud.outbound)}</span></>
      : <span title="Mottaget på alla portar">↓ {formatRate(traffic)}</span>}</div>
    <Handle type="source" position={Position.Bottom} className="flow-handle" />
  </div>
}
const nodeTypes = { networkDevice: NetworkDevice }

type TrafficEdge = Edge<{ down: number | null; up: number | null; load: number | null; linkUp: boolean; labelAtSource: boolean }, 'traffic'>
// The rate chip sits at the lower end of the link, just above the device it leads to. Links from
// one device share the horizontal segment, but each has its own vertical segment there. When
// several links end in the same device (clouds above a router), the chip moves to the upper end.
// When zoomed out, chips are scaled up (up to 2x) so the rates stay readable.
function TrafficEdgeView({ id, sourceX, sourceY, sourcePosition, targetX, targetY, targetPosition, style, data, selected }: EdgeProps<TrafficEdge>) {
  const [path] = getSmoothStepPath({ sourceX, sourceY, sourcePosition, targetX, targetY, targetPosition })
  const scale = useStore(s => Math.min(2, Math.max(1, 1 / s.transform[2])))
  const load = data?.load ?? null
  const place = data?.labelAtSource
    ? { transform: `translate(-50%, 0) translate(${sourceX}px, ${sourceY + 10}px) scale(${scale})`, transformOrigin: '50% 0' }
    : { transform: `translate(-50%, -100%) translate(${targetX}px, ${targetY - 10}px) scale(${scale})`, transformOrigin: '50% 100%' }
  return <><BaseEdge id={id} path={path} style={style}/>
    <EdgeLabelRenderer><div className={`edge-chip ${selected ? 'is-selected' : ''} ${data?.linkUp ? '' : 'is-down'}`} style={place}>
      {data?.linkUp ? <>
        <span className="edge-rate" title="Mot enheten nedanför">↓ {formatRate(data.down)}</span>
        <span className="edge-rate" title="Mot enheten ovanför">↑ {formatRate(data.up)}</span>
        {load != null && <span className="edge-load" title={`${Math.round(load)} % av länkens kapacitet`}><span className="meter"><i style={{ width: `${Math.min(100, Math.max(3, load))}%`, background: loadColor(load) }}/></span>{Math.round(load)} %</span>}
      </> : <span className="edge-fault">Nere</span>}
    </div></EdgeLabelRenderer></>
}
const edgeTypes = { traffic: TrafficEdgeView }

// Traffic of a cloud, from the real ports linked to it: inbound is what our side receives.
function cloudTraffic(topology: Topology, device: Device): CloudTraffic {
  const port = topology.interfaces.find(i => i.deviceId === device.id)
  let inbound: number | null = null, outbound: number | null = null
  for (const link of topology.links) {
    const peerId = link.aInterfaceId === port?.id ? link.bInterfaceId : link.bInterfaceId === port?.id ? link.aInterfaceId : null
    const peer = peerId == null ? undefined : topology.interfaces.find(i => i.id === peerId)
    if (!peer?.lastSample || topology.serverTime - peer.lastSample > 45 || peer.rxBps == null || peer.txBps == null) continue
    inbound = (inbound ?? 0) + peer.rxBps
    outbound = (outbound ?? 0) + peer.txBps
  }
  return { inbound, outbound, capacity: port?.speedBps || 0 }
}

function FormTitle({ title, onCancel }: { title: string; onCancel: () => void }) {
  return <div className="form-title"><strong>{title}</strong><button type="button" className="icon-button" onClick={onCancel} aria-label="Stäng"><X size={16}/></button></div>
}

function CloudForm({ cloud, capacity, onDone, onCancel }: { cloud?: Device; capacity?: number; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState(cloud?.name || ''), [mbit, setMbit] = useState(capacity ? String(capacity / 1e6) : ''), [error, setError] = useState(''), [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    const body = JSON.stringify({ name, capacityBps: mbit.trim() ? Math.round(Number(mbit) * 1e6) : 0 })
    try { await api(cloud ? `/externals/${cloud.id}` : '/externals', { method: cloud ? 'PATCH' : 'POST', body }); onDone() }
    catch (e) { setError((e as Error).message) } finally { setBusy(false) }
  }
  return <form className="inline-form" onSubmit={submit}><FormTitle title={cloud ? 'Redigera moln' : 'Nytt moln'} onCancel={onCancel}/>
    <label>Operatör<input value={name} onChange={e => setName(e.target.value)} placeholder="T.ex. Arelion, Telia eller IX-namn" maxLength={64} required /></label>
    <label>Avtalad kapacitet, Mbit/s (valfritt)<input value={mbit} onChange={e => setMbit(e.target.value)} type="number" min={0} step="any" inputMode="decimal" placeholder="Tomt = portens hastighet" /></label>
    {!cloud && <p className="muted">Koppla sedan molnet till routerns port med Ny länk. Trafiken mäts på routerns port.</p>}
    {error && <div className="form-error" role="alert">{error}</div>}<button className="primary" disabled={busy}>{busy ? 'Sparar…' : cloud ? 'Spara ändringar' : 'Skapa moln'}</button>
  </form>
}

function Login({ onLogin }: { onLogin: () => void }) {
  const [username, setUsername] = useState('admin'), [password, setPassword] = useState(''), [error, setError] = useState(''), [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { await api('/login', { method: 'POST', body: JSON.stringify({ username, password }) }); onLogin() }
    catch (e) { setError((e as Error).message) } finally { setBusy(false) }
  }
  return <main className="login-page"><section className="login-panel">
    <div className="brand"><span className="brand-icon"><TopologyMark size={18}/></span><strong>Trafficflow</strong></div>
    <p className="login-lead">Logga in för att se nätet.</p>
    <form onSubmit={submit}><label>Användarnamn<input autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required /></label><label>Lösenord<input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} required autoFocus /></label>
      {error && <div className="form-error" role="alert">{error}</div>}<button className="primary" disabled={busy}>{busy ? 'Loggar in…' : 'Logga in'}</button></form>
  </section></main>
}

const authOptions = <><option value="SHA1">SHA1</option><option value="MD5">MD5</option><option value="SHA256">SHA256</option></>

function AddDevice({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [os, setOs] = useState<'routeros' | 'swos'>('routeros'), [address, setAddress] = useState(''), [community, setCommunity] = useState(''), [user, setUser] = useState(''), [authPassword, setAuthPassword] = useState(''), [privPassword, setPrivPassword] = useState(''), [authProtocol, setAuthProtocol] = useState('SHA1'), [version, setVersion] = useState<'3' | '2c'>('3'), [error, setError] = useState(''), [busy, setBusy] = useState(false)
  const v2c = os === 'swos' || version === '2c'
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { await api('/devices', { method: 'POST', body: JSON.stringify({ address, os, snmpVersion: v2c ? '2c' : '3', community, user, authPassword, privPassword, authProtocol: v2c ? '' : authProtocol }) }); onDone() }
    catch (e) { setError((e as Error).message) } finally { setBusy(false) }
  }
  return <form className="inline-form" onSubmit={submit}><FormTitle title="Ny enhet" onCancel={onCancel}/>
    <label>Operativsystem<select value={os} onChange={e => setOs(e.target.value as 'routeros' | 'swos')}><option value="routeros">RouterOS 7</option><option value="swos">SwOS · SNMPv2c</option></select></label>
    {os === 'routeros' && <label>SNMP-version<select value={version} onChange={e => setVersion(e.target.value as '3' | '2c')}><option value="3">SNMPv3 · krypterad (rekommenderas)</option><option value="2c">SNMPv2c · okrypterad</option></select></label>}
    <label>IP-adress eller DNS-namn<input value={address} onChange={e => setAddress(e.target.value)} placeholder="192.168.1.1 eller switch1.example.lan" autoCapitalize="off" spellCheck={false} required /></label>
    {v2c ? <label>SNMP-community<input value={community} onChange={e => setCommunity(e.target.value)} type="password" required /></label> : <><label>SNMPv3-användare<input value={user} onChange={e => setUser(e.target.value)} required /></label><label>Autentiseringsprotokoll<select value={authProtocol} onChange={e => setAuthProtocol(e.target.value)}>{authOptions}</select></label><label>Autentiseringslösenord<input value={authPassword} onChange={e => setAuthPassword(e.target.value)} type="password" minLength={8} required /></label><label>Krypteringslösenord, AES<input value={privPassword} onChange={e => setPrivPassword(e.target.value)} type="password" minLength={8} required /></label></>}
    {error && <div className="form-error" role="alert">{error}</div>}<button className="primary" disabled={busy}>{busy ? 'Sparar…' : 'Lägg till enhet'}</button>
  </form>
}

function AddLink({ topology, onDone, onCancel }: { topology: Topology; onDone: () => void; onCancel: () => void }) {
  const [a, setA] = useState(''), [b, setB] = useState(''), [error, setError] = useState(''), [busy, setBusy] = useState(false)
  const interfaces = topology.interfaces.filter(i => topology.devices.some(d => d.id === i.deviceId))
  const label = (i: Interface) => `${topology.devices.find(d => d.id === i.deviceId)?.name || '?'} / ${i.name}`
  async function submit(event: FormEvent) { event.preventDefault(); setBusy(true); setError(''); try { await api('/links', { method: 'POST', body: JSON.stringify({ aInterfaceId: Number(a), bInterfaceId: Number(b) }) }); onDone() } catch (e) { setError((e as Error).message) } finally { setBusy(false) } }
  return <form className="inline-form" onSubmit={submit}><FormTitle title="Ny länk" onCancel={onCancel}/>
    <label>Port A<select value={a} onChange={e => setA(e.target.value)} required><option value="">Välj port</option>{interfaces.map(i => <option key={i.id} value={i.id}>{label(i)}</option>)}</select></label>
    <label>Port B<select value={b} onChange={e => setB(e.target.value)} required><option value="">Välj port</option>{interfaces.filter(i => topology.interfaces.find(x => x.id === Number(a))?.deviceId !== i.deviceId).map(i => <option key={i.id} value={i.id}>{label(i)}</option>)}</select></label>
    {error && <div className="form-error" role="alert">{error}</div>}<button className="primary" disabled={busy || !a || !b}>Skapa länk</button>
  </form>
}

function since(seconds: number) { const m = Math.max(0, Math.round(seconds / 60)); return m < 60 ? `${m} min` : m < 2880 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${Math.floor(m / 1440)} dygn` }

type Channel = 'discord' | 'email'

function AlertPanel({ topology, onSelect, onCancel }: { topology: Topology; onSelect: (deviceId: number) => void; onCancel: () => void }) {
  const [settings, setSettings] = useState<AlertSettings | null>(null), [recent, setRecent] = useState<Alarm[]>([]), [webhook, setWebhook] = useState(''), [password, setPassword] = useState(''), [to, setTo] = useState(''), [error, setError] = useState(''), [notice, setNotice] = useState(''), [busy, setBusy] = useState(false), [showHistory, setShowHistory] = useState(false)
  const [testing, setTesting] = useState<Channel | null>(null), [results, setResults] = useState<Partial<Record<Channel, { ok: boolean; text: string }>>>({})
  useEffect(() => { api<AlertSettings>('/alert-settings').then(s => { setSettings(s); setTo(s.to.join(', ')) }).catch(e => setError((e as Error).message)); api<{ recent: Alarm[] }>('/alarms').then(r => setRecent(r.recent)).catch(() => {}) }, [])
  const set = (patch: Partial<AlertSettings>) => setSettings(s => s && { ...s, ...patch })
  async function save() {
    if (!settings) return false
    const body = { discordEnabled: settings.discordEnabled, discordWebhook: webhook.trim(), emailEnabled: settings.emailEnabled, smtpHost: settings.smtpHost, smtpPort: Number(settings.smtpPort), smtpSecurity: settings.smtpSecurity, smtpUser: settings.smtpUser, smtpPassword: password, from: settings.from, to: to.split(/[,;\s]+/).filter(Boolean) }
    const saved = await api<AlertSettings>('/alert-settings', { method: 'PUT', body: JSON.stringify(body) })
    setSettings(saved); setTo(saved.to.join(', ')); setWebhook(''); setPassword('')
    return true
  }
  async function submit(event: FormEvent) { event.preventDefault(); setBusy(true); setError(''); setNotice(''); try { if (await save()) setNotice('Inställningarna är sparade.') } catch (e) { setError((e as Error).message) } finally { setBusy(false) } }
  // Saves first so the test uses what is in the form, then sends through that channel only.
  async function test(channel: Channel) {
    setTesting(channel); setError(''); setNotice(''); setResults(r => ({ ...r, [channel]: undefined }))
    try { await save(); await api('/alert-settings/test', { method: 'POST', body: JSON.stringify({ channel }) }); setResults(r => ({ ...r, [channel]: { ok: true, text: channel === 'discord' ? 'Skickat – kolla kanalen.' : 'Skickat – kolla inkorgen.' } })) }
    catch (e) { setResults(r => ({ ...r, [channel]: { ok: false, text: (e as Error).message } })) } finally { setTesting(null) }
  }
  const testButton = (channel: Channel) => <div className="channel-test"><button type="button" className="secondary" disabled={busy || testing !== null} onClick={() => test(channel)}><Send size={14}/> {testing === channel ? 'Skickar…' : channel === 'discord' ? 'Testa Discord' : 'Testa e-post'}</button>{results[channel] && <span className={results[channel]!.ok ? 'test-ok' : 'test-fail'} role="status">{results[channel]!.text}</span>}</div>
  const alarms = topology.alarms
  return <form className="inline-form alert-panel" onSubmit={submit}><FormTitle title="Larm" onCancel={onCancel}/>
    {alarms.length ? <div className="alarm-list">{alarms.map(a => <button type="button" key={a.id} className="alarm-item" onClick={() => a.deviceId && onSelect(a.deviceId)}><span className="status-dot offline"/><span><strong>{a.title}</strong><small>{a.kind === 'device' ? 'Svarar inte' : 'Porten är nere'} · {since(topology.serverTime - a.startedAt)}</small></span></button>)}</div> : <p className="muted">Inga aktiva larm. Enheter som slutar svara och länkportar som går ner larmar efter ungefär 30 sekunder.</p>}
    {settings ? <>
      <fieldset className="channel"><label className="check"><input type="checkbox" checked={settings.discordEnabled} onChange={e => set({ discordEnabled: e.target.checked })}/> Discord</label>
        {settings.discordEnabled && <label>Webhook-URL<input value={webhook} onChange={e => setWebhook(e.target.value)} type="password" autoComplete="off" placeholder={settings.hasDiscordWebhook ? 'Sparad – lämna tomt' : 'https://discord.com/api/webhooks/…'} required={!settings.hasDiscordWebhook}/></label>}{settings.discordEnabled && testButton('discord')}</fieldset>
      <fieldset className="channel"><label className="check"><input type="checkbox" checked={settings.emailEnabled} onChange={e => set({ emailEnabled: e.target.checked })}/> E-post</label>
        {settings.emailEnabled && <>
          <label>SMTP-server<input value={settings.smtpHost} onChange={e => set({ smtpHost: e.target.value })} placeholder="smtp.example.com" autoCapitalize="off" spellCheck={false} required/></label>
          <div className="field-pair"><label>Port<input value={settings.smtpPort} onChange={e => set({ smtpPort: Number(e.target.value) })} type="number" min={1} max={65535} required/></label><label>Kryptering<select value={settings.smtpSecurity} onChange={e => { const v = e.target.value as AlertSettings['smtpSecurity']; set({ smtpSecurity: v, smtpPort: v === 'tls' ? 465 : v === 'starttls' ? 587 : 25 }) }}><option value="starttls">STARTTLS</option><option value="tls">TLS</option><option value="none">Ingen</option></select></label></div>
          <label>Användare (valfritt)<input value={settings.smtpUser} onChange={e => set({ smtpUser: e.target.value })} autoComplete="off"/></label>
          {settings.smtpUser && <label>Lösenord<input value={password} onChange={e => setPassword(e.target.value)} type="password" autoComplete="new-password" placeholder={settings.hasSmtpPassword ? 'Sparat – lämna tomt' : ''}/></label>}
          <label>Avsändare<input value={settings.from} onChange={e => set({ from: e.target.value })} type="email" placeholder="trafficflow@example.com" required/></label>
          <label>Mottagare<input value={to} onChange={e => setTo(e.target.value)} placeholder="noc@example.com, jour@example.com" required/></label>
          {testButton('email')}
        </>}</fieldset>
      {settings.lastError && <div className="form-error" role="alert">Senaste utskick misslyckades {new Date(settings.lastErrorAt * 1000).toLocaleTimeString('sv-SE')}: {settings.lastError}</div>}
      {error && <div className="form-error" role="alert">{error}</div>}{notice && <p className="form-notice" role="status">{notice}</p>}
      <button className="primary" disabled={busy || testing !== null}>{busy ? 'Sparar…' : 'Spara'}</button>
    </> : !error ? <p className="muted">Hämtar inställningar…</p> : <div className="form-error" role="alert">{error}</div>}
    {recent.length > 0 && <div><button type="button" className="dismissed-toggle history-toggle" aria-expanded={showHistory} onClick={() => setShowHistory(!showHistory)}><ChevronRight size={14} className={showHistory ? 'open' : ''}/> Historik <span>{recent.length}</span></button>
      {showHistory && <div className="alarm-history">{recent.map(a => <div key={a.id}><strong>{a.title}</strong><small>{new Date(a.startedAt * 1000).toLocaleString('sv-SE', { dateStyle: 'short', timeStyle: 'short' })} · nere {since((a.clearedAt || a.startedAt) - a.startedAt)}</small></div>)}</div>}</div>}
  </form>
}

function EditDevice({ device, onDone, onCancel }: { device: Device; onDone: () => void; onCancel: () => void }) {
  const [address, setAddress] = useState(device.address), [community, setCommunity] = useState(''), [user, setUser] = useState(''), [authPassword, setAuthPassword] = useState(''), [privPassword, setPrivPassword] = useState(''), [authProtocol, setAuthProtocol] = useState(''), [error, setError] = useState(''), [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { await api(`/devices/${device.id}`, { method: 'PATCH', body: JSON.stringify({ address, community, user, authPassword, privPassword, authProtocol }) }); onDone() }
    catch (e) { setError((e as Error).message) } finally { setBusy(false) }
  }
  return <form className="inline-form" onSubmit={submit}><FormTitle title="Redigera enhet" onCancel={onCancel}/>
    <label>IP-adress eller DNS-namn<input value={address} onChange={e => setAddress(e.target.value)} autoCapitalize="off" spellCheck={false} required /></label>
    {device.snmpVersion === '2c' ? <label>SNMP-community<input value={community} onChange={e => setCommunity(e.target.value)} type="password" placeholder="Oförändrad" autoComplete="off" /></label> : <><label>SNMPv3-användare<input value={user} onChange={e => setUser(e.target.value)} placeholder="Oförändrad" autoComplete="off" /></label><label>Autentiseringsprotokoll<select value={authProtocol} onChange={e => setAuthProtocol(e.target.value)}><option value="">Oförändrat</option>{authOptions}</select></label><label>Autentiseringslösenord<input value={authPassword} onChange={e => setAuthPassword(e.target.value)} type="password" minLength={8} placeholder="Oförändrat" autoComplete="new-password" /></label><label>Krypteringslösenord, AES<input value={privPassword} onChange={e => setPrivPassword(e.target.value)} type="password" minLength={8} placeholder="Oförändrat" autoComplete="new-password" /></label></>}
    <p className="muted">Lämna SNMP-fälten tomma för att behålla sparade uppgifter. Portar, länkar och historik behålls.</p>
    {error && <div className="form-error" role="alert">{error}</div>}<button className="primary" disabled={busy}>{busy ? 'Sparar…' : 'Spara ändringar'}</button>
  </form>
}

function PanelHead({ kind, icon, title, subtitle, onClose }: { kind: string; icon: ReactNode; title: ReactNode; subtitle?: ReactNode; onClose: () => void }) {
  return <header className="panel-head"><span className="panel-icon">{icon}</span><div><span className="panel-kind">{kind}</span><h2>{title}</h2>{subtitle && <p className="panel-subtitle">{subtitle}</p>}</div><button className="icon-button" aria-label="Stäng detaljer" onClick={onClose}><X size={17}/></button></header>
}

function DetailPanel({ topology, selection, onClose, onDelete, onSaved, onError }: { topology: Topology; selection: Selection; onClose: () => void; onDelete: () => void; onSaved: () => void; onError: (message: string) => void }) {
  const [editing, setEditing] = useState(false), [range, setRange] = useState('1h'), [points, setPoints] = useState<Point[]>([]), [loading, setLoading] = useState(false)
  const link = selection?.kind === 'link' ? topology.links.find(l => l.id === selection.id) : undefined
  const device = selection?.kind === 'device' ? topology.devices.find(d => d.id === selection.id) : undefined
  const a = link && topology.interfaces.find(i => i.id === link.aInterfaceId), b = link && topology.interfaces.find(i => i.id === link.bInterfaceId)
  const da = a && topology.devices.find(d => d.id === a.deviceId), db = b && topology.devices.find(d => d.id === b.deviceId)
  useEffect(() => { if (!link) return; let live = true; setLoading(true); api<{ points: Point[] }>(`/history?link=${link.id}&range=${range}`).then(data => { if (live) setPoints(data.points) }).catch(e => { if (live) onError((e as Error).message) }).finally(() => { if (live) setLoading(false) }); return () => { live = false } }, [link?.id, range, topology.serverTime])
  useEffect(() => { setEditing(false) }, [selection?.kind, selection?.id])
  if (!selection || (!link && !device)) return <aside className="details empty-details"><MousePointerClick size={22}/><h2>Välj en enhet eller länk</h2><p>Klicka på kartan för att se portar, status och trafikhistorik.</p></aside>
  async function remove(kind: 'devices' | 'links', id: number) { if (!window.confirm(kind === 'devices' ? 'Ta bort enheten och dess länkar?' : 'Ta bort länken?')) return; try { await api(`/${kind}/${id}`, { method: 'DELETE' }); onDelete() } catch (e) { onError((e as Error).message) } }
  const fresh = (i: Interface) => i.lastSample != null && topology.serverTime - i.lastSample <= 45
  if (device?.os === 'external') {
    const port = topology.interfaces.find(i => i.deviceId === device.id), traffic = cloudTraffic(topology, device)
    const peers = topology.links.flatMap(l => { const peerId = l.aInterfaceId === port?.id ? l.bInterfaceId : l.bInterfaceId === port?.id ? l.aInterfaceId : null; const peer = topology.interfaces.find(i => i.id === peerId); return peer ? [{ link: l, peer, owner: topology.devices.find(d => d.id === peer.deviceId) }] : [] })
    return <aside className="details">
      <PanelHead kind="Moln" icon={<Cloud size={20} strokeWidth={1.7}/>} title={device.name} subtitle="Extern operatör" onClose={onClose}/>
      {editing && <CloudForm key={device.id} cloud={device} capacity={port?.speedBps} onCancel={() => setEditing(false)} onDone={() => { setEditing(false); onSaved() }}/>}
      <div className="traffic-pair"><div><span><ArrowDownRight size={14}/> In till nätet</span><strong>{formatRate(traffic.inbound)}</strong></div><div><span><ArrowUpRight size={14}/> Ut från nätet</span><strong>{formatRate(traffic.outbound)}</strong></div></div>
      <dl className="facts"><div><dt>Avtalad kapacitet</dt><dd>{port?.speedBps ? formatRate(port.speedBps) : 'Portens hastighet'}</dd></div></dl>
      <div className="section-heading"><h3>Anslutningar</h3><span>{peers.length}</span></div>
      {peers.length ? <div className="port-list">{peers.map(({ link, peer, owner }) => <div className="port-row" key={link.id}><span className={`status-dot ${peer.status}`}/><div><strong>{owner?.name || 'Okänd'} / {peer.name}</strong><small>{formatRate(peer.speedBps)}</small></div><div className="port-rate">{fresh(peer) ? formatRate(peer.rxBps) : '–'}<small>in</small></div></div>)}</div> : <p className="muted">Inte kopplat ännu. Använd Ny länk och välj {device.name} / Anslutning mot routerns port.</p>}
      <div className="detail-foot">{!editing && <button className="secondary" onClick={() => setEditing(true)}><Pencil size={15}/> Redigera moln</button>}<button className="text-danger" onClick={() => remove('devices', device.id)}><Trash2 size={15}/> Ta bort moln</button></div>
    </aside>
  }
  if (device) {
    const ports = topology.interfaces.filter(i => i.deviceId === device.id)
    const up = ports.filter(i => i.status === 'up').length
    const linked = new Set(topology.links.flatMap(l => [l.aInterfaceId, l.bInterfaceId])), alarmed = new Set(topology.alarms.map(a => a.interfaceId))
    async function toggleAlert(i: Interface) { try { await api(`/interfaces/${i.id}`, { method: 'PATCH', body: JSON.stringify({ alert: !i.alert }) }); onSaved() } catch (e) { onError((e as Error).message) } }
    const alertButton = (i: Interface) => { const always = linked.has(i.id), on = always || i.alert; return <button className={`port-alert ${on ? 'on' : ''}`} disabled={always} aria-pressed={on} aria-label={`Larm för ${i.name}`} title={always ? 'Länkport – bevakas alltid' : on ? 'Larmar när porten går ner. Klicka för att stänga av.' : 'Larma när porten går ner'} onClick={() => toggleAlert(i)}>{on ? <BellRing size={14}/> : <BellOff size={14}/>}</button> }
    return <aside className="details">
      <PanelHead kind={deviceKind(device.os)} icon={<DeviceIcon os={device.os} size={20}/>} title={device.name} subtitle={<span className="mono">{addressLabel(device)}</span>} onClose={onClose}/>
      <div className={`status-line ${device.status}`}><span className={`status-dot ${device.status}`}/>{device.status === 'online' ? 'Svarar' : device.status === 'offline' ? 'Svarar inte' : 'Väntar på första mätningen'}<span className="status-line-time"><Clock3 size={13}/> {formatTime(device.lastSeen)}</span></div>
      {device.lastError && <div className="notice-error">{device.lastError}</div>}
      {editing && <EditDevice key={device.id} device={device} onCancel={() => setEditing(false)} onDone={() => { setEditing(false); onSaved() }}/>}
      <div className="section-heading"><h3>Portar</h3><span>{up} uppe av {ports.length}</span></div>
      {ports.length ? <div className="port-list">{ports.map(i => <div className={`port-row ${i.status} ${alarmed.has(i.id) ? 'is-alarm' : ''}`} key={i.id}><span className={`status-dot ${i.status}`}/><div><strong>{i.name}</strong><small>{i.description && i.description !== i.name ? i.description : i.status === 'up' && i.speedBps ? formatRate(i.speedBps) : i.status === 'down' ? 'Ingen länk' : 'Okänd status'}</small></div>{fresh(i) && i.status === 'up' && <div className="port-rate"><span>↓ {formatRate(i.rxBps)}</span><span>↑ {formatRate(i.txBps)}</span></div>}{alertButton(i)}</div>)}</div> : <p className="muted">Portar visas efter första lyckade SNMP-avläsningen.</p>}
      <div className="detail-foot">{!editing && <button className="secondary" onClick={() => setEditing(true)}><Pencil size={15}/> Redigera enhet</button>}<button className="text-danger" onClick={() => remove('devices', device.id)}><Trash2 size={15}/> Ta bort enhet</button></div>
    </aside>
  }
  const current = a && fresh(a) && a.rxBps != null && a.txBps != null ? { rx: a.rxBps, tx: a.txBps, time: a.lastSample } : b && fresh(b) && b.rxBps != null && b.txBps != null ? { rx: b.txBps, tx: b.rxBps, time: b.lastSample } : null
  const linkUp = a?.status === 'up' && b?.status === 'up'
  const linkSpeed = Math.min(a?.speedBps || Infinity, b?.speedBps || Infinity), linkLoad = current && linkUp && linkSpeed !== Infinity ? Math.max(current.rx, current.tx) / linkSpeed * 100 : null
  return <aside className="details">
    <PanelHead kind={`Länk #${link?.id}`} icon={<Cable size={20} strokeWidth={1.7}/>} title={<>{da?.name || 'Okänd'} <span className="arrow-inline">↔</span> {db?.name || 'Okänd'}</>} subtitle={link?.source === 'manual' ? 'Manuellt skapad' : link?.source === 'confirmed' ? 'Bekräftat förslag' : link?.source === 'mac' ? 'Upptäckt via MAC-tabeller' : 'Bekräftad via LLDP'} onClose={onClose}/>
    <div className={`status-line ${linkUp ? 'online' : 'offline'}`}><span className={`status-dot ${linkUp ? 'online' : 'offline'}`}/>{linkUp ? 'Uppe' : 'Nere eller okänd'}<span className="status-line-time"><Clock3 size={13}/> {formatTime(current?.time)}</span></div>
    <div className="endpoint-grid"><div><small>A</small><strong>{a?.name || '–'}</strong><span>{da?.name}</span></div><div><small>B</small><strong>{b?.name || '–'}</strong><span>{db?.name}</span></div></div>
    <div className="traffic-pair"><div><span><ArrowDownRight size={14}/> In mot A</span><strong>{current ? formatRate(current.rx) : '–'}</strong></div><div><span><ArrowUpRight size={14}/> Ut från A</span><strong>{current ? formatRate(current.tx) : '–'}</strong></div></div>
    <div className="load-row"><div className="load-label"><span>Beläggning</span><strong style={{ color: linkLoad != null && linkLoad > 40 ? loadColor(linkLoad) : undefined }}>{linkLoad == null ? '–' : `${Math.round(linkLoad)} %`}</strong><span>av {formatRate(linkSpeed === Infinity ? null : linkSpeed)}</span></div><span className="meter meter-large"><i style={{ width: `${linkLoad == null ? 0 : Math.min(100, Math.max(1, linkLoad))}%`, background: loadColor(linkLoad) }}/></span></div>
    <div className="section-heading"><h3>Trafikhistorik</h3><div className="range-tabs" role="tablist">{['1h','24h','7d','30d'].map(x => <button key={x} role="tab" aria-selected={range === x} className={range === x ? 'active' : ''} onClick={() => setRange(x)}>{x}</button>)}</div></div>
    <div className="chart-wrap">{loading && !points.length ? <div className="chart-empty">Hämtar mätningar…</div> : points.length ? <ResponsiveContainer width="100%" height="100%"><AreaChart data={points} margin={{ top: 8, right: 4, left: -18, bottom: 0 }}><defs><linearGradient id="rxFill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor={chartIn} stopOpacity={0.28}/><stop offset="100%" stopColor={chartIn} stopOpacity={0}/></linearGradient></defs><CartesianGrid vertical={false} stroke="oklch(30% .01 165)"/><XAxis dataKey="ts" tickFormatter={v => new Date(v * 1000).toLocaleTimeString('sv-SE',{ hour:'2-digit',minute:'2-digit' })} tick={{ fontSize: 10, fill: 'oklch(62% .012 165)' }} axisLine={false} tickLine={false} minTickGap={24}/><YAxis tickFormatter={v => formatRate(v).split(' ')[0]} tick={{ fontSize: 10, fill: 'oklch(62% .012 165)' }} axisLine={false} tickLine={false}/><Tooltip labelFormatter={v => formatTime(Number(v))} formatter={v => formatRate(Number(v))} contentStyle={{ background: 'oklch(23% .01 165)', border: '1px solid oklch(34% .012 165)', borderRadius: 6, fontSize: 12, color: 'oklch(92% .008 165)' }} labelStyle={{ color: 'oklch(72% .012 165)' }} cursor={{ stroke: 'oklch(45% .012 165)' }}/><Area type="monotone" dataKey="rxBps" name="In" stroke={chartIn} fill="url(#rxFill)" strokeWidth={1.8} dot={false} isAnimationActive={false}/><Area type="monotone" dataKey="txBps" name="Ut" stroke={chartOut} fill="none" strokeWidth={1.5} dot={false} isAnimationActive={false}/></AreaChart></ResponsiveContainer> : <div className="chart-empty">Historik visas när länken har mätts.</div>}</div>
    <div className="chart-legend"><span><i style={{ background: chartIn }}/> In mot A</span><span><i style={{ background: chartOut }}/> Ut från A</span></div>
    <div className="detail-foot"><button className="text-danger" onClick={() => link && remove('links', link.id)}><Trash2 size={15}/> Ta bort länk</button></div>
  </aside>
}

export default function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null), [topology, setTopology] = useState<Topology | null>(null), [selection, setSelection] = useState<Selection>(null), [panel, setPanel] = useState<'device' | 'link' | 'cloud' | 'alerts' | null>(null), [error, setError] = useState(''), [query, setQuery] = useState(''), [nodes, setNodes, onNodesChange] = useNodesState<NetworkNode>([])
  const [lastContact, setLastContact] = useState(0), [now, setNow] = useState(Date.now())
  const refresh = useCallback(async () => { try { const data = await api<Topology>('/topology'); setTopology(data); setLastContact(Date.now()); setError('') } catch (e) { if ((e as Error).message.includes('Inloggning') || (e as Error).message.includes('Sessionen')) setAuthenticated(false); else setError((e as Error).message) } }, [])
  useEffect(() => { api('/me').then(() => setAuthenticated(true)).catch(() => setAuthenticated(false)) }, [])
  useEffect(() => { if (!authenticated) return; refresh(); const timer = setInterval(refresh, 15000); return () => clearInterval(timer) }, [authenticated, refresh])
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 5000); return () => clearInterval(timer) }, [])
  useEffect(() => { if (!topology) return; setNodes(existing => { let arranged: ReturnType<typeof arrangeMap> | undefined; return topology.devices.map(device => { const old = existing.find(n => n.id === String(device.id)); const saved = device.x != null && device.y != null ? { x: device.x, y: device.y } : undefined; const current = topology.interfaces.filter(i => i.deviceId === device.id && i.lastSample && topology.serverTime - i.lastSample <= 45 && i.rxBps != null); const position = old?.position || saved || (arranged ??= arrangeMap(topology.devices, topology.interfaces, topology.links)).get(device.id) || { x: 0, y: 0 }; return { id: String(device.id), type: 'networkDevice', position, data: { device, traffic: current.length ? current.reduce((sum, i) => sum + (i.rxBps || 0), 0) : null, cloud: device.os === 'external' ? cloudTraffic(topology, device) : undefined }, selected: selection?.kind === 'device' && selection.id === device.id } }) }) }, [topology, selection, setNodes])
  const flow = useRef<ReactFlowInstance<NetworkNode, TrafficEdge> | null>(null)
  async function savePositions(positions: { id: number; x: number; y: number }[]) { try { await api('/layout', { method: 'PUT', body: JSON.stringify({ positions }) }) } catch (e) { setError((e as Error).message) } }
  function arrange() { if (!topology) return; const positions = arrangeMap(topology.devices, topology.interfaces, topology.links); setNodes(existing => existing.map(n => ({ ...n, position: positions.get(Number(n.id)) || n.position }))); requestAnimationFrame(() => flow.current?.fitView({ padding: 0.15, duration: 300 })); savePositions([...positions].map(([id, p]) => ({ id, ...p }))) }
  const edges = useMemo<TrafficEdge[]>(() => {
    if (!topology) return []
    const y = new Map(nodes.map(n => [n.id, n.position.y]))
    const fresh = (i: Interface) => i.lastSample != null && topology.serverTime - i.lastSample <= 45 && i.rxBps != null && i.txBps != null
    const result = topology.links.flatMap<TrafficEdge>(link => {
      const a = topology.interfaces.find(i => i.id === link.aInterfaceId), b = topology.interfaces.find(i => i.id === link.bInterfaceId)
      if (!a || !b) return []
      // Draw from the upper device to the lower one; "down" is traffic flowing toward the lower device.
      const [upper, lower] = (y.get(String(a.deviceId)) ?? 0) <= (y.get(String(b.deviceId)) ?? 0) ? [a, b] : [b, a]
      const down = fresh(lower) ? lower.rxBps : fresh(upper) ? upper.txBps : null
      const up = fresh(lower) ? lower.txBps : fresh(upper) ? upper.rxBps : null
      const linkUp = a.status === 'up' && b.status === 'up', selected = selection?.kind === 'link' && selection.id === link.id
      // Load is the busier direction as a share of the slower port's speed.
      const speeds = [a.speedBps, b.speedBps].filter(v => v > 0), speed = speeds.length ? Math.min(...speeds) : 0
      const load = linkUp && speed > 0 && down != null && up != null ? Math.max(down, up) / speed * 100 : null
      const stroke = selected ? selectColor : linkUp ? loadColor(load) : downColor
      return [{ id: String(link.id), source: String(upper.deviceId), target: String(lower.deviceId), type: 'traffic', data: { down, up, load, linkUp, labelAtSource: false }, selected, style: { stroke, strokeWidth: selected ? 3.5 : load != null && load > 40 ? 3 : 2, strokeDasharray: linkUp ? undefined : '5 5' } }]
    })
    const incoming = new Map<string, number>()
    for (const e of result) incoming.set(e.target, (incoming.get(e.target) || 0) + 1)
    for (const e of result) e.data!.labelAtSource = (incoming.get(e.target) || 0) > 1
    return result
  }, [topology, selection, nodes])
  const devices = topology?.devices.filter(d => d.os !== 'external') || []
  const online = devices.filter(d => d.status === 'online').length
  const linksUp = edges.filter(e => e.data?.linkUp).length
  const busiest = edges.reduce<TrafficEdge | undefined>((max, e) => e.data?.load != null && (max?.data?.load == null || e.data.load > max.data.load) ? e : max, undefined)
  const alarmCount = topology?.alarms.length || 0
  const stale = lastContact > 0 && now - lastContact > 45000
  const statusRank = (d: Device) => d.status === 'offline' ? 0 : d.os === 'external' ? 2 : 1
  const filteredDevices = (topology?.devices.filter(d => `${d.name} ${d.address} ${d.resolved}`.toLowerCase().includes(query.toLowerCase())) || []).sort((x, y) => statusRank(x) - statusRank(y) || x.name.localeCompare(y.name, 'sv', { numeric: true }))
  const activeCandidates = topology?.candidates.filter(c => !topology.links.some(l => l.aInterfaceId === c.localInterfaceId || l.bInterfaceId === c.localInterfaceId)) || []
  const [showDismissed, setShowDismissed] = useState(false)
  const dismissed = topology?.dismissedCandidates || []
  const portLabel = (id: number) => { const local = topology?.interfaces.find(i => i.id === id), source = topology?.devices.find(d => d.id === local?.deviceId); return `${source?.name || 'Okänd'} / ${local?.name || '?'}` }
  async function dismiss(candidate: Candidate) { try { await api(`/candidates/${candidate.id}/dismiss`, { method: 'POST' }); refresh() } catch (e) { setError((e as Error).message) } }
  async function restore(candidate: DismissedCandidate) { try { await api(`/dismissed-candidates/${candidate.id}`, { method: 'DELETE' }); refresh() } catch (e) { setError((e as Error).message) } }
  async function accept(candidate: Candidate) { try { await api(`/candidates/${candidate.id}/accept`, { method: 'POST' }); refresh() } catch (e) { setError((e as Error).message) } }
  async function logout() { try { await api('/logout', { method: 'POST' }) } finally { setAuthenticated(false); setTopology(null) } }
  if (authenticated === null) return <div className="app-loading"><TopologyMark size={24}/></div>
  if (!authenticated) return <Login onLogin={() => setAuthenticated(true)} />
  return <div className="app-shell"><header className="topbar"><div className="brand"><span className="brand-icon"><TopologyMark size={18}/></span><strong>Trafficflow</strong></div><div className="topbar-actions">
      <span className={`live-indicator ${stale ? 'is-stale' : ''}`} title="Kartan hämtas var 15:e sekund">{stale ? <><AlertTriangle size={13}/> Ingen kontakt sedan {new Date(lastContact).toLocaleTimeString('sv-SE')}</> : <><span className="pulse"/> {topology ? `Uppdaterad ${new Date(topology.serverTime * 1000).toLocaleTimeString('sv-SE')}` : 'Ansluter…'}</>}</span>
      <button className={`icon-button alarm-button ${alarmCount ? 'has-alarms' : ''}`} title={alarmCount ? `${alarmCount} aktiva larm` : 'Larm och aviseringar'} aria-label={alarmCount ? `${alarmCount} aktiva larm` : 'Larm och aviseringar'} aria-expanded={panel === 'alerts'} onClick={() => setPanel(panel === 'alerts' ? null : 'alerts')}>{alarmCount ? <BellRing size={16}/> : <Bell size={16}/>}{alarmCount > 0 && <span className="alarm-count">{alarmCount}</span>}</button><button className="icon-button" title="Uppdatera nu" aria-label="Uppdatera nu" onClick={refresh}><RefreshCw size={16}/></button><button className="icon-button" title="Logga ut" aria-label="Logga ut" onClick={logout}><LogOut size={16}/></button></div></header>
    <div className="workspace"><aside className="sidebar">
      <div className="health">
        <div className={online < devices.length ? 'is-bad' : ''}><span>Svarar</span><strong>{online}<small>/{devices.length}</small></strong></div>
        <div className={linksUp < edges.length ? 'is-bad' : ''}><span>Länkar uppe</span><strong>{linksUp}<small>/{edges.length}</small></strong></div>
        <button className="health-busiest" disabled={!busiest} onClick={() => busiest && setSelection({ kind: 'link', id: Number(busiest.id) })} title="Visa den mest belastade länken"><span>Högst last</span><strong style={{ color: busiest?.data?.load != null && busiest.data.load > 40 ? loadColor(busiest.data.load) : undefined }}>{busiest?.data?.load != null ? `${Math.round(busiest.data.load)} %` : '–'}</strong></button>
      </div>
      <div className="sidebar-actions"><button className="primary" onClick={() => setPanel(panel === 'device' ? null : 'device')}><Plus size={16}/> Enhet</button><button className="secondary" onClick={() => setPanel(panel === 'link' ? null : 'link')}><Cable size={16}/> Länk</button><button className="secondary" onClick={() => setPanel(panel === 'cloud' ? null : 'cloud')}><Cloud size={16}/> Moln</button></div>
      {panel === 'device' && <AddDevice onCancel={() => setPanel(null)} onDone={() => { setPanel(null); refresh() }}/>}{panel === 'cloud' && <CloudForm onCancel={() => setPanel(null)} onDone={() => { setPanel(null); refresh() }}/>}{panel === 'alerts' && topology && <AlertPanel topology={topology} onSelect={id => setSelection({ kind: 'device', id })} onCancel={() => setPanel(null)}/>}{panel === 'link' && topology && <AddLink topology={topology} onCancel={() => setPanel(null)} onDone={() => { setPanel(null); refresh() }}/>}
      <label className="search"><Search size={15}/><input placeholder="Sök enhet, IP eller namn" value={query} onChange={e => setQuery(e.target.value)} aria-label="Sök enhet"/></label>
      <div className="device-list">{filteredDevices.map(d => <button key={d.id} className={`device-list-item ${d.status === 'offline' ? 'is-offline' : ''} ${selection?.kind === 'device' && selection.id === d.id ? 'active' : ''}`} onClick={() => setSelection({kind:'device',id:d.id})}><span className="list-device-icon"><DeviceIcon os={d.os} size={16}/></span><span className="device-list-text"><strong>{d.name}</strong><small className={d.os === 'external' ? '' : 'mono'}>{d.os === 'external' ? 'Moln' : d.address}</small></span>{d.os !== 'external' && <span className={`status-dot ${d.status}`} title={d.status === 'offline' ? 'Svarar inte' : d.status === 'online' ? 'Svarar' : 'Väntar'}/>}</button>)}
        {!filteredDevices.length && <div className="list-empty">{topology?.devices.length ? 'Ingen enhet matchar sökningen.' : 'Lägg till en RouterOS- eller SwOS-enhet för att börja.'}</div>}</div>
      <div className="candidate-section"><div className="list-heading"><h2>Länkförslag</h2><span>{activeCandidates.length}</span></div>{activeCandidates.length ? activeCandidates.map(c => <div className="candidate" key={c.id}><div><strong>{portLabel(c.localInterfaceId)}</strong><small>→ {c.remoteName}{c.remotePort ? ` / ${c.remotePort}` : ''}</small></div><button disabled={!c.remoteDeviceId} onClick={() => accept(c)} title={c.remoteDeviceId ? 'Bekräfta länk' : 'Lägg till motparten först'}>Bekräfta</button><button className="candidate-dismiss" onClick={() => dismiss(c)} title="Neka förslaget. Det flyttas till Nekade förslag.">Neka</button></div>) : <p className="candidate-empty">Inga nya förslag.</p>}
        {dismissed.length > 0 && <div className="dismissed"><button className="dismissed-toggle" aria-expanded={showDismissed} onClick={() => setShowDismissed(!showDismissed)}><ChevronRight size={14} className={showDismissed ? 'open' : ''}/> Nekade förslag <span>{dismissed.length}</span></button>
          {showDismissed && dismissed.map(c => <div className="candidate dismissed-item" key={c.id}><div><strong>{portLabel(c.localInterfaceId)}</strong><small>→ {c.remoteName}{c.remotePort ? ` / ${c.remotePort}` : ''} · nekad {new Date(c.dismissedAt * 1000).toLocaleDateString('sv-SE')}</small></div><button onClick={() => restore(c)} title="Visa förslaget igen">Återställ</button></div>)}</div>}</div>
    </aside><main className="map-area">
      <div className="map-toolbar">
        <div className="map-key" aria-label="Förklaring"><span className="key-scale" title="Länkens beläggning">{loadBands.map(b => <i key={b.max} style={{ background: b.color }} title={b.label}/>)}</span><span className="key-scale-labels"><span>0</span><span>100 %</span></span><span className="key-down"><i/> Nere</span></div>
        {topology && topology.devices.length > 0 && <button className="secondary" onClick={arrange} title="Ordna enheterna som ett träd utifrån länkarna"><Network size={15}/> Sortera karta</button>}
      </div>
      {error && <div className="toast-error" role="alert"><AlertTriangle size={16}/>{error}<button onClick={() => setError('')} aria-label="Stäng fel"><X size={15}/></button></div>}
      {topology && topology.devices.length ? <ReactFlow key={topology.devices.map(d => d.id).join(',')} colorMode="dark" nodes={nodes} edges={edges} nodeTypes={nodeTypes} edgeTypes={edgeTypes} onNodesChange={onNodesChange} onInit={instance => { flow.current = instance; requestAnimationFrame(() => instance.fitView({ padding: 0.2 })) }} onNodeDragStop={(_, __, dragged) => savePositions(dragged.map(n => ({ id: Number(n.id), ...n.position })))} onNodeClick={(_, node) => setSelection({kind:'device',id:Number(node.id)})} onEdgeClick={(_, edge) => setSelection({kind:'link',id:Number(edge.id)})} onPaneClick={() => setSelection(null)} fitView fitViewOptions={{ padding: 0.2 }} minZoom={0.08} maxZoom={1.8} nodesConnectable={false} edgesReconnectable={false} proOptions={{ hideAttribution: true }}><Background color="oklch(30% .01 165)" gap={28} size={1}/><Controls showInteractive={false}/></ReactFlow>
        : topology && <div className="map-empty"><TopologyMark size={30}/><h3>Kartan är tom</h3><p>Lägg till routrar och switchar med IP-adress eller DNS-namn. Portar och länkar hämtas när SNMP svarar.</p><button className="primary" onClick={() => setPanel('device')}><Plus size={16}/> Lägg till första enheten</button></div>}
    </main><DetailPanel topology={topology || {devices:[],interfaces:[],links:[],candidates:[],dismissedCandidates:[],alarms:[],serverTime:0}} selection={selection} onClose={() => setSelection(null)} onDelete={() => { setSelection(null); refresh() }} onSaved={refresh} onError={setError}/></div>
  </div>
}
