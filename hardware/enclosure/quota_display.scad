// SUPERSEDED: legacy DFR0975/DFR0665 enclosure. DO NOT PRINT FOR E32R28T.
// Codex / Claude quota display enclosure
// Units: millimetres. OpenSCAD 2021.01+.
//
// The bulk envelopes below model the 755060 cell, DFR0975 controller, 84 x 58
// carrier and DFR0665 display.  The 2.4 mm carrier-top field deliberately
// excludes the currently unresolved J1/J2/J5 connector bodies; it must not be
// read as an exact-part interference pass.  Measure production parts, import
// their exact CAD and adjust the named parameters before ordering a batch.

$fn = 48;

// 0=assembly, 1=base, 2=lid, 3=button cap, 4=power-switch cap.
part_id = is_undef(part_id) ? 0 : part_id;

// 90 x 65 x 16 cannot contain the specified stack.  The official DFR0665 STEP
// spans 7.0 mm through Z (including rear-side parts), so the earlier 23.5 mm
// approximation was also too thin.  93 x 65 x 25.7 leaves 0.2 mm display
// thickness tolerance and 0.4 mm face clearance with the current carrier.
outer_w = 93;
outer_h = 65;
outer_d = 25.7;
corner_r = 4;
wall = 1.5;
base_h = 16.3;
lid_h = outer_d - base_h;
lid_top = 1.8;
fit = 0.25;

carrier_origin = [4.5, 3.5];
carrier_size = [84, 58, 1.0];
carrier_z = 12.2;
carrier_low_profile_h = 2.4;
carrier_rail_h = 0.8;

battery_origin = [3.0, 7.5];
battery_envelope = [60, 50, 7.5];
battery_z = 1.9;
battery_plan_clearance = 0.8;
battery_swell_z = 0.8;

// DFR0975 is rotated 90 degrees in plan so it can sit beside the battery.
controller_origin = [65.0, 2.5];
controller_envelope = [25.4, 60, 9.0];
controller_z = 1.9;

// DFRobot's dimension drawing gives an 80 x 50 mm board, 2.0 mm holes on
// 75 x 45 mm centres.  The official STEP is exactly 7.0 mm thick; use a
// 7.2 mm manufacturing envelope.  With the rear-most feature at Z=0, the top
// PCB face is nominally 3.5 mm above it, plus 0.1 mm fit allowance here.
screen_module = [80, 50, 7.2];
screen_active = [57.6, 43.2];
screen_window = [59.2, 44.8];
screen_center = [outer_w/2, outer_h/2];
screen_origin = [(outer_w-screen_module[0])/2,
                 (outer_h-screen_module[1])/2];
screen_z = base_h;
screen_pcb_top_from_bottom = 3.6;
screen_hole_d = 2.0;
screen_boss_d = 4.4;
screen_pilot_d = 1.25;
screen_pilot_depth = 3.15;
screen_holes = [
    [screen_origin[0]+2.5, screen_origin[1]+2.5],
    [screen_origin[0]+77.5, screen_origin[1]+2.5],
    [screen_origin[0]+2.5, screen_origin[1]+47.5],
    [screen_origin[0]+77.5, screen_origin[1]+47.5]
];

minimum_z_gap = 0.5;
// Final PCB switch centres are SW2=(79,42), SW3=(79,51), SW1=(77,34)
// in carrier coordinates.  The enclosure Y values must use those exact
// centres; the previous +43/+49/+31 approximations caused 1--3 mm errors.
button_y = [carrier_origin[1]+42, carrier_origin[1]+51];
button_z = 14.0;
power_switch_y = carrier_origin[1]+34;

// The PTS850 drawing dimensions the unpressed actuator face 4.35 mm from
// the footprint reference centre.  With the board at carrier_origin and the
// switches at X=79, the face is therefore X=87.85 in enclosure coordinates.
// Leave a small printable gap rather than preloading the 0.2 +/- 0.1 mm tact
// travel.  The previous 14.2 mm stem reached X=78.8 and would have crossed
// through the complete switch body.
pts850_reference_x = carrier_origin[0]+79;
pts850_face_offset = 4.35;
button_tip_clearance = 0.20;
button_tip_x = pts850_reference_x+pts850_face_offset+button_tip_clearance;
button_tip_offset = button_tip_x-outer_w;

// PCM12SMTR is a side-accessed slide switch, not a push switch.  After the
// PCB footprint is rotated -90 degrees its actuator points toward +X and
// travels 1.5 mm along Y.  The official projection gives body front +1.3 mm
// and actuator face +2.8 mm from the footprint reference.  A plan-view fork
// surrounds the 1.3 mm-wide stem while remaining open through Z so an
// unverified actuator-height assumption is not baked into the print.
pcm12_reference_x = carrier_origin[0]+77;
pcm12_body_front_x = pcm12_reference_x+1.3;
pcm12_stem_face_x = pcm12_reference_x+2.8;
switch_fork_body_clearance = 0.4;
switch_fork_end_clearance = 0.2;
switch_fork_nose_offset = pcm12_body_front_x+
                          switch_fork_body_clearance-outer_w;
switch_fork_slot_end_offset = pcm12_stem_face_x+
                              switch_fork_end_clearance-outer_w;
switch_fork_slot_depth = switch_fork_slot_end_offset-
                         switch_fork_nose_offset;
switch_fork_outer_y = 3.3;
switch_fork_slot_y = 1.7;
switch_face_y = 4.6;
switch_flange_y = 4.4;
power_slot_y = 5.3;
pcm12_travel = 1.5;
// 0.20 mm plan clearance per side in the 1.7 mm fork slot consumes part of
// the cap motion on reversal.  Allow that take-up plus 0.05 mm after the
// switch reaches either stable position before the enclosure stop engages.
pcm12_overtravel = 0.25;
button_flange_y = 8.4;

// Printed stops transfer excessive user force into the enclosure instead of
// the small switches.  They are connected to the right carrier rail and must
// still be checked for print strength and dimensional shrink on the trial fit.
button_max_press = 0.48;
button_flange_inner_rest_x = outer_w-wall-0.7;
button_stop_face_x = button_flange_inner_rest_x-button_max_press;
button_stop_finger_x = 1.0;
button_stop_edge_y = 0.7;
switch_y_stop_x = outer_w-wall-1.1;
switch_y_stop_w = 0.85;
switch_y_stop_thickness = 0.4;
switch_lower_stop_face_y = power_switch_y-pcm12_travel/2-
                           switch_flange_y/2-pcm12_overtravel;
switch_upper_stop_face_y = power_switch_y+pcm12_travel/2+
                           switch_flange_y/2+pcm12_overtravel;

// Four lid fingers bridge the skirt-to-board side gap and limit carrier lift.
// They remain outside the 80 x 50 display outline and avoid the right-side
// switch actuator zones.  The board is still removable after lifting the lid.
carrier_press_gap = 0.15;
carrier_press_bottom = carrier_z + carrier_size[2] + carrier_press_gap - base_h;
carrier_press_top = 0.4;
carrier_press_w = 2.4;
carrier_press_h = 6.0;
carrier_press_left_y = [22, 55];
carrier_press_right_y = [19, 29];

// These assertions are also exercised during every command-line STL export.
assert(base_h + lid_h == outer_d, "base/lid depth mismatch");
assert(carrier_origin[0] >= wall && carrier_origin[1] >= wall,
       "carrier leaves the inner cavity");
assert(button_tip_offset < -wall,
       "button stem must reach beyond the inner wall");
assert(power_switch_y+power_slot_y/2 < button_y[0]-4.0,
       "power slot overlaps button A aperture");
assert(power_switch_y+pcm12_travel/2+switch_face_y/2 <
       button_y[0]-7.6/2,
       "power slider face collides with button A at end of travel");
assert(switch_upper_stop_face_y+switch_y_stop_thickness <
       button_y[0]-button_flange_y/2,
       "power stop collides with button A retention flange");
assert(carrier_origin[0] + carrier_size[0] <= outer_w-wall &&
       carrier_origin[1] + carrier_size[1] <= outer_h-wall,
       "carrier leaves the inner cavity");
assert(battery_origin[0]-battery_plan_clearance >= wall &&
       battery_origin[1]-battery_plan_clearance >= wall &&
       battery_origin[0]+battery_envelope[0]+battery_plan_clearance <= outer_w-wall &&
       battery_origin[1]+battery_envelope[1]+battery_plan_clearance <= outer_h-wall,
       "battery swelling envelope leaves the inner cavity");
assert(controller_origin[0] >= wall && controller_origin[1] >= wall &&
       controller_origin[0]+controller_envelope[0] <= outer_w-wall &&
       controller_origin[1]+controller_envelope[1] <= outer_h-wall,
       "controller leaves the inner cavity");
assert(battery_origin[0]+battery_envelope[0]+battery_plan_clearance+minimum_z_gap <=
       controller_origin[0], "battery and controller plan clearances overlap");
assert(battery_z+battery_envelope[2]+battery_swell_z+minimum_z_gap <= carrier_z,
       "battery swelling envelope intersects carrier");
assert(controller_z+controller_envelope[2]+minimum_z_gap <= carrier_z,
       "controller intersects carrier");
assert(controller_z+controller_envelope[2]+minimum_z_gap <=
       carrier_z-carrier_rail_h+0.01,
       "controller intersects carrier support rail");
assert(battery_z+battery_envelope[2]+battery_swell_z+minimum_z_gap <=
       carrier_z-carrier_rail_h+0.01,
       "battery swelling envelope intersects carrier support rail");
assert(carrier_z+carrier_size[2]+carrier_low_profile_h+minimum_z_gap <= screen_z,
       "carrier low-profile field intersects display");
assert(carrier_press_bottom > carrier_z+carrier_size[2]-base_h &&
       carrier_press_bottom < 0 && carrier_press_top > 0,
       "carrier retention fingers do not bridge the lid split");
assert(screen_z+screen_module[2] <= outer_d-lid_top+0.01,
       "display intersects lid top");
assert(screen_window[0] >= screen_active[0] &&
       screen_window[1] >= screen_active[1],
       "display aperture clips the nominal active area");
for (p = screen_holes)
    assert(p[0]-screen_boss_d/2 >= wall &&
           p[0]+screen_boss_d/2 <= outer_w-wall &&
           p[1]-screen_boss_d/2 >= wall &&
           p[1]+screen_boss_d/2 <= outer_h-wall,
           "display mounting boss leaves the enclosure");

module rounded_prism(size, radius) {
    hull() {
        for (x = [radius, size[0] - radius])
            for (y = [radius, size[1] - radius])
                translate([x, y, 0]) cylinder(r=radius, h=size[2]);
    }
}

module rounded_slot(size, radius=1) {
    hull() {
        for (x = [radius, size[0]-radius])
            for (y = [radius, size[1]-radius])
                translate([x, y, 0]) cylinder(r=radius, h=size[2]);
    }
}

module rounded_face_x(size_y, size_z, depth, radius=0.8) {
    hull() {
        for (y = [radius, size_y-radius])
            for (z = [radius, size_z-radius])
                translate([0, y, z]) rotate([0, 90, 0])
                    cylinder(r=radius, h=depth);
    }
}

module base() {
    difference() {
        rounded_prism([outer_w, outer_h, base_h], corner_r);
        translate([wall, wall, wall])
            rounded_prism([outer_w-2*wall, outer_h-2*wall,
                           base_h+1], corner_r-wall);

        // Rotated DFR0975 USB-C port, centred on the lower edge.
        translate([controller_origin[0]+7.2, -0.2, 3.0])
            cube([11.0, wall+0.4, 5.6]);

        // Side access for the carrier power switch.
        translate([outer_w-wall-0.2, power_switch_y-power_slot_y/2,
                   button_z-1.8])
            cube([wall+0.4, power_slot_y, 3.6]);

        // Side openings aligned with SW2/SW3 carrier coordinates.  The long
        // printed plungers run below the display module.
        for (y = button_y)
            translate([outer_w-wall-0.2, y-4.0, button_z-1.9])
                cube([wall+0.4, 8.0, 3.8]);

        // Bottom pressure-equalisation slots; not a waterproof design.
        for (x = [12:12:72])
            translate([x, 4.8, -0.1]) rounded_slot([6, 1.5, wall+0.2], 0.65);
    }

    // Battery fence follows the 0.8 mm planar swelling envelope, never the
    // nominal pouch outline.  The outer wall serves as the left stop.
    translate([battery_origin[0]+battery_envelope[0]+battery_plan_clearance,
               battery_origin[1]-battery_plan_clearance, wall-0.2])
        cube([0.8, battery_envelope[1]+2*battery_plan_clearance, 4.0]);
    translate([battery_origin[0]-battery_plan_clearance,
               battery_origin[1]-battery_plan_clearance-0.8, wall-0.2])
        cube([battery_envelope[0]+2*battery_plan_clearance+0.8, 0.8, 4.0]);
    translate([battery_origin[0]-battery_plan_clearance,
               battery_origin[1]+battery_envelope[1]+battery_plan_clearance,
               wall-0.2])
        cube([battery_envelope[0]+2*battery_plan_clearance+0.8, 0.8, 4.0]);

    // Carrier support rails.  The board rests at carrier_z and is retained by
    // the lid skirt; rails remain above both lower component envelopes.
    translate([wall-0.1, carrier_origin[1]+0.5,
               carrier_z-carrier_rail_h])
        cube([carrier_origin[0]-wall+0.9,
              carrier_size[1]-1.0, carrier_rail_h]);
    translate([carrier_origin[0]+carrier_size[0]-0.8,
               carrier_origin[1]+0.5, carrier_z-carrier_rail_h])
        cube([outer_w-wall-carrier_origin[0]-carrier_size[0]+0.9,
              carrier_size[1]-1.0, carrier_rail_h]);

}

module lid() {
    difference() {
        union() {
            translate([0, 0, lid_h-lid_top])
                rounded_prism([outer_w, outer_h, lid_top], corner_r);

            // Four blind M1.6 thread-forming bosses use the DFR0665's real
            // 2.0 mm holes.  Screws enter from the PCB rear; the bosses stop
            // on its component-side face and remain joined to the lid skin.
            for (p = screen_holes)
                translate([p[0], p[1], screen_pcb_top_from_bottom])
                    cylinder(d=screen_boss_d,
                             h=lid_h-lid_top-screen_pcb_top_from_bottom+0.2);

            // Carrier anti-lift fingers overlap the board edge by 0.9 mm and
            // join the locating skirt by 0.25 mm.  Their lower faces sit
            // 0.15 mm above the nominal 1.0 mm PCB top.
            for (y = carrier_press_left_y)
                translate([2*wall, y-carrier_press_h/2, carrier_press_bottom])
                    cube([carrier_press_w, carrier_press_h,
                          carrier_press_top-carrier_press_bottom]);
            for (y = carrier_press_right_y)
                translate([outer_w-2*wall-carrier_press_w,
                           y-carrier_press_h/2, carrier_press_bottom])
                    cube([carrier_press_w, carrier_press_h,
                          carrier_press_top-carrier_press_bottom]);

            // Install the caps into the base first, then lower the lid.  These
            // lid-mounted fingers enter vertically behind the cap flanges, so
            // unlike base-integral stops they do not block assembly.  They
            // merge into the right locating skirt at local Z=0..0.4.
            for (y = button_y) {
                translate([button_stop_face_x-button_stop_finger_x,
                           y-button_flange_y/2, carrier_press_bottom])
                    cube([button_stop_finger_x, button_stop_edge_y,
                          carrier_press_top-carrier_press_bottom]);
                translate([button_stop_face_x-button_stop_finger_x,
                           y+button_flange_y/2-button_stop_edge_y,
                           carrier_press_bottom])
                    cube([button_stop_finger_x, button_stop_edge_y,
                          carrier_press_top-carrier_press_bottom]);
            }

            // PCM12 Y end stops include fork-clearance take-up and permit
            // 0.05 mm cap overtravel after either nominal stable position.
            translate([switch_y_stop_x, switch_lower_stop_face_y-
                       switch_y_stop_thickness, carrier_press_bottom])
                cube([switch_y_stop_w, switch_y_stop_thickness,
                      carrier_press_top-carrier_press_bottom]);
            translate([switch_y_stop_x, switch_upper_stop_face_y,
                       carrier_press_bottom])
                cube([switch_y_stop_w, switch_y_stop_thickness,
                      carrier_press_top-carrier_press_bottom]);

            // Full-height locating skirt.  It stays outside the carrier and
            // display envelopes, overlaps the top skin, and therefore exports
            // as one printable solid instead of a detached ring.
            difference() {
                translate([wall+fit, wall+fit, 0])
                    rounded_prism([outer_w-2*(wall+fit), outer_h-2*(wall+fit),
                                   lid_h-lid_top+0.2], corner_r-wall);
                translate([2*wall+fit, 2*wall+fit, -0.1])
                    rounded_prism([outer_w-2*(2*wall+fit),
                                   outer_h-2*(2*wall+fit),
                                   lid_h-lid_top+0.2],
                                  corner_r-2*wall);
            }
        }

        // Aperture clears the nominal active area by 0.8 mm per side.
        translate([screen_center[0]-screen_window[0]/2,
                   screen_center[1]-screen_window[1]/2,
                   lid_h-lid_top-0.1])
            rounded_slot([screen_window[0], screen_window[1],
                          lid_top+0.3], 1.2);

        // Blind pilots retain at least 0.85 mm beneath the top skin.  M1.6 x
        // 4 mm screws pass through the 1.0 mm display PCB and engage 3.0 mm.
        for (p = screen_holes)
            translate([p[0], p[1], screen_pcb_top_from_bottom-0.1])
                cylinder(d=screen_pilot_d, h=screen_pilot_depth+0.1);
    }
}

module button_cap() {
    // Insert from inside before installing the carrier.  The internal flange
    // is larger than the wall aperture, so the cap cannot fall out.  The face
    // and stem retain 0.2 mm total Y/Z clearance in the aperture.  Print one
    // trial cap first and tune button_tip_clearance for the actual printer.
    union() {
        rounded_face_x(7.6, 3.4, 1.2, 0.8);
        translate([button_tip_offset, 2.8, 1.0])
            cube([-button_tip_offset+0.2, 2.0, 1.8]);
        translate([-wall-0.7, (7.6-button_flange_y)/2, 0.0])
            cube([0.7, button_flange_y, 3.9]);
    }
}

module switch_cap() {
    // Insert from inside before the carrier.  The Z-wide flange retains the
    // slider while allowing the face and fork to traverse the full 1.5 mm.
    // The open-through-Z fork avoids inventing an actuator height absent from
    // the projection drawing; a first-print dry fit remains mandatory.
    union() {
        rounded_face_x(switch_face_y, 3.2, 1.2, 0.7);
        difference() {
            translate([switch_fork_nose_offset,
                       (switch_face_y-switch_fork_outer_y)/2, 0.9])
                cube([-switch_fork_nose_offset+0.2,
                      switch_fork_outer_y, 1.8]);
            translate([switch_fork_nose_offset-0.1,
                       (switch_face_y-switch_fork_slot_y)/2, 0.8])
                cube([switch_fork_slot_depth+0.1,
                      switch_fork_slot_y, 2.0]);
        }
        translate([-wall-0.7, (switch_face_y-switch_flange_y)/2, -0.1])
            cube([0.7, switch_flange_y, 3.9]);
    }
}

module assembly() {
    color("#202838") base();
    translate([0, 0, base_h]) color("#34415c", 0.9) lid();

    // Visual-only bulk envelopes; '%' objects are not exported in base/lid
    // STLs.  J1/J2/J5 are intentionally excluded pending a low-profile wiring
    // decision, so this view is not an exact component collision result.
    %translate([battery_origin[0], battery_origin[1], battery_z])
        color("#d97706") cube(battery_envelope);
    %translate([controller_origin[0], controller_origin[1], controller_z])
        color("#2563eb") cube(controller_envelope);
    %translate([carrier_origin[0], carrier_origin[1], carrier_z])
        color("#15803d") cube(carrier_size);
    %translate([carrier_origin[0], carrier_origin[1],
                carrier_z+carrier_size[2]])
        color("#22c55e") cube([carrier_size[0], carrier_size[1],
                               carrier_low_profile_h]);
    %translate([screen_origin[0], screen_origin[1], screen_z])
        color("#0ea5e9") cube(screen_module);

    for (y = button_y)
        translate([outer_w, y-3.8, button_z-1.7])
            color("#94a3b8") button_cap();
    // Mid-travel preview; the installed slider moves +/-0.75 mm along Y.
    translate([outer_w, power_switch_y-switch_face_y/2, button_z-1.6])
        color("#64748b") switch_cap();
}

if (part_id == 1) base();
else if (part_id == 2) lid();
else if (part_id == 3) button_cap();
else if (part_id == 4) switch_cap();
else assembly();
