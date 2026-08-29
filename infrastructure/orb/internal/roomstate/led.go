package roomstate

// The overall room condition, which is what the Sense's own LED shows.
//
// Two of them, actually. The firmware keeps TWO colour slots in `room_color[2]`
// (led_cmd.c), indexed by the bool argument to led_set_user_color, and picks
// between them by whether the room is lit. So every sync response carries both
// a lights-on condition and a lights-off one, and the device decides which to
// display. Sending only the first leaves the second on its initial value, which
// is hard-coded green: a Sense that never hears about lights-off glows "ideal"
// all night regardless of the room.

// DeviceSample is one reading as the Sense uploaded it, before any conversion.
//
// Raw wire values rather than calibrated ones, because the calibration differs
// per sensor and doing it at the call site is how the light bug below happened
// in the reference.
type DeviceSample struct {
	Temperature int32
	Humidity    int32
	// Light is the raw count. It is NOT lux: see Conditions.
	Light int32
	// DustMax, not the mean dust column. ReceiveResource passes
	// data.getDustMax() where the app path passes the average, so the LED reacts
	// to the worst minute rather than the typical one.
	DustMax                int32
	AudioPeakBackgroundDB  int32
	AudioPeakDisturbanceDB int32
}

// Conditions returns the lights-on and lights-off conditions for one reading.
//
// The rule is the reference's getGeneralRoomConditionV2, and it is much
// stricter than the legacy scoring it replaced. Legacy took temperature,
// humidity and dust, weighted them into a percentage and tolerated one bad
// reading. This is a hard any-one-fails over temperature, humidity, light and
// sound, plus dust when the device has been calibrated. One warning anywhere
// makes the whole room a warning.
//
// The lights-off variant forces light to IDEAL rather than dropping it, which
// is getRoomConditionV2LightOff: a bright room at bedtime should not turn the
// LED red, because the LED is the thing lighting it.
//
// dustOffset is nil for a device that has never been calibrated, and then dust
// is BOTH left unadjusted and excluded from the judgement. The reference splits
// those two decisions across a feature flag and a table lookup; orb has neither,
// so the single question "do we know this sensor's offset" answers both.
//
// # The light divergence
//
// The reference has a bug here and orb does not reproduce it. Its
// CurrentRoomState.fromRawData calibrates temperature, humidity, dust and audio
// through DataUtils, and then passes the light count STRAIGHT THROUGH to a
// classifier whose thresholds are in lux. DataUtils.convertLightCountsToLux
// exists and is simply never called. With the alert threshold at 8 lux and raw
// counts in the thousands, the reference's lights-on condition is ALERT
// essentially always, day and night.
//
// It went unnoticed for the obvious reason: the legacy scoring that ran for
// years ignored light entirely, so the bug only becomes visible the moment
// new_room_condition is turned on. Verified on real hardware on 2026-08-17,
// where a genuine 8.2 lux room and a raw count of 2156 both classify as ALERT
// and only one of them deserves to.
//
// So this converts, and the lights-ON condition will DISAGREE with suripu for
// as long as both are running. That is deliberate and is the second such
// divergence, after Air Quality always being shown; see CONSOLIDATION-PLAN.md.
// The lights-OFF condition is unaffected either way, because that path forces
// light to IDEAL before it is ever classified.
func Conditions(s DeviceSample, dustOffset *int32) (lightsOn, lightsOff string) {
	temperature := Classify(CalibratedTemperature(s.Temperature), TemperatureScale).Condition
	humidity := Classify(CalibratedHumidity(s.Temperature, s.Humidity), HumidityScale).Condition
	light := Classify(CalibratedLux(s.Light), LightScale).Condition
	sound := Classify(DeviceSound(s.AudioPeakBackgroundDB, s.AudioPeakDisturbanceDB), NoiseScale).Condition

	// Every modality that counts, in both variants. Dust joins only when the
	// device's offset is known.
	lit := []string{temperature, humidity, light, sound}
	unlit := []string{temperature, humidity, Ideal, sound}
	if dustOffset != nil {
		dust := Classify(CalibratedParticulates(s.DustMax, dustOffset), ParticulatesScale).Condition
		lit = append(lit, dust)
		unlit = append(unlit, dust)
	}

	return worst(lit), worst(unlit)
}

// worst reduces a set of per-sensor conditions to the one the LED shows.
//
// Any alert wins, then any warning. Not a count and not an average: the
// reference tallies both and then only ever compares the tallies against zero,
// so the tallies are just a longer way of writing this.
func worst(conditions []string) string {
	result := Ideal
	for _, c := range conditions {
		if c == Alert {
			return Alert
		}
		if c == Warning {
			result = Warning
		}
	}
	return result
}
