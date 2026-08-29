// Package roomstate turns raw sensor readings into the conditions that colour
// them: the bands behind the app's dials, and the single overall condition
// behind the Sense's own LED.
//
// It exists as its own package because those two callers must not drift. The
// app's Air Quality dial and the Orb's night-time LED colour are the same
// judgement about the same room, made from the same thresholds, and the moment
// they live in two files they will disagree by a band and nobody will notice
// for a month. `internal/api` renders them as JSON, `internal/edge` renders one
// of them as a protobuf enum, and both ask this package what the condition is.
//
// The scales and the calibration arithmetic were moved here wholesale from
// api/sensors.go, where they were verified against the reference by diffing
// live responses. Nothing about their behaviour changed in the move.
package roomstate

import "math"

// The three conditions, as the app's JSON spells them.
//
// Strings rather than an enum because that is what the sensors endpoint puts on
// the wire and what the scale tables were written with. The device path maps
// them onto the protobuf enum, which is a different numbering and belongs with
// the protobuf.
const (
	Ideal   = "IDEAL"
	Warning = "WARNING"
	Alert   = "ALERT"
)

// Interval is one band on a sensor's scale.
//
// Min and Max are pointers because the outermost bands are open-ended: "Cold"
// has no minimum and "Hot" has no maximum, and the app draws an arrow rather
// than a boundary. Zero would be a real temperature.
//
// Message is carried alongside Condition because the matching band supplies the
// sensor's own message; the bands are rendered to the app with just a name and
// a range.
type Interval struct {
	Name      string
	Message   string
	Min, Max  *float32
	Condition string
}

// F32 is the pointer helper the open-ended bands need.
func F32(v float32) *float32 { return &v }

// The five scales, copied from the reference's scale classes.
//
// The gaps between bands are deliberate and are in the original: temperature
// runs to 9.99 then resumes at 10, so 9.995 falls in no band at all. Closing
// the gaps would be tidier and would move the boundaries. Classify walks in
// order and takes the first band whose bounds contain the value, which is what
// the reference does, so a value in a gap gets the band above it.
//
// The apostrophes are U+2019, not ASCII. They are compared byte for byte
// against the reference.
//
// LightScale and NoiseScale carry the same boundaries as the reference's
// LightClassifier and SoundClassifier, which the LED path uses: light warns
// above 2 lux and alerts above 8, sound warns above 65 dB and alerts above 90.
// Checked against Lights.LIGHT_LEVEL_WARNING/ALERT and
// SoundLevel.SOUND_LEVEL_WARNING/ALERT, which is why one table can serve both
// callers.
var (
	TemperatureScale = []Interval{
		{"Cold", "It’s far too cold.", nil, F32(9.99), Alert},
		{"Cool", "It’s a bit cool.", F32(10), F32(14.99), Warning},
		{"Ideal", "The temperature is just right.", F32(15), F32(19.99), Ideal},
		{"Warm", "It’s a bit warm.", F32(20), F32(25.99), Warning},
		{"Hot", "It’s far too hot.", F32(26), nil, Alert},
	}
	HumidityScale = []Interval{
		{"Dry", "It’s far too dry.", F32(0), F32(20.99), Alert},
		{"Somewhat dry", "It’s a bit dry.", F32(21), F32(30.99), Warning},
		{"Ideal", "The humidity is just right.", F32(31), F32(60.99), Ideal},
		{"Somewhat humid", "It’s a bit humid.", F32(61), F32(80.99), Warning},
		{"Humid", "It’s far too humid.", F32(81), F32(100), Alert},
	}
	LightScale = []Interval{
		{"Ideal", "The light level is just right.", F32(0), F32(1.99), Ideal},
		{"Somewhat bright", "It’s a bit bright.", F32(2), F32(7.99), Warning},
		{"Bright", "It’s a bit bright.", F32(8), F32(14.99), Alert},
		{"Very bright", "It’s far too bright.", F32(15), F32(49.99), Alert},
		{"Extremely bright", "It’s far too bright.", F32(50), nil, Alert},
	}
	// Six bands, and note that "Moderate" carries IDEAL rather than WARNING:
	// the first two bands are both green. Copied from ParticulatesScale.
	//
	// The top band is CLOSED at 399.9, unlike temperature's open-ended "Hot".
	// A reading above that matches no band, and Classify falls back to the last
	// one, which is Hazardous. That is the right answer, but it arrives by the
	// fallback rather than by the range.
	ParticulatesScale = []Interval{
		{"Ideal", "The air quality is just right.", F32(0), F32(49.9), Ideal},
		{"Moderate", "The air quality is moderate.", F32(50), F32(99.9), Ideal},
		{"Unhealthy for sensitive groups", "The air quality is unhealthy for sensitive groups.", F32(100), F32(149.9), Warning},
		{"Unhealthy", "The air quality is unhealthy.", F32(150), F32(199.9), Warning},
		{"Very unhealthy", "The air quality is very unhealthy.", F32(200), F32(299.9), Alert},
		{"Hazardous", "The air quality is hazardous.", F32(300), F32(399.9), Alert},
	}
	NoiseScale = []Interval{
		{"Quiet", "The noise level is just right.", F32(0), F32(64.99), Ideal},
		{"Somewhat noisy", "It’s a bit noisy.", F32(65), F32(69.99), Warning},
		{"Noisy", "It’s a bit noisy.", F32(70), F32(89.99), Warning},
		{"Very noisy", "It’s far too noisy.", F32(90), F32(129.99), Alert},
		{"Extremely noisy", "It’s far too noisy.", F32(130), nil, Alert},
	}
)

// Classify finds the band a value falls in.
//
// Falls back to the LAST band rather than the first when nothing matches, which
// is what a value above every maximum wants: the top band is the open-ended one.
func Classify(v float32, scale []Interval) Interval {
	for _, iv := range scale {
		if iv.Min != nil && v < *iv.Min {
			continue
		}
		if iv.Max != nil && v > *iv.Max {
			continue
		}
		return iv
	}
	return scale[len(scale)-1]
}

// The calibration constants, from DataUtils.
const (
	// 389 hundredths of a degree, which is 7 degrees Fahrenheit. Sense reads
	// warm because it measures its own board as well as the room.
	TemperatureCalibrationCelsius = 389
	FloatToIntMultiplier          = 100.0
	audioFloatToIntMultiplier     = 1000.0
)

// CalibratedTemperature converts the stored hundredths of a degree.
func CalibratedTemperature(raw int32) float32 {
	return float32(raw-TemperatureCalibrationCelsius) / FloatToIntMultiplier
}

// CalibratedHumidity corrects the reading for Sense's own warmth.
//
// Not a subtraction. The sensor's relative humidity was measured at the board's
// temperature, which is 3.89 degrees too high, so the reading is converted to a
// dew point (which is absolute) and back at the corrected temperature. Applying
// the temperature offset to the humidity directly gives a number that looks
// reasonable and is wrong in a way that grows with how dry the room is.
func CalibratedHumidity(rawTemp, rawHumidity int32) float32 {
	temperature := float64(float32(rawTemp) / FloatToIntMultiplier)
	humidity := float64(float32(rawHumidity) / FloatToIntMultiplier)
	dewPoint := computeDewPoint(temperature, humidity)
	adjusted := temperature - float64(TemperatureCalibrationCelsius)/FloatToIntMultiplier
	return float32(computeHumidity(adjusted, dewPoint))
}

func computeDewPoint(temperature, humidity float64) float64 {
	saturation := 6.11 * math.Pow(10.0, 7.5*(temperature/(237.7+temperature)))
	actual := (humidity * saturation) / 100.0
	logVapor := math.Log(actual)
	return (-430.22 + 237.7*logVapor) / (-1.0*logVapor + 19.08)
}

func computeHumidity(temperature, dewPoint float64) float64 {
	saturation := 6.11 * math.Pow(10.0, 7.5*(temperature/(237.7+temperature)))
	actual := 6.11 * math.Pow(10.0, 7.5*(dewPoint/(237.7+dewPoint)))
	return (actual / saturation) * 100.0
}

// CalibratedLux converts the raw light count.
//
// The 2x is the white Sense's internal-to-external conversion, baked in because
// the reference has no way to read the enclosure colour either: its own comment
// says "set conversion to 2x for now until we have a way to get sense color". A
// black Sense would want 5x on top and does not get it there either, so
// matching the reference means keeping the same gap.
func CalibratedLux(rawCount int32) float32 {
	const maxLux = 8000.0
	const maxCount = 4194304.0
	const whiteMultiplier = 2.0

	if float32(rawCount) > maxCount {
		return whiteMultiplier * maxLux
	}
	internal := (float32(rawCount) / maxCount) * maxLux
	return whiteMultiplier * internal
}

// audioCountsPerDB is the raw-ADC-to-decibel divisor the Sense firmware uses.
//
// On the app path it has no business being applied and applying it is the
// point: see ReReadAudio. On the device path it is the genuine conversion, and
// DeviceSound applies it once to a value that has not been converted yet.
const audioCountsPerDB = 1024.0

// ReReadAudio reproduces a conversion the reference applies to a value that has
// already been converted.
//
// The stored column is millidecibels, written that way at ingest. On the way
// back out, the DynamoDB row is turned into a DeviceData by
// `attributeMapToDeviceData`, which calls `withAudioPeakEnergyDB`, and that
// builder assumes it is being handed a raw ADC count: it divides by 1024 and
// multiplies by 1000 again. The sibling converter, `dynamoItemToRawDeviceData`,
// calls `withAlreadyCalibratedPeakEnergyDB` and does not. Two converters, two
// builders, and only one of them is right; `getMostRecent`, which is what the
// sensors endpoint uses, takes the wrong one.
//
// So every sound value the app has ever shown is scaled by 1000/1024, about
// 2.3% low, and the truncation back to an int loses a little more. That is
// wrong and it is what the app receives, so orb reproduces it exactly rather
// than quietly serving a better number that disagrees.
//
// Found by arithmetic, not by reading: orb read about 1 dB high with a gap that
// would not sit still (1.023, then 1.049), and a proportional error is what a
// non-constant gap looks like.
func ReReadAudio(stored int32) float32 {
	db := float32(stored) / audioCountsPerDB
	// Truncated, not rounded: Java's (int) cast toward zero, and the discarded
	// fraction is the rest of the discrepancy.
	return float32(int32(db*audioFloatToIntMultiplier)) / audioFloatToIntMultiplier
}

// CalibratedSound converts the peak ENERGY, not the peak disturbance.
//
// This is the APP path, reading stored columns. The device path is DeviceSound,
// and the two genuinely differ in which column they read: see that function.
//
// The fallback matters: the reference tests energy for zero AFTER the re-read
// above, and a zero energy reading means the minute has no energy figure at all
// rather than a silent room, so it falls back to the disturbance column.
//
// The 40 removes the sensor's noise floor and the 25 puts an artificial one
// back, so a silent room reads about 25 dB rather than 0.
func CalibratedSound(rawPeakEnergyDB, rawPeakDisturbancesDB int32) float32 {
	peak := ReReadAudio(rawPeakEnergyDB)
	if peak == 0 {
		peak = ReReadAudio(rawPeakDisturbancesDB)
	}
	return noiseFloor(peak)
}

// DeviceSound converts the audio the Sense just uploaded, for the LED.
//
// Three things differ from the app path and all three are the reference's,
// from ReceiveResource calling CurrentRoomState.fromRawData:
//
//  1. The DISTURBANCE column, not the energy column. fromRawData is handed
//     (audio_peak_background_energy_db, audio_peak_disturbance_energy_db) and
//     passes the second of those as its "peak".
//  2. The background argument is accepted and then completely ignored by
//     DataUtils.calibrateAudio, which only ever reads peakDB. It is taken here
//     for the same reason: so the call site reads like the reference's and
//     nobody re-derives why one of the two columns vanished.
//  3. A single honest divide by 1024, because this value came straight off the
//     wire and has not been through the stored-column round trip that
//     ReReadAudio exists to undo.
//
// The firmware-blacklist branch in calibrateAudio is not reproduced: it applies
// to 0.4.0 through 0.9.x, and this Orb runs 1.9.2. Were it ever to matter the
// difference is only where the max() sits relative to the artificial floor.
func DeviceSound(rawBackgroundDB, rawDisturbanceDB int32) float32 {
	_ = rawBackgroundDB
	return noiseFloor(float32(rawDisturbanceDB) / audioCountsPerDB)
}

// noiseFloor swaps the sensor's real noise floor for an artificial one, so a
// silent room reads about 25 dB rather than 0.
func noiseFloor(peakDB float32) float32 {
	if v := peakDB - 40.0 + 25.0; v > 0 {
		return v
	}
	return 0
}

// maxDustAnalogValue is the dust sensor's full-scale count.
const maxDustAnalogValue = 4095.0

// The per-device dust calibration, from the reference's Calibration model.
//
// The stored value is NOT the delta. `dust_offset` is a factory measurement,
// and the delta applied to readings is derived from it:
//
//	delta = round(300 - dustOffset * 1.3)
//
// This device's offset of 395 gives a delta of -213, which is why an
// uncalibrated orb read about 90 where the reference read about 53. Storing
// the derived delta instead would work until somebody re-tested the sensor and
// wondered why the number in the database matched nothing in the reference.
const (
	dustCalibrationBase    = 300.0
	dustCalibrationKFactor = 1.3
)

// DustDelta derives the calibration delta from the stored offset.
//
// The rounding is Java's, not Go's, and they disagree on exactly this value.
// Java's Math.round rounds half toward POSITIVE INFINITY, so -213.5 becomes
// -213. Go's math.Round rounds half away from zero and would give -214. One
// count of dust is not much, but the whole point of matching is that nobody has
// to wonder whether a difference is meaningful.
func DustDelta(dustOffset int32) int32 {
	return int32(math.Floor(dustCalibrationBase-float64(dustOffset)*dustCalibrationKFactor) + 0.5)
}

// CalibratedParticulates converts the raw dust count to a density.
//
// The offset is a per-device calibration delta added to the RAW COUNT before
// conversion, not to the density afterwards. Applying it to the density gives a
// number that looks reasonable and is wrong by the conversion factor.
//
// Floored at 0.001 before scaling, so a clean room reads 1 rather than 0: the
// reference does this, and the floor is applied to the density, not the count.
func CalibratedParticulates(rawCount int32, dustOffset *int32) float32 {
	// No calibration means NO delta, not a delta derived from zero. An offset of
	// zero would shift every reading by +300 counts, which is a different and
	// much wronger thing than leaving it alone.
	count := rawCount
	if dustOffset != nil {
		count += DustDelta(*dustOffset)
	}
	density := (float32(count) / maxDustAnalogValue) * 4.1076 * (0.5 / 2.9)
	if density < 0.001 {
		density = 0.001
	}
	return density * 1000.0
}
