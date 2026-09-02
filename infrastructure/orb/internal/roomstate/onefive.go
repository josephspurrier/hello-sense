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

// The 1.5's extra sensors. Conversions are the reference's
// (SenseOneFiveDataConversion); the scales are not, because the reference
// snapshot we have predates its classifiers for them. The bands below are
// ordinary indoor-air guidance and are documented as ours.

// PressureMillibar converts the Q24.8 pascal reading.
func PressureMillibar(raw int32) float32 { return float32(raw) / 256.0 / 100.0 }

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

// UVIndex converts the UV count.
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
		{"Fresh", "The air is fresh.", nil, F32(999.99), Ideal},
		{"Stuffy", "The air is a bit stuffy.", F32(1000), F32(1999.99), Warning},
		{"Stale", "The air is stale.", F32(2000), nil, Alert},
	}
	TVOCScale = []Interval{
		{"Low", "Airborne chemicals are low.", nil, F32(299.99), Ideal},
		{"Elevated", "Airborne chemicals are a bit elevated.", F32(300), F32(999.99), Warning},
		{"High", "Airborne chemicals are high.", F32(1000), nil, Alert},
	}
	PressureScale = []Interval{
		{"Normal", "Air pressure is normal.", nil, nil, Ideal},
	}
	UVScale = []Interval{
		{"Low", "UV light is low.", nil, F32(2.99), Ideal},
		{"Moderate", "UV light is moderate.", F32(3), F32(5.99), Warning},
		{"High", "UV light is high.", F32(6), nil, Alert},
	}
	LightTemperatureScale = []Interval{
		{"Warm", "The light is warm.", nil, F32(3999.99), Ideal},
		{"Cool", "The light is cool. Blue light late in the evening can delay sleep.", F32(4000), nil, Warning},
	}
)
