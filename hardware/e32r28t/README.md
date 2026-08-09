# Current hardware: Keyes E32R28T

This directory is the current hardware source of truth. The selected module
already contains the ESP32-32E, ILI9341V display, XPT2046 touch controller,
CH340C USB interface, Wi-Fi antenna and 1S charger. No custom PCB is required.

- `system_bom.csv`: complete current purchase/assembly list.
- `../wiring/HARNESS_SPEC.md`: battery connector and polarity acceptance.
- `../wiring/wiring_diagram.svg`: current USB/battery topology.

The files under `hardware/pcb` and `hardware/rendered/pcb` belong to the
superseded DFRobot carrier and must not be fabricated for this build.
