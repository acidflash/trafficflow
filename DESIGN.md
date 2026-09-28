---
name: Trafficflow
description: Live topology and link load for a fibre access network, built for the night shift.
colors:
  graphite-floor: "oklch(16.5% .008 165)"
  panel-graphite: "oklch(19.5% .009 165)"
  raised-graphite: "oklch(23% .01 165)"
  hover-graphite: "oklch(27% .011 165)"
  hairline: "oklch(29% .011 165)"
  strong-hairline: "oklch(38% .013 165)"
  signal-white: "oklch(93% .008 165)"
  secondary-text: "oklch(76% .011 165)"
  quiet-text: "oklch(62% .012 165)"
  selection-frost: "oklch(92% .03 200)"
  focus-ice: "oklch(82% .09 210)"
  alive-green: "oklch(74% .15 150)"
  fault-red: "oklch(67% .2 27)"
  fault-text: "oklch(78% .13 27)"
  fault-surface: "oklch(26% .05 27)"
  fault-line: "oklch(42% .11 27)"
  stale-amber: "oklch(84% .14 88)"
  load-idle: "oklch(56% .04 175)"
  load-light: "oklch(74% .12 170)"
  load-half: "oklch(84% .15 100)"
  load-heavy: "oklch(75% .17 58)"
  load-full: "oklch(66% .21 27)"
  chart-in-sky: "oklch(80% .09 225)"
  chart-out-sand: "oklch(82% .1 78)"
typography:
  data:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "20px"
    fontWeight: 650
    lineHeight: 1.1
    letterSpacing: "-0.02em"
    fontFeature: "tnum"
  title:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "16px"
    fontWeight: 650
    lineHeight: 1.3
    letterSpacing: "-0.01em"
  body:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "12.5px"
    fontWeight: 400
    lineHeight: 1.5
    fontFeature: "tnum"
  label:
    fontFamily: "Inter, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "11px"
    fontWeight: 600
    lineHeight: 1.3
  mono:
    fontFamily: "ui-monospace, SF Mono, Cascadia Mono, JetBrains Mono, Menlo, Consolas, monospace"
    fontSize: "11.5px"
    fontWeight: 400
    letterSpacing: "-0.01em"
rounded:
  xs: "5px"
  sm: "6px"
  md: "7px"
  lg: "8px"
  cloud: "18px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "20px"
components:
  button-primary:
    backgroundColor: "{colors.signal-white}"
    textColor: "{colors.graphite-floor}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "7px 12px"
    height: "34px"
  button-primary-hover:
    backgroundColor: "oklch(84% .01 165)"
  button-secondary:
    backgroundColor: "{colors.raised-graphite}"
    textColor: "{colors.signal-white}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "7px 12px"
    height: "34px"
  button-secondary-hover:
    backgroundColor: "{colors.hover-graphite}"
  input-field:
    backgroundColor: "{colors.graphite-floor}"
    textColor: "{colors.signal-white}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "0 9px"
    height: "34px"
  map-node:
    backgroundColor: "{colors.panel-graphite}"
    textColor: "{colors.signal-white}"
    rounded: "{rounded.lg}"
    padding: "9px 11px 10px"
    width: "176px"
  map-node-router:
    backgroundColor: "{colors.raised-graphite}"
  map-node-offline:
    backgroundColor: "{colors.fault-surface}"
    textColor: "{colors.fault-text}"
  map-node-cloud:
    backgroundColor: "transparent"
    rounded: "{rounded.cloud}"
  edge-chip:
    backgroundColor: "{colors.panel-graphite}"
    textColor: "{colors.signal-white}"
    rounded: "{rounded.sm}"
    padding: "4px 7px 5px"
  edge-chip-down:
    backgroundColor: "{colors.fault-surface}"
    textColor: "{colors.fault-text}"
---

# Design System: Trafficflow

## 1. Overview

**Creative North Star: "Nattskiftet" (the night shift)**

An operations room late in the evening. The lights are down, the wall screen glows softly, and nothing asks for attention until something breaks. Trafficflow is built for that room: a graphite surface with a faint green cast, neutral text, thin hairlines, and a map that stays quiet while the network is healthy. Colour is an alarm system, not decoration. When a link fills up it warms from muted sea-grey through yellow to red; when a device stops answering it turns red and moves to the top of every list.

The system is dense and matter-of-fact. It is a tool for people who read MAC tables and port names, so it shows names, addresses and rates first, in small precise type, and puts nothing between them and the topology. Panels support the map and never crowd it. Every element on the map encodes network state; nothing is there for flavour.

It rejects anything that makes a healthy network look alarming, anything that hides the topology behind panels, and the familiar dark-dashboard reflex of navy backgrounds with neon accents.

**Key Characteristics:**
- Dark graphite (hue 165, chroma ≤ .013) for every neutral; no pure black, no pure white.
- Colour only for load, faults, liveness and selection.
- Flat tonal layers instead of shadows.
- Small, dense, tabular type; mono for addresses and error text.
- Equipment drawn in network-diagram notation: router, switch, cloud.

## 2. Colors

A near-neutral graphite ramp carries the interface; a five-step load scale and a single fault red carry all meaning.

### Primary
- **Load scale** (load-idle → load-light → load-half → load-heavy → load-full): the colour of a link and its meter, by the busier direction as a share of the slower port's speed, in bands of 20 %. The idle band is deliberately desaturated so a healthy network reads as calm; the scale only becomes vivid from 41 % upward.

### Secondary
- **Fault Red** (fault-red, with fault-text, fault-surface, fault-line): devices that do not answer, links that are down (dashed stroke), error notices and destructive actions. The only warm colour besides the top of the load scale, so a fault is never mistaken for decoration.
- **Alive Green** (alive-green): status dots for devices that answer and ports that are up. Dots only, never fills or text.

### Tertiary
- **Selection Frost** (selection-frost): the stroke of the selected link and the border of the selected node or chip. Near-white with a cold tint so it reads as "picked" rather than as load.
- **Stale Amber** (stale-amber): the top bar's live indicator when the server has not answered for 45 seconds.
- **Chart Sky / Chart Sand** (chart-in-sky, chart-out-sand): inbound and outbound series in traffic history. Kept off the load scale's hues so history never reads as a load warning.

### Neutral
- **Graphite Floor** (graphite-floor): the map and page background, and input fields sunk into panels.
- **Panel Graphite** (panel-graphite): top bar, sidebar, detail panel, map nodes, edge chips.
- **Raised Graphite** (raised-graphite): router nodes, forms, icon tiles, secondary buttons.
- **Hover Graphite** (hover-graphite): hover and active list rows.
- **Hairline / Strong Hairline** (hairline, strong-hairline): 1px dividers, borders, node outlines, the empty track of a meter.
- **Signal White / Secondary / Quiet Text** (signal-white, secondary-text, quiet-text): primary text and the primary button fill; supporting text; labels, metadata and axis ticks.

### Named Rules
**The Alarm-Only Rule.** Colour is spent on load, faults, liveness and selection. If a colour appears and nothing is wrong, busy, alive or selected, remove it.

**The Calm Floor Rule.** Links at 0–20 % use load-idle. A healthy network must be readable as "nothing to see" from across the room.

**The One Red Rule.** Fault red means down or failing. Never use it for emphasis, branding or a "high" state that is not a fault; load-full shares its hue only because a full link is a fault in waiting.

## 3. Typography

**Body Font:** Inter (with system-ui, -apple-system, Segoe UI)
**Label/Mono Font:** ui-monospace stack (SF Mono, Cascadia Mono, JetBrains Mono, Menlo)

**Character:** One quiet sans for everything, set small and with tabular figures so rates line up and do not jitter every 15 seconds. Mono marks machine strings: IP addresses, DNS names, SNMP errors.

### Hierarchy
- **Data** (650, 20px, 1.1): the few numbers worth reading from a distance: the health strip and the in/out rates in the detail panel (19px there).
- **Title** (650, 16px, 1.3): the name of the selected device or link in the detail panel.
- **Body** (400–650, 12.5px, 1.5): list rows, node names (12.5px/650), form fields, port names.
- **Label** (600, 11px): field labels, section counts, metadata, legend text. Sentence case, never tracked-out uppercase.
- **Mono** (400, ~11.5px): addresses and error text only.

### Named Rules
**The Tabular Rule.** Every number is set with tabular figures. A rate that shifts width on each refresh is a bug.

**The No Eyebrow Rule.** No uppercase, letter-spaced kicker labels above headings. The kind of thing (Router, Switch, Moln, Länk #4) is a plain 11px label.

## 4. Elevation

Flat by default. Depth comes from three tonal steps (floor, panel, raised) and 1px hairlines, not shadows. The only shadow in the system is the overlay shadow when the detail panel slides over the map on narrow screens, because there it genuinely sits above live content.

### Shadow Vocabulary
- **Overlay** (`box-shadow: -12px 0 32px oklch(8% .01 165 / .5)`): the detail panel as an overlay at ≤ 900px; upward (`0 -12px 40px … / .6`) as a bottom sheet at ≤ 620px.

### Named Rules
**The Tonal Step Rule.** To lift something, move it one step up the graphite ramp. If you reach for a shadow on a resting element, the ramp step is wrong.

## 5. Components

Matter-of-fact and dense: small radii, thin lines, no ornament. Tools, not decoration.

### Buttons
- **Shape:** gently squared (7px), 34px tall.
- **Primary:** signal-white fill with graphite-floor text: the one inverted element in a panel, used for the main action (add device, save, log in).
- **Secondary:** raised-graphite with a hairline border; hover moves one tonal step up and strengthens the border.
- **Icon button:** 30px, transparent, quiet-text; hover fills hover-graphite and brightens the icon.
- **Destructive:** text-only in fault-text with an underline on hover. Never a filled red button.
- **Focus:** 2px focus-ice outline, 2px offset, on every interactive element.

### Chips
- **Edge chip:** panel-graphite at 95 % with a hairline border, 6px radius. Shows ↓ and ↑ rates (10.5px/600) and a 4px meter with the load percentage. Sits at the lower end of its link, or the upper end when several links end in the same device. When zoomed out it scales up to 2× so rates stay readable. A down link shows only "Nere" on fault-surface.

### Cards / Containers
- **Map node:** 176px wide, 8px radius, panel-graphite with a strong-hairline border. Icon tile with a status dot on its corner, name, kind and address, total inbound rate. Routers sit one tonal step higher; clouds are transparent with a dashed border and an 18px radius; offline nodes take fault-surface and fault-line and show "Svarar inte".
- **Inline form:** raised-graphite, hairline border, 9px radius, 12px padding. Forms open inline in the sidebar or detail panel; there are no modals.
- **Candidate row:** raised-graphite, 7px radius; rejected ones turn transparent with a dashed border.

### Inputs / Fields
- **Style:** graphite-floor, sunk into the panel, hairline border, 6px radius, 34px tall.
- **Hover / Focus:** border strengthens on hover; focus-ice on focus.
- **Error:** fault-surface block with fault-line border and fault-text, directly under the fields.

### Navigation
- **Top bar:** 48px, panel-graphite, brand mark (three linked nodes) and name on the left; live indicator (pulsing alive-green dot and last update time, stale-amber warning when contact is lost), refresh and log out on the right.
- **Sidebar:** health strip (devices answering, links up, busiest link as a button), three action buttons, search, device list with offline devices first, link suggestions and rejected suggestions at the bottom.
- **Mobile (≤ 620px):** single column; the detail panel becomes a bottom sheet with a 14px top radius.

### Load meter (signature)
A 4px track (6px in the detail panel) in hairline with a fill in the load colour, minimum 3 % wide so an idle link still shows a sliver. It replaces coloured side stripes and is the only way load is shown besides the link stroke.

### Equipment icons (signature)
Router: round body with two arrows out and two in. Switch: flat box with two opposing arrows. Cloud: lucide Cloud. Drawn on lucide's 24px grid with a 1.7 stroke so they sit beside lucide UI icons.

## 6. Do's and Don'ts

### Do:
- **Do** keep every neutral on hue 165 with chroma ≤ .013, and every colour in OKLCH.
- **Do** colour a link only through the load scale, fault red (down, dashed 5 5) or selection frost.
- **Do** put the most important state first: offline devices at the top of lists, the busiest link in the health strip.
- **Do** use tabular figures for every rate, count and time.
- **Do** draw routers, switches and clouds with the equipment icons, never generic app icons.
- **Do** open forms inline and keep the map visible while working.

### Don't:
- **Don't** make a healthy network look alarming: no vivid green for idle links, no red for anything that is not a fault.
- **Don't** hide the topology behind panels, overlays or modals on desktop widths.
- **Don't** use navy backgrounds with neon accents; that is the dark-dashboard reflex this system avoids.
- **Don't** use border-left or border-right wider than 1px as a coloured accent on chips, cards, list items or alerts. Use the load meter or a full border.
- **Don't** use gradient text, glassmorphism or decorative shadows on resting elements.
- **Don't** use uppercase letter-spaced eyebrow labels.
- **Don't** use #000 or #fff; the extremes are graphite-floor and signal-white.
