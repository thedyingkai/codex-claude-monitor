# Carrier electrical design status

The carrier is a deterministic KiCad 9 design. `carrier.kicad_sch` is a native,
symbol-based schematic, not a drawing. The manufacturing script runs official
KiCad ERC, exports an XML netlist, compares all numbered schematic endpoints
with the PCB, runs PCB invariants, then runs official DRC. The current digital
result is zero ERC violations, zero DRC violations and zero unconnected pads.

This is still **prototype-only**. Digital checks do not resolve the two physical
blockers in `PRODUCTION_STATUS.md`: protected-pack deep-discharge recharge
recovery and the present display/enclosure interference.

## Battery and protection path

- J1.1 `BAT_RAW` passes through Bourns `MF-NSMF150-2` F1 to `BAT_FUSED`.
- Q1 is Vishay `Si5908BDC-T1-GE3`. Official pins are 1=S1, 2=G1, 3=S2,
  4=G2, 5/6=D2 and 7/8=D1. Pins 1+3 form `FET_COMMON`; pins 2+4 form
  `PROT_GATE`; 5+6 are `VBAT_SAFE`; 7+8 are `BAT_FUSED`.
- U3 is `LTC4365ITS8#TRMPBF`: GATE=`PROT_GATE`, VOUT=`VBAT_SAFE`,
  VIN=`BAT_FUSED`, OV/GND=GND. R5 holds SHDN high. R8 holds UV high, so the
  external UV threshold is disabled while the controller's built-in VIN UVLO
  remains. This R0.1 uses U3 only for reverse-supply gate control; it does not
  implement external battery UV/OV protection or 2S rejection.
- `VBAT_SAFE` feeds J2.1, C1, C2, C7, TP1, MAX17048 CELL pin 2 and VDD pin 3.
- The topology has not yet demonstrated recharge recovery when a protected
  pack opens and J1/VIN collapses near 0V while USB raises J2/VOUT. Do not call
  this production-safe until that case passes the documented bench test.

## Display and gauge

- J3/J4 pins 2..18 pass through one-for-one. Pin 1 alone is switched by U2.
- U2 is `AP22804AW5-7`; R6 is a 100k EN pull-down. C3 and C4 are input/output
  decoupling. Display inrush and C4/C7 transients require measurement.
- MAX17048 uses the ADI T822+3 / 90-0065 land pattern. CELL pin 2 and VDD pin 3
  both use `VBAT_SAFE`; CTG, GND, QSTRT and exposed pad are grounded.

## Mechanical coordinates

PCB origin is the upper-left drawing origin; board size is 84 x 58 x 1.0mm.

| Item | PCB center/origin (mm) | Note |
|---|---:|---|
| J3 | 34.25, 4.50 | FH12 input FPC contact row |
| J4 | 34.25, 50.50 | FH12 display FPC contact row |
| SW1 | 77.00, 34.00 | PCM12 rotated 270°; actuator faces +X wall and travels along Y |
| SW2 | 79.00, 42.00 | PTS850 actuator toward +X |
| SW3 | 79.00, 51.00 | PTS850 actuator toward +X |
| H4 | 70.00, 55.00 | moved away from SW3 |
| J1 | 5.00, 18.00 | moved clear of the left display-boss centre; body is still too tall for the current stack |
| J2 | 12.00, 41.00 | moved clear of the lower-left display-boss centre; body height still needs a physical fit check |
| J5 | 74.00, 7.00 | moved clear of the right display-boss centre; vertical header is still too tall and is DNP for an enclosed build |

The PTS850 rows above use the official **with-boss** recommended land pattern,
with footprint rotation fixed at 0 degrees.  Relative to each footprint origin,
pads 1/2 are at `(-1.70,-2.90)/(+1.70,-2.90)` mm and pads 3/4 are at
`(-1.70,+2.90)/(+1.70,+2.90)` mm.  Pads 1 and 3 are the normally-open button
signal; pads 2 and 4 are GND.  Each copper land is 1.40 x 1.50 mm.  The two
0.90 mm NPTH locating bosses are at `(-1.50,0)/(+1.50,0)` mm.  The body is
5.4 x 5.0 mm and its free actuator-face reference is local `X=+4.35 mm`, toward
the board's +X edge.  Signal and GND pair routing detours around both bosses;
do not replace it with a straight trace through the locating holes.

SW1 is fixed at KiCad rotation 270 degrees (`-90` when read back through the
KiCad API).  Its numbered pad centers are 1=`(78.43,31.75)` mm GND,
2=`(78.43,34.75)` mm POWER_SW and 3=`(78.43,36.25)` mm NC.  This orientation
places the PCM12 actuator toward +X and changes slide travel to the wall's Y
direction.  The wall slot/cap geometry is still a mandatory physical fit gate.

With the enclosure's current carrier offset `(4.5, 3.5)`, add that offset to
the PCB coordinates. SW1/SW2/SW3 centers become `(81.5,37.5)`,
`(83.5,45.5)` and `(83.5,54.5)` in enclosure coordinates.

H1--H4 are PCB tooling/mounting holes, but the current enclosure does not use
fasteners through them. Carrier retention relies on the printed support rails
and lid anti-lift fingers with only 0.15mm nominal clearance. This must be
measured on a physical print; a clean CAD assertion is not retention testing.

## Primary footprint sources

- [LTC4365](https://www.analog.com/media/en/technical-documentation/data-sheets/LTC4365.pdf)
- [Si5908BDC](https://www.vishay.com/docs/61683/si5908bdc.pdf) and [ChipFET land pattern](https://www.vishay.com/docs/72286/an826.pdf)
- [MAX17048](https://www.analog.com/media/en/technical-documentation/data-sheets/max17048-max17049.pdf)
- [AP22804](https://www.diodes.com/datasheet/download/AP22804.pdf)
- [JST PH](https://www.jst-mfg.com/product/pdf/eng/ePH.pdf)
- [Bourns MF-NSMF](https://www.bourns.com/docs/product-datasheets/mf-nsmf.pdf)
- [C&K PTS850](https://www.littelfuse.com/assetdocs/littelfuse-ck-tactile-pts850-series-datasheet?assetguid=fa02d3ac-6de0-4ead-8de3-fd258067c98f)
- [Keystone 5015](https://www.keyelco.com/pdfs/M55p51.pdf)
- [Murata GRM21 reflow land guidance](https://search.murata.co.jp/Ceramy/image/img/A01X/G101/ENG/GRM21BC81H475KE11-01A.pdf)
