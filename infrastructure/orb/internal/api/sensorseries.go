package api

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/roomstate"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// POST /v2/sensors: the graph behind each dial.
//
// A read despite the verb; the body is a query and nothing is written. Second
// most called endpoint in the app, just behind its own GET.
//
// The response is two parallel arrays rather than a list of objects, which is
// what keeps a week of five-minute samples to a sensible size:
//
//	{"timestamps": [{"t": ..., "o": ...}, ...],
//	 "sensors": {"TEMPERATURE": [23.1, ...]}}
//
// The two are positional. A value at index i belongs to the timestamp at index
// i, so nothing may ever be dropped from one array without the other.

type batchQuery struct {
	Scope   string   `json:"scope"`
	Sensors []string `json:"sensors"`
}

// batchStamp is a slot's time and the offset in force then. Short keys because
// there are up to 289 of them.
type batchStamp struct {
	T int64 `json:"t"`
	O int32 `json:"o"`
}

type batchResponse struct {
	Sensors    map[string][]float32 `json:"sensors"`
	Timestamps []batchStamp         `json:"timestamps"`
}

// scopes are the three windows the app asks for, with the slot size each is
// drawn at.
var scopes = map[string]struct {
	window time.Duration
	slot   time.Duration
}{
	"LAST_3H_5_MINUTE": {3 * time.Hour, 5 * time.Minute},
	"DAY_5_MINUTE":     {24 * time.Hour, 5 * time.Minute},
	"WEEK_1_HOUR":      {7 * 24 * time.Hour, time.Hour},
}

// missingSample is what a slot with no data carries.
//
// -1, not null. The same sentinel the timeline and trends use, and the app
// knows to draw a gap rather than a reading below zero. Sending null here would
// be more honest and is not what the app parses.
const missingSample float32 = -1

func (h *Handler) postSensors(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var q batchQuery
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid query."})
		return
	}
	scope, ok := scopes[q.Scope]
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid scope."})
		return
	}

	// The grid ends at the current slot boundary, rounded DOWN, and runs back
	// window/slot + 1 slots. Derived from the clock rather than from the data:
	// the app draws a fixed axis and expects the same number of points every
	// time it asks.
	now := time.Now().UTC()
	end := now.Truncate(time.Minute).Truncate(scope.slot)
	count := int(scope.window/scope.slot) + 1

	// The DATA window starts at the unrounded now minus the scope, while the
	// GRID starts at the rounded slot boundary before that. They are not the
	// same instant, and the difference is deliberate in the reference: the
	// oldest slot is partial, holding only the samples inside the real window
	// rather than the whole slot. Using the grid start here instead fills that
	// slot with a few extra minutes of data and is wrong on exactly one point
	// out of 37, which is easy to miss and easy to dismiss as a clock skew.
	samples, err := h.store.SamplesBetween(r.Context(), accountID,
		now.Add(-scope.window), end.Add(scope.slot))
	if err != nil {
		h.log.Error("sensor series", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	offsetMS, _, err := h.store.OffsetMSAt(r.Context(), accountID, now)
	if err != nil {
		h.log.Error("sensor series offset", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	out := batchResponse{Sensors: map[string][]float32{}, Timestamps: []batchStamp{}}
	if len(samples) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	// Bucket by slot, then reduce. The reducers differ per sensor and that is
	// the part worth being careful about: see slot.
	buckets := map[int64]*slot{}
	for _, s := range samples {
		key := s.TS.UTC().Truncate(scope.slot).UnixMilli()
		b := buckets[key]
		if b == nil {
			b = &slot{}
			buckets[key] = b
		}
		b.add(s)
	}

	for i := count - 1; i >= 0; i-- {
		t := end.Add(-time.Duration(i) * scope.slot)
		out.Timestamps = append(out.Timestamps, batchStamp{T: t.UnixMilli(), O: offsetMS})
	}

	for _, name := range q.Sensors {
		values := make([]float32, 0, count)
		for _, stamp := range out.Timestamps {
			b := buckets[stamp.T]
			if b == nil || b.n == 0 {
				values = append(values, missingSample)
				continue
			}
			values = append(values, b.value(name))
		}
		out.Sensors[name] = values
	}

	writeJSON(w, http.StatusOK, out)
}

// slot reduces the samples that fell in one bucket.
//
// The reducers are NOT all the mean, and assuming they are gives a plausible
// wrong answer on most of them:
//
//   - temperature is the MINIMUM. Sense measures its own board as well as the
//     room, so the coolest reading in a slot is the closest to the truth.
//     Averaging bakes the self-heating back in that the calibration constant
//     exists to remove.
//   - the audio columns are the MAXIMUM, because a peak is the point of a peak.
//   - humidity and light are the rounded mean.
type slot struct {
	n                     int
	minTemp               int32
	sumHumidity, sumLight int64
	maxEnergy, maxDisturb int32
	sumDust               int64
	dustOffset            *int32

	// The device's generation and the 1.5's extras, summed over the minutes
	// that carried them (nLux and so on count those minutes, since rows from
	// before orb kept the extras have none).
	hw                                          int32
	sumLux, sumPressure, sumTVOC, sumCO2, sumUV int64
	nLux, nPressure, nTVOC, nCO2, nUV           int
	sumR, sumG, sumB, sumClear                  int64
	nRGB                                        int
}

func (s *slot) add(r store.LatestSampleRow) {
	if s.n == 0 || r.Temperature < s.minTemp {
		s.minTemp = r.Temperature
	}
	s.sumHumidity += int64(r.Humidity)
	s.sumLight += int64(r.Light)
	s.sumDust += int64(r.AirQualityRaw)
	s.dustOffset = r.DustOffset
	if r.AudioPeakEnergyDB > s.maxEnergy {
		s.maxEnergy = r.AudioPeakEnergyDB
	}
	if r.AudioPeakDisturbancesDB > s.maxDisturb {
		s.maxDisturb = r.AudioPeakDisturbancesDB
	}
	s.hw = r.HWVersion
	sumIf := func(v *int32, sum *int64, n *int) {
		if v != nil {
			*sum += int64(*v)
			*n++
		}
	}
	sumIf(r.LuxCount, &s.sumLux, &s.nLux)
	sumIf(r.Pressure, &s.sumPressure, &s.nPressure)
	if roomstate.GasReady(r.TVOC) {
		sumIf(r.TVOC, &s.sumTVOC, &s.nTVOC)
	}
	if roomstate.GasReady(r.CO2) {
		sumIf(r.CO2, &s.sumCO2, &s.nCO2)
	}
	sumIf(r.UVCount, &s.sumUV, &s.nUV)
	if r.R != nil && r.G != nil && r.B != nil && r.Clear != nil {
		s.sumR += int64(*r.R)
		s.sumG += int64(*r.G)
		s.sumB += int64(*r.B)
		s.sumClear += int64(*r.Clear)
		s.nRGB++
	}
	s.n++
}

// roundedMean matches the reference's integer rounding before calibration.
// Calibrating each sample and averaging the results is not the same number.
func roundedMean(sum int64, n int) int32 {
	if n == 0 {
		return 0
	}
	return int32((sum + int64(n)/2) / int64(n))
}

// round1 is the one decimal place the wire carries.
//
// The reference rounds here, not in the app, so a value that is not rounded is
// visibly different: 48.11301 against 48.5. Half away from zero, matching Java's
// Math.round, which is NOT Go's default banker-ish formatting of a float32.
func round1(v float32) float32 {
	return float32(math.Floor(float64(v)*10+0.5)) / 10
}

func (s *slot) value(sensor string) float32 {
	temp := s.minTemp
	humidity := roundedMean(s.sumHumidity, s.n)
	switch sensor {
	case "TEMPERATURE":
		return round1(roomstate.Temperature(s.hw, temp))
	case "HUMIDITY":
		return round1(roomstate.Humidity(s.hw, temp, humidity))
	case "LIGHT":
		if roomstate.IsOneFive(s.hw) {
			if s.nLux == 0 {
				return missingSample
			}
			lux := roundedMean(s.sumLux, s.nLux)
			return round1(roomstate.Lux(s.hw, 0, &lux))
		}
		return round1(calibratedLux(roundedMean(s.sumLight, s.n)))
	case "CO2":
		if s.nCO2 == 0 {
			return missingSample
		}
		return round1(roomstate.CO2PPM(roundedMean(s.sumCO2, s.nCO2)))
	case "TVOC":
		if s.nTVOC == 0 {
			return missingSample
		}
		return round1(roomstate.TVOC(roundedMean(s.sumTVOC, s.nTVOC)))
	case "PRESSURE":
		if s.nPressure == 0 {
			return missingSample
		}
		return round1(roomstate.PressureMillibar(roundedMean(s.sumPressure, s.nPressure)))
	case "UV":
		if s.nUV == 0 {
			return missingSample
		}
		return round1(float32(roundedMean(s.sumUV, s.nUV)))
	case "LIGHT_TEMP":
		if s.nRGB == 0 {
			return missingSample
		}
		return round1(roomstate.LightTemperatureKelvin(roundedMean(s.sumR, s.nRGB),
			roundedMean(s.sumG, s.nRGB), roundedMean(s.sumB, s.nRGB), roundedMean(s.sumClear, s.nRGB)))
	case "PARTICULATES":
		// Rounded mean, matching the reference's aggregation for the raw dust
		// column. Averaged as counts and converted once, not converted per
		// sample and then averaged.
		return round1(calibratedParticulates(roundedMean(s.sumDust, s.n), s.dustOffset))
	case "SOUND":
		// Through the same re-read as the dial: this path builds its DeviceData
		// with the raw builder too, so the 1000/1024 error is in the graph as
		// well. Applied after the max, because the max runs on stored values.
		return round1(calibratedSound(s.maxEnergy, s.maxDisturb))
	default:
		return missingSample
	}
}
