# SUPERSEDED — DO NOT ORDER OR FABRICATE

This DFR0975/DFR0665 carrier was replaced by the integrated Keyes E32R28T
hardware selected for the current build. Nothing in this directory is needed
for that device. The Gerbers, drill files, BOM and enclosure references are
retained only as engineering history and must not be sent to a PCB factory.

The historical R0.1 blockers follow for traceability.

The generated ERC, schematic/PCB endpoint comparison and PCB DRC are clean.
That is necessary but not sufficient for production. Do not order a production
run, assemble with an unrestricted lithium cell, or claim enclosure fit until
all blockers below are closed with recorded measurements.

1. **Deep-discharge recharge recovery:** LTC4365 is powered from J1/BAT_FUSED.
   When a protected pack opens and VIN is near 0V, it is below the controller's
   UVLO/guaranteed operating range. Back-to-back MOSFETs can then isolate USB
   charger voltage present at J2/VOUT, preventing pack wake-up. Test the exact
   DFR0975, protected 755060 pack and cable with a current-limited supply at
   0V/open-pack, USB attach, detach and repeated hot-plug. If recovery is not
   deterministic, redesign the charge/protection partition before release.
2. **UV/OV protection is not implemented:** R0.1 ties OV low and pulls UV high,
   intentionally disabling the LTC4365 external thresholds. U3 is used only
   for reverse-supply gate control. Do not advertise battery under/overvoltage
   or 2S misconnection rejection. Before release, either add a formally
   tolerance-analysed threshold network and repeat fault tests, or replace the
   architecture with a qualified protection/charging solution.
3. **Enclosure/display interference:** the current screen-bottom clearance is
   about 3.1mm. J1/J2 bodies are about 4.5mm high and lie under the display
   projection. Their centres have been moved to `(5,18)` and `(12,41)` to clear
   the mapped display-boss centres, but that does not solve the Z interference.
   J5 moved to `(74,7)` to clear the upper-right boss centre, yet the vertical
   TSW header is still much too tall and is DNP for the enclosed build. Rework
   the stack, connector side/placement, or an approved low-profile wire
   termination, then rerun a real 3D interference review using measured parts.
4. **Power validation:** record normal/reversed J1, USB-only, battery-only and
   simultaneous USB+battery currents; MOSFET/PTC temperature; charge direction;
   display inrush; C4/C7 transients; sleep/off current and brownout recovery.
   C7 is exact MPN `GRM21BR61A106KE19L` with Murata's nominal reflow land, but
   its 4.2V DC-bias effective capacitance still requires SimSurfing/bench proof.
5. **Mechanical and safety validation:** verify all polarities, finished holes,
   switch boss locations, FPC contact orientation, battery swelling clearance,
   charger temperature and strain relief on physical samples. H1--H4 are not
   fastened by the current enclosure; carrier retention depends on printed
   rails and lid anti-lift fingers with 0.15mm nominal clearance. Confirm that
   the board cannot lift, rattle or load components across print tolerances.
   SW1 is digitally rotated so its actuator faces +X and travels along Y, but
   the matching wall slot/cap travel must still be verified on a physical fit.

Closing ERC/DRC alone must never remove this file or change this status.
