# Hardware status

## Current build

Use [`e32r28t/`](e32r28t/) and [`wiring/`](wiring/). The selected Keyes
E32R28T is an integrated display/controller/charger board, so the current build
requires no custom PCB and no separate Wi-Fi module.

## Superseded files

The following directories are retained only as history from the abandoned
DFR0975 + DFR0665 carrier design:

- `pcb/`
- `rendered/pcb/`
- `enclosure/quota_display.scad` and its rendered outputs
- `scripts/` that generate or verify the old carrier

**Do not order, fabricate, assemble or print those files for E32R28T.** Their
production status is explicitly marked `SUPERSEDED — DO NOT ORDER OR FABRICATE`.
The current enclosure must be regenerated after measuring the purchased board,
battery, connector projection and required LiPo swelling clearance.
