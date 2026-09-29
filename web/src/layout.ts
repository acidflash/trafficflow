import { Graph, layout } from '@dagrejs/dagre'
import type { Device, Interface, Link } from './api'

const NODE_WIDTH = 176, NODE_HEIGHT = 100, GAP_X = 26, GAP_Y = 140, LOOSE_PER_ROW = 6

export type Positions = Map<number, { x: number; y: number }>

// arrangeMap lays out the network as a top-down tree. Each connected group starts at its root:
// the RouterOS device with most links, otherwise the device with most links. Only the tree edges
// shape the layout, so redundant links do not tangle it. Clouds (external networks) are kept out
// of the tree and placed one level above the devices they connect to; servers are ordinary tree
// nodes and so end up below their switch. Devices without links go in rows below.
export function arrangeMap(devices: Device[], interfaces: Interface[], links: Link[]): Positions {
  const byId = new Map(devices.map(d => [d.id, d]))
  const deviceOf = new Map(interfaces.map(i => [i.id, i.deviceId]))
  const isCloud = (id: number) => byId.get(id)!.os === 'external' && byId.get(id)!.kind !== 'server'
  const neighbours = new Map(devices.map(d => [d.id, new Set<number>()]))
  const cloudLinks = new Map(devices.map(d => [d.id, new Set<number>()]))
  for (const link of links) {
    const a = deviceOf.get(link.aInterfaceId), b = deviceOf.get(link.bInterfaceId)
    if (a == null || b == null || a === b || !byId.has(a) || !byId.has(b)) continue
    if (isCloud(a) || isCloud(b)) {
      const [cloud, device] = isCloud(a) ? [a, b] : [b, a]
      if (!isCloud(device)) cloudLinks.get(cloud)!.add(device)
      continue
    }
    neighbours.get(a)!.add(b)
    neighbours.get(b)!.add(a)
  }
  const byName = (a: number, b: number) => byId.get(a)!.name.localeCompare(byId.get(b)!.name, 'sv', { numeric: true })
  const weight = (id: number) => (byId.get(id)!.os === 'routeros' ? 1000 : 0) + neighbours.get(id)!.size
  const clouds = devices.map(d => d.id).filter(id => isCloud(id) && cloudLinks.get(id)!.size > 0).sort(byName)
  const attached = new Set(clouds.flatMap(id => [...cloudLinks.get(id)!]))
  const connected = devices.map(d => d.id).filter(id => !isCloud(id) && (neighbours.get(id)!.size > 0 || attached.has(id))).sort((a, b) => weight(b) - weight(a) || byName(a, b))
  const loose = devices.map(d => d.id).filter(id => !connected.includes(id) && !clouds.includes(id)).sort(byName)

  const graph = new Graph()
  graph.setGraph({ rankdir: 'TB', nodesep: GAP_X, ranksep: GAP_Y })
  graph.setDefaultEdgeLabel(() => ({}))
  for (const id of connected) graph.setNode(String(id), { width: NODE_WIDTH, height: NODE_HEIGHT })
  const visited = new Set<number>()
  for (const root of connected) {
    if (visited.has(root)) continue
    visited.add(root)
    const queue = [root]
    while (queue.length) {
      const id = queue.shift()!
      // dagre places siblings in reverse insertion order, so add them reversed to get A–Z left to right.
      for (const next of [...neighbours.get(id)!].filter(n => !visited.has(n)).sort(byName).reverse()) {
        visited.add(next)
        graph.setEdge(String(id), String(next))
        queue.push(next)
      }
    }
  }

  // An edge from each cloud to its devices ranks the cloud one level above them.
  for (const cloud of [...clouds].reverse()) {
    graph.setNode(String(cloud), { width: NODE_WIDTH, height: NODE_HEIGHT })
    for (const device of cloudLinks.get(cloud)!) graph.setEdge(String(cloud), String(device))
  }

  const positions: Positions = new Map()
  let bottom = 0
  if (connected.length) {
    layout(graph)
    for (const id of [...connected, ...clouds]) {
      const node = graph.node(String(id))
      positions.set(id, { x: node.x - NODE_WIDTH / 2, y: node.y - NODE_HEIGHT / 2 })
      bottom = Math.max(bottom, node.y + NODE_HEIGHT / 2 + GAP_Y)
    }
  }
  loose.forEach((id, index) => {
    positions.set(id, { x: (index % LOOSE_PER_ROW) * (NODE_WIDTH + GAP_X), y: bottom + Math.floor(index / LOOSE_PER_ROW) * (NODE_HEIGHT + GAP_X) })
  })
  return positions
}
