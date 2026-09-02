package api

import (
	"net/http"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/roomstate"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// GET /v2/sensors: the current state of the bedroom, as four dials.
//
// Everything here is derived from ONE row, the most recent sensor sample. The
// calibration that turns the stored integers into the numbers the app shows is
// arithmetic rather than analysis, so unlike the timeline it stays in Go.

// SensorsResponse is the whole payload.
type SensorsResponse struct {
	Sensors []SensorView `json:"sensors"`
	Status  string       `json:"status"`
}

type SensorView struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Unit      string          `json:"unit"`
	Value     *float32        `json:"value"`
	Condition string          `json:"condition"`
	Message   string          `json:"message"`
	Scale     []ScaleInterval `json:"scale"`
}

// ScaleInterval is one band on the sensor's dial.
//
// Min and max are pointers because the outermost bands are open-ended: "Cold"
// has no minimum and "Hot" has no maximum, and the app draws an arrow rather
// than a boundary. Zero would be a real temperature.
type ScaleInterval struct {
	Name      string   `json:"name"`
	Min       *float32 `json:"min"`
	Max       *float32 `json:"max"`
	Condition string   `json:"condition"`
}

// The scales and the calibration arithmetic now live in internal/roomstate,
// because the Sense's own LED needs exactly the same judgement about exactly
// the same room and two copies would drift. See that package's doc comment.
//
// These are local names for them rather than a rewrite of every call site: the
// arithmetic here was verified by diffing live responses against the reference,
// and a mechanical rename across it would put that verification at risk to no
// benefit. `interval` is an ALIAS, not a definition, so it is the same type the
// roomstate package and the edge handler use.
type interval = roomstate.Interval

var (
	f32      = roomstate.F32
	classify = roomstate.Classify

	temperatureScale  = roomstate.TemperatureScale
	humidityScale     = roomstate.HumidityScale
	lightScale        = roomstate.LightScale
	particulatesScale = roomstate.ParticulatesScale
	noiseScale        = roomstate.NoiseScale

	calibratedTemperature  = roomstate.CalibratedTemperature
	calibratedHumidity     = roomstate.CalibratedHumidity
	calibratedLux          = roomstate.CalibratedLux
	calibratedSound        = roomstate.CalibratedSound
	calibratedParticulates = roomstate.CalibratedParticulates
	dustDelta              = roomstate.DustDelta
)

func publicScale(scale []interval) []ScaleInterval {
	out := make([]ScaleInterval, 0, len(scale))
	for _, iv := range scale {
		out = append(out, ScaleInterval{
			Name: iv.Name, Min: iv.Min, Max: iv.Max, Condition: iv.Condition,
		})
	}
	return out
}

// Personalising the ideal temperature band.
//
// A REVIVAL, not a port. The reference once shifted its ideal temperature range
// by the answer to "do you sleep better when it's hot or cold", then removed
// it: commit 2c2997a39 added it, 340fd53d8 took the last of it out, and what
// remains in TemperatureHumidity.java is a TODO saying so. The numbers below
// are that original code's, recovered from git history rather than invented.
//
// The asymmetry is theirs and is kept: a cold sleeper's band moves down 3°F, a
// hot sleeper's moves up 5°F. Sleeping hot is the more strongly felt
// complaint.
//
// This makes GET /v2/sensors DIVERGE from the reference for any account that
// answered the question, deliberately and with the user's agreement. It is the
// one place in the port where orb is meant to be better rather than identical.
// The whole scale shifts, not just the ideal band, because shifting one band
// would leave the neighbours overlapping it.
const (
	coldSleeperAdjustF = -3.0
	hotSleeperAdjustF  = 5.0
)

// shiftScale moves every boundary by a delta in degrees Fahrenheit, converted
// to Celsius. The deliberate gaps between bands (9.99 to 10) are preserved
// because every boundary moves by the same amount.
func shiftScale(scale []interval, deltaF float64) []interval {
	deltaC := float32(deltaF * 5.0 / 9.0)
	out := make([]interval, 0, len(scale))
	for _, iv := range scale {
		shifted := iv
		if iv.Min != nil {
			shifted.Min = f32(*iv.Min + deltaC)
		}
		if iv.Max != nil {
			shifted.Max = f32(*iv.Max + deltaC)
		}
		out = append(out, shifted)
	}
	return out
}

// temperatureScaleFor returns the band set for a person's stated preference.
func temperatureScaleFor(preference string) []interval {
	switch preference {
	case "COLD":
		return shiftScale(temperatureScale, coldSleeperAdjustF)
	case "HOT":
		return shiftScale(temperatureScale, hotSleeperAdjustF)
	default:
		return temperatureScale
	}
}

// sensorFreshness is how old the newest sample may be before the dials stop
// claiming to describe the room.
//
// Fifteen minutes, from the reference. Without this orb happily serves a
// three-hour-old temperature as the current one, which is worse than showing
// nothing: the whole point of the screen is that it is now. The gap was found
// when the reference's own ingest stalled and it answered UNKNOWN while orb
// answered with stale readings as though they were live.
const sensorFreshness = 15 * time.Minute

// isStale is split out so the boundary can be tested without a database. The
// comparison is strictly greater than, so a sample exactly at the threshold
// still counts as current.
func isStale(sampleTS, now time.Time) bool {
	return now.Sub(sampleTS) > sensorFreshness
}

func (h *Handler) getSensors(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	sample, err := h.store.LatestSample(r.Context(), accountID)
	if err != nil {
		h.log.Error("sensors", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if sample == nil {
		// No sample means no paired Sense as far as this screen is concerned.
		// The app shows a "pair your Sense" prompt for this, which is why the
		// status is a field rather than a status code.
		writeJSON(w, http.StatusOK, SensorsResponse{
			Sensors: []SensorView{}, Status: "NO_SENSE",
		})
		return
	}

	// A stale sample keeps its sensor on the screen but says nothing about it:
	// the name, unit and scale survive so the dial still draws, the value goes
	// null and the condition goes UNKNOWN.
	stale := isStale(sample.TS, time.Now())
	if stale {
		h.log.Info("sensor data stale",
			"account", accountID, "age", time.Since(sample.TS).Round(time.Second))
	}

	// Personalised if the account answered the hot/cold sleeper question.
	tempPref, err := h.store.SleepTempPreference(r.Context(), accountID)
	if err != nil {
		h.log.Error("sensors temp preference", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, SensorsResponse{
		Sensors: sensorViews(sample, tempPref, stale), Status: "OK",
	})
}

// sensorViews builds the five dials, in order.
//
// Split out of the handler so the ORDER and the COUNT can be tested without a
// database. They could not be before, and a reordering commit dropped the SOUND
// dial entirely while every unit test still passed: the tests all exercised the
// calibration functions and nothing asserted what the endpoint actually
// returns. apidiff caught it against the running reference, which is the wrong
// place to find out.
//
// The order is part of the app's contract, not a presentation detail. The app
// reads this array positionally in places, and it matches the reference:
// TEMPERATURE, HUMIDITY, LIGHT, PARTICULATES, SOUND.
func sensorViews(sample *store.LatestSampleRow, tempPref string, stale bool) []SensorView {
	views := []SensorView{}
	add := func(kind, name, unit string, value float32, scale []interval) {
		v := SensorView{
			Type: kind, Name: name, Unit: unit,
			Value: f32(value), Scale: publicScale(scale),
		}
		if stale {
			// A stale sample keeps its sensor on the screen but says nothing
			// about it: the name, unit and scale survive so the dial still
			// draws, the value goes null and the condition goes UNKNOWN.
			v.Value, v.Condition, v.Message = nil, "UNKNOWN", ""
		} else {
			iv := classify(value, scale)
			v.Condition, v.Message = iv.Condition, iv.Message
		}
		views = append(views, v)
	}

	hw := sample.HWVersion
	add("TEMPERATURE", "Temperature", "CELSIUS",
		roomstate.Temperature(hw, sample.Temperature), temperatureScaleFor(tempPref))
	add("HUMIDITY", "Humidity", "PERCENT",
		roomstate.Humidity(hw, sample.Temperature, sample.Humidity), humidityScale)
	add("LIGHT", "Light", "LUX",
		roomstate.Lux(hw, sample.Light, sample.LuxCount), lightScale)

	// Air quality is ALWAYS shown, which is a deliberate divergence.
	//
	// In the reference a device with no calibration row makes
	// CurrentRoomState.particulates() null, and SensorViewFactory then drops
	// the sensor from the response entirely. No dial, and nothing on screen to
	// say why. That is not a hypothetical: it hid air quality on this account
	// for the whole revival, and was only noticed by comparing against what the
	// hardware actually reports.
	//
	// An uncalibrated reading is more useful than a silent absence, so a null
	// offset is treated as zero and the dial is drawn. See migration 0008.
	add("PARTICULATES", "Air Quality", "MG_CM",
		calibratedParticulates(sample.AirQualityRaw, sample.DustOffset), particulatesScale)

	// "Noise", not "Sound". The type stays SOUND; only the label differs, and
	// the two are not interchangeable in the response.
	// The Sense 1.5's extra sensors, each only when the row carries it. The
	// app groups PARTICULATES, CO2 and TVOC into one air-quality tile and
	// renders the rest on their own; the type and unit strings are the ones
	// SenseKit parses (SENSensor.m). CO2 and TVOC follow PARTICULATES so the
	// group is contiguous.
	if roomstate.IsOneFive(hw) {
		if sample.CO2 != nil {
			add("CO2", "CO2", "PPM", roomstate.CO2PPM(*sample.CO2), roomstate.CO2Scale)
		}
		if sample.TVOC != nil {
			add("TVOC", "Chemicals", "VOC", roomstate.TVOC(*sample.TVOC), roomstate.TVOCScale)
		}
		if sample.Pressure != nil {
			add("PRESSURE", "Air Pressure", "MILLIBAR",
				roomstate.PressureMillibar(*sample.Pressure), roomstate.PressureScale)
		}
		if sample.UVCount != nil {
			add("UV", "UV Light", "RATIO", roomstate.UVIndex(*sample.UVCount), roomstate.UVScale)
		}
		if sample.R != nil && sample.G != nil && sample.B != nil && sample.Clear != nil {
			add("LIGHT_TEMP", "Light Temperature", "KELVIN",
				roomstate.LightTemperatureKelvin(*sample.R, *sample.G, *sample.B, *sample.Clear),
				roomstate.LightTemperatureScale)
		}
	}

	add("SOUND", "Noise", "DB",
		calibratedSound(sample.AudioPeakEnergyDB, sample.AudioPeakDisturbancesDB), noiseScale)

	return views
}
