package roomstate

// The Sense 1.5 (Sense with Voice) is a different instrument from the Sense
// 1.0 and the reference converts it differently: CalibratedDeviceData switches
// to SenseOneFiveDataConversion when a row carries the 1.5's extra sensors.
// Read with the 1.0 formulas, a 1.5's lit room registers as dark (its `light`
// field is the colour sensor's clear channel, about 40 counts, which the 1.0
// curve turns into 0.16 lux) and its temperature reads about two degrees warm.
//
// Everything here is keyed on the hardware version the device reported at
// pairing, the reference's HardwareVersion ids: 1 for the original, 4 for the
// 1.5. Zero, a device that never said, is treated as a 1.0.

const (
	// SenseOneFive is HardwareVersion.SENSE_ONE_FIVE.
	SenseOneFive int32 = 4

	// OneFiveTemperatureCalibration is the 1.5's self-heating offset in
	// hundredths of a degree: SenseOneFiveDataConversion.TEMP_ALPHA_TWO. The
	// reference also applies a derivative term when it has the previous
	// minute's raw value; its own timeline path passes none, and neither
	// does this.
	OneFiveTemperatureCalibration = 600

	// oneFiveLuxPerCount is SenseOneFiveDataConversion.convertLuxCountToLux
	// for a white Sense: lux_count / 5. A black one would divide by 2.
	oneFiveLuxPerCount = 5.0

	// oneFiveUVIndexPerCount is SenseOneFiveDataConversion.UVI_COEFF.
	oneFiveUVIndexPerCount = 1.0 / 5500.0
)

// IsOneFive reports whether a hardware version is the Sense 1.5.
func IsOneFive(hardwareVersion int32) bool { return hardwareVersion == SenseOneFive }

// Temperature converts a raw reading for the device that took it.
func Temperature(hardwareVersion int32, raw int32) float32 {
	if IsOneFive(hardwareVersion) {
		return float32(raw-OneFiveTemperatureCalibration) / FloatToIntMultiplier
	}
	return CalibratedTemperature(raw)
}

// Humidity converts a raw reading for the device that took it. The 1.5's
// humidity is reported as is; the 1.0's is corrected for the board's warmth.
func Humidity(hardwareVersion int32, rawTemp, rawHumidity int32) float32 {
	if IsOneFive(hardwareVersion) {
		return float32(rawHumidity) / FloatToIntMultiplier
	}
	return CalibratedHumidity(rawTemp, rawHumidity)
}

// Lux converts light for the device that took it. On a 1.5 the reading is the
// firmware's lux count, and `light` is ignored; luxCount is nil on rows
// stored before orb kept it (2026-09-02), which read as dark rather than as
// a 1.0 reading of the clear channel.
func Lux(hardwareVersion int32, rawLight int32, luxCount *int32) float32 {
	if IsOneFive(hardwareVersion) {
		if luxCount == nil {
			return 0
		}
		return float32(*luxCount) / oneFiveLuxPerCount
	}
	return CalibratedLux(rawLight)
}

// The 1.5's extra sensors. Conversions are SenseOneFiveDataConversion's and
// the bands are suripu-app's (app/sensors/scales: Co2Scale, VocScale,
// UvScale, PressureScale), copied with their names and messages. Light
// temperature has no reference scale; that one band set is ours.

// PressureMillibar converts the Q24.8 pascal reading.
func PressureMillibar(raw int32) float32 { return float32(raw) / 256.0 / 100.0 }

// GasReady reports whether a CO2 or TVOC reading is a reading at all. For a
// couple of minutes after a reboot the 1.5's gas sensor reports 65021 in both
// fields, a not-ready sentinel, and then omits them. The reference clamps
// anything past its ceiling to the top band, which turns a warm-up into an
// ALERT; a sentinel is treated here as no reading. Real values are hundreds
// to low thousands.
func GasReady(raw *int32) bool { return raw != nil && *raw < 60000 }

// CO2PPM clamps the reading the way the reference does: the sensor's floor is
// 400 and anything past 2000 is a self-calibration fault.
func CO2PPM(raw int32) float32 {
	switch {
	case raw <= 400:
		return 400
	case raw >= 2000:
		return 2000
	}
	return float32(raw)
}

// TVOC clamps the reading the way the reference does.
func TVOC(raw int32) float32 {
	switch {
	case raw <= 0:
		return 0
	case raw >= 4000:
		return 4000
	}
	return float32(raw)
}

// UVIndex converts the UV count to an index. Kept for reference: the app tile
// shows the RAW count against UVScale, as suripu-app did (SensorViewFactory
// passes extra().uvCount() straight to the scale, unit COUNT).
func UVIndex(raw int32) float32 { return float32(raw) * oneFiveUVIndexPerCount }

// LightTemperatureKelvin is SenseOneFiveDataConversion.convertRawToColorTemp:
// the correlated colour temperature from the RGB and clear channels. Returns
// the 1000 K floor when there is no red to divide by, which is the reference's
// answer for a dark room too.
func LightTemperatureKelvin(r, g, b, clear int32) float32 {
	const coeff, offset = 5500.0, 1000.0
	ir := float32(r+g+b-clear) / 2.0
	rClean := float32(r) - ir
	bClean := float32(b) - ir
	if rClean <= 0 {
		return offset
	}
	return coeff*bClean/rClean + offset
}

var (
	CO2Scale = []Interval{
		{"Ideal", "The CO2 level is just right.", F32(0), F32(599.9), Ideal},
		{"Elevated", "The CO2 level is elevated.", F32(600), F32(1199.9), Warning},
		{"Unhealthy", "The CO2 level is unhealthy.", F32(1200), nil, Alert},
	}
	TVOCScale = []Interval{
		{"Ideal", "The VOC level is just right.", F32(0), F32(499.99), Ideal},
		{"Elevated", "The VOC level is elevated.", F32(500), F32(3999.99), Warning},
		{"Unhealthy", "The VOC level is unhealthy.", F32(4000), nil, Alert},
	}
	UVScale = []Interval{
		{"Low", "The UV level is just right.", F32(0), F32(2.99), Ideal},
		{"Moderate", "The UV level is a bit high.", F32(3), F32(5.99), Warning},
		{"High", "The UV level is far too high.", F32(6), F32(7.99), Alert},
		{"Very High", "The UV level is far too high.", F32(8), F32(10.99), Alert},
		{"Extreme", "The UV level is far too high.", F32(11), nil, Alert},
	}

	// PressureChangeScale is PressureScale.changeScale(): bands over the
	// CHANGE in millibar since about four hours earlier. "Pressure sensor is
	// basically one big exception" (SensorViewFactory): the condition comes
	// from the change, while the scale the app draws is these same bands
	// shifted to sit around the current reading (PressureScaleAround). The
	// trailing space in "Stable " is the reference's.
	PressureChangeScale = []Interval{
		{"Decreasing", "The barometric pressure is decreasing.", nil, F32(-40.1), Alert},
		{"Decreasing slightly", "The barometric pressure is decreasing.", F32(-40), F32(-20.1), Warning},
		{"Stable ", "The barometric pressure is just right.", F32(-20), F32(20), Ideal},
		{"Increasing slightly", "The barometric pressure is increasing.", F32(20.1), F32(40), Warning},
		{"Increasing", "The barometric pressure is increasing.", F32(40.1), nil, Alert},
	}

	LightTemperatureScale = []Interval{
		{"Warm", "The light is warm.", nil, F32(3999.99), Ideal},
		{"Cool", "The light is cool. Blue light late in the evening can delay sleep.", F32(4000), nil, Warning},
	}
)

// PressureScaleAround is PressureScale.intervals(): the change bands shifted
// so they sit around the current reading, which is what the app draws.
func PressureScaleAround(current float32) []Interval {
	out := make([]Interval, 0, len(PressureChangeScale))
	for _, iv := range PressureChangeScale {
		shifted := iv
		if iv.Min != nil {
			shifted.Min = F32(current + *iv.Min)
		}
		if iv.Max != nil {
			shifted.Max = F32(current + *iv.Max)
		}
		out = append(out, shifted)
	}
	return out
}
