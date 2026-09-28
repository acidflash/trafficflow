export type Device = { id: number; name: string; address: string; resolved: string; os: 'routeros' | 'swos' | 'external'; snmpVersion: string; status: string; lastSeen: number | null; lastError: string; x: number | null; y: number | null }
export type Interface = { id: number; deviceId: number; ifIndex: number; name: string; description: string; speedBps: number; status: string; rxBps: number | null; txBps: number | null; lastSample: number | null; alert: boolean }
export type Link = { id: number; aInterfaceId: number; bInterfaceId: number; source: string }
export type Candidate = { id: number; localInterfaceId: number; remoteDeviceId: number | null; remotePort: string; remoteName: string }
export type DismissedCandidate = { id: number; localInterfaceId: number; remotePort: string; remoteName: string; dismissedAt: number }
export type Topology = { devices: Device[]; interfaces: Interface[]; links: Link[]; candidates: Candidate[]; dismissedCandidates: DismissedCandidate[]; alarms: Alarm[]; serverTime: number }
export type Alarm = { id: number; kind: 'device' | 'port'; deviceId: number | null; interfaceId: number | null; title: string; startedAt: number; clearedAt: number | null }
export type AlertSettings = { discordEnabled: boolean; emailEnabled: boolean; smtpHost: string; smtpPort: number; smtpSecurity: 'starttls' | 'tls' | 'none'; smtpUser: string; from: string; to: string[]; hasDiscordWebhook: boolean; hasSmtpPassword: boolean; lastError: string; lastErrorAt: number }
export type Point = { ts: number; rxBps: number; txBps: number }

export async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`/api${path}`, { credentials: 'same-origin', ...options, headers: { 'Content-Type': 'application/json', ...options?.headers } })
  if (!response.ok) {
    let message = `HTTP ${response.status}`
    try { message = (await response.json()).error || message } catch { /* preserve HTTP error */ }
    throw new Error(message)
  }
  return response.json() as Promise<T>
}
