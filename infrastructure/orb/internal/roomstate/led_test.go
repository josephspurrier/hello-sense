package roomstate

import "testing"

// The reading this device actually uploaded at 2026-08-17T13:29:34Z, taken from
// suripu-service's log of the decoded protobuf. Anchoring the test to a real
// upload rather than to invented numbers is what caught the light bug: the
// values have to be plausible together, and made-up ones are not.
var observed = DeviceSample{
	Temperature:            2535,
	Humidity:               4049,
	Light:                  2156,
	DustMax:                605,
	AudioPeakBackgroundDB:  10,
	AudioPeakDisturbanceDB: 10,
}

func offset(v int32) *int32 { return &v }

// What the hardware showed for this reading once new_room_condition was on:
// `lightson 3` and `lightsoff 2`, read off the device's own log. 3 is ALERT and
// 2 is WARNING.
//
// Lights-on is ALERT on the light, which converts to 8.22 lux and just clears
// the 8 lux line. Lights-off is WARNING on the temperature: 21.46°C lands in
// "Warm", because the reference's ideal band stops at 19.99.
func TestMatchesTheHardware(t *testing.T) {
	on, off := Conditions(observed, offset(395))
	if on != Alert {
		t.Errorf("lights-on = %s, want %s", on, Alert)
	}
	if off != Warning {
		t.Errorf("lights-off = %s, want %s", off, Warning)
	}
}

// The deliberate divergence, and the reason this package converts light when
// the reference does not.
//
// A raw count of 100 is 0.38 lux, a properly dark room. The reference would
// classify 100 as 100 lux and call it ALERT, which is the bug. Here the only
// thing left to complain about is the temperature, so both slots agree.
//
// If this test ever fails because someone "fixed" CalibratedLux out of the
// path, the Orb will glow red all night and nothing else will say why.
func TestDarkRoomIsNotAnAlert(t *testing.T) {
	dark := observed
	dark.Light = 100

	on, off := Conditions(dark, offset(395))
	if on != Warning {
		t.Errorf("lights-on in a dark room = %s, want %s (temperature only)", on, Warning)
	}
	if off != Warning {
		t.Errorf("lights-off in a dark room = %s, want %s", off, Warning)
	}
}

// Lights-off forces light to IDEAL rather than dropping the sensor, so a room
// that is bright AND otherwise perfect still reads IDEAL at bedtime.
func TestLightsOffIgnoresBrightness(t *testing.T) {
	// 18°C and 45% humidity, both squarely ideal, in a very bright room.
	s := DeviceSample{
		Temperature:            2189, // 18.0°C after the 3.89 calibration
		Humidity:               4500,
		Light:                  500000, // ~1907 lux
		DustMax:                605,
		AudioPeakDisturbanceDB: 10,
	}

	on, off := Conditions(s, offset(395))
	if on != Alert {
		t.Errorf("lights-on = %s, want %s: the room is genuinely bright", on, Alert)
	}
	if off != Ideal {
		t.Errorf("lights-off = %s, want %s: light must not count when the lights are off", off, Ideal)
	}
}

// Dust joins the judgement only when the device's offset is known, and it is
// the one modality that can be absent.
func TestDustCountsOnlyWhenCalibrated(t *testing.T) {
	// A dust reading bad enough to alert on its own, with everything else ideal.
	s := DeviceSample{
		Temperature:            2189,
		Humidity:               4500,
		Light:                  100,
		DustMax:                4000,
		AudioPeakDisturbanceDB: 10,
	}

	if _, off := Conditions(s, nil); off != Ideal {
		t.Errorf("uncalibrated lights-off = %s, want %s: dust must not count", off, Ideal)
	}
	if _, off := Conditions(s, offset(0)); off == Ideal {
		t.Error("calibrated lights-off = IDEAL, want dust to have been counted")
	}
}

// The V2 rule is any-one-fails, not a weighted score. This is the whole
// behavioural difference from the legacy scoring it replaced, and the reason
// enabling new_room_condition made this room's LED redder rather than greener.
func TestOneBadSensorIsEnough(t *testing.T) {
	// Ideal everything, then break exactly one thing.
	base := DeviceSample{
		Temperature:            2189,
		Humidity:               4500,
		Light:                  100,
		DustMax:                605,
		AudioPeakDisturbanceDB: 10,
	}
	if _, off := Conditions(base, offset(395)); off != Ideal {
		t.Fatalf("baseline lights-off = %s, want %s", off, Ideal)
	}

	loud := base
	// 90 dB alerts. The conversion is raw/1024 - 40 + 25, so 107520 raw is
	// 105 dB and comfortably over the line.
	loud.AudioPeakDisturbanceDB = 107520
	if _, off := Conditions(loud, offset(395)); off != Alert {
		t.Errorf("one alerting sensor gave %s, want %s", off, Alert)
	}

	humid := base
	// 50% as measured, which is NOT 50% in the room: the correction to the
	// cooler true temperature pushes it to about 64%, into "Somewhat humid".
	// Raising this much further alerts rather than warns.
	humid.Humidity = 5000
	if _, off := Conditions(humid, offset(395)); off != Warning {
		t.Errorf("one warning sensor gave %s, want %s", off, Warning)
	}
}

// The device path reads the DISTURBANCE column and ignores the background one,
// which is the reference's behaviour and easy to get backwards.
func TestDeviceSoundUsesDisturbance(t *testing.T) {
	// A loud background and a silent disturbance must still read as silence.
	if got := DeviceSound(107520, 10); got != 0 {
		t.Errorf("DeviceSound(background=107520, disturbance=10) = %v, want 0", got)
	}
	// And the reverse: the disturbance is what carries. 107520/1024 is 105 dB,
	// less the 40 dB noise floor and plus the 25 dB artificial one, so 90.
	if got := DeviceSound(10, 107520); got != 90 {
		t.Errorf("DeviceSound(background=10, disturbance=107520) = %v, want 90", got)
	}
}
