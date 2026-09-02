package roomstate

import "testing"

func TestOneFiveConversions(t *testing.T) {
	// A lit room on the voice Sense, 2026-09-02 09:22 ET: lux_count 184 with
	// light (the clear channel) 43. The 1.0 curve reads that as dark.
	lux := int32(184)
	if got := Lux(SenseOneFive, 43, &lux); got < 36 || got > 37 {
		t.Errorf("1.5 lux = %v, want about 36.8", got)
	}
	if got := Lux(0, 43, &lux); got > 1 {
		t.Errorf("1.0 lux of the clear channel = %v, want under 1", got)
	}
	if got := Lux(SenseOneFive, 43, nil); got != 0 {
		t.Errorf("1.5 lux with no count = %v, want 0", got)
	}

	// Raw 2744 is 27.44 C before the self-heating offset.
	if got := Temperature(SenseOneFive, 2744); got < 21.43 || got > 21.45 {
		t.Errorf("1.5 temperature = %v, want 21.44", got)
	}
	if got := Temperature(0, 2744); got < 23.54 || got > 23.56 {
		t.Errorf("1.0 temperature = %v, want 23.55", got)
	}
	if got := Humidity(SenseOneFive, 2744, 4979); got != 49.79 {
		t.Errorf("1.5 humidity = %v, want 49.79", got)
	}

	if got := PressureMillibar(26009633); got < 1015 || got > 1017 {
		t.Errorf("pressure = %v mbar, want about 1016", got)
	}
	if got := CO2PPM(300); got != 400 {
		t.Errorf("co2 floor = %v, want 400", got)
	}
	// No red to divide by: the reference's floor.
	if got := LightTemperatureKelvin(0, 0, 0, 0); got != 1000 {
		t.Errorf("dark light temperature = %v, want 1000", got)
	}
	// Warm light: more red than blue after the IR correction.
	if got := LightTemperatureKelvin(100, 80, 40, 200); got <= 1000 || got >= 4000 {
		t.Errorf("warm light temperature = %v, want between 1000 and 4000", got)
	}
}
