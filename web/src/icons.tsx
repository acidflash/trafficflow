import { Cloud, Server } from 'lucide-react'
import type { Device } from './api'

// Equipment symbols in classic network-diagram notation, drawn to match lucide's 24px grid and stroke.
type IconProps = { size?: number; strokeWidth?: number }
const svg = (size: number, strokeWidth: number) => ({ width: size, height: size, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const, 'aria-hidden': true })

// Router: a round body with two arrows out (up/down) and two arrows in (left/right).
export function RouterIcon({ size = 18, strokeWidth = 1.7 }: IconProps) {
  return <svg {...svg(size, strokeWidth)}><circle cx="12" cy="12" r="10"/><path d="M12 10V4.5M9.8 6.7 12 4.5l2.2 2.2M12 14v5.5M9.8 17.3 12 19.5l2.2-2.2M4.5 12H10M7.8 9.8 10 12l-2.2 2.2M19.5 12H14M16.2 9.8 14 12l2.2 2.2"/></svg>
}

// Switch: a flat box with two opposing arrows, for traffic switched both ways between ports.
export function SwitchIcon({ size = 18, strokeWidth = 1.7 }: IconProps) {
  return <svg {...svg(size, strokeWidth)}><rect x="2" y="5" width="20" height="14" rx="2.5"/><path d="M6 9.5h12M15.5 7.5l2.5 2-2.5 2M18 14.5H6M8.5 12.5 6 14.5l2.5 2"/></svg>
}

// Brand mark: three nodes joined by links.
export function TopologyMark({ size = 18, strokeWidth = 1.8 }: IconProps) {
  return <svg {...svg(size, strokeWidth)}><circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="18.5" r="2.5"/><circle cx="19" cy="18.5" r="2.5"/><path d="m10.8 7.2-4.6 9M13.2 7.2l4.6 9M7.5 18.5h9"/></svg>
}

type Kind = Pick<Device, 'os' | 'kind'>

export function DeviceIcon({ device, size = 18 }: { device: Kind; size?: number }) {
  if (device.os === 'external') return device.kind === 'server' ? <Server size={size} strokeWidth={1.7} aria-hidden /> : <Cloud size={size} strokeWidth={1.7} aria-hidden />
  return device.os === 'routeros' ? <RouterIcon size={size}/> : <SwitchIcon size={size}/>
}

export function deviceKind(device: Kind) {
  return device.os === 'external' ? device.kind === 'server' ? 'Server' : 'Moln' : device.os === 'routeros' ? 'Router' : 'Switch'
}
