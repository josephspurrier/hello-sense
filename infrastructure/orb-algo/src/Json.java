import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;

import java.util.ArrayList;
import java.util.List;

/**
 * The wire contract with orb. Field names mirror the Go structs in
 * orb/internal/timeline exactly; a rename on either side breaks scoring
 * silently, since an absent field just decodes as null.
 *
 * Timestamps arrive as RFC3339 strings and are converted to epoch millis on
 * construction, because everything downstream works in millis. They stay in
 * real UTC from here on; see the class comment on Mapping for why nothing in
 * this path shifts them to local time.
 */
public final class Json {

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static final class Request {
        @JsonProperty("account_id") public long accountId;
        @JsonProperty("date")       public String date;
        @JsonProperty("offset_ms")  public int offsetMs;
        @JsonProperty("sensors")    public List<Sensor> sensors = new ArrayList<>();
        @JsonProperty("motion")     public List<Motion> motion = new ArrayList<>();
        @JsonProperty("feedback")   public List<Feedback> feedback = new ArrayList<>();
        // Go marshals []byte as base64, which Jackson decodes into byte[].
        @JsonProperty("prior_model") public byte[] priorModel;
        @JsonProperty("scratchpad")  public byte[] scratchpad;

        // Restricts the chain to one algorithm. Absent means the real chain,
        // which is what orb always sends. This exists because the fallback is
        // only reached when the one above it fails, so without it VOTING is
        // exercised only by luck, and the first time it runs for real is a
        // night nobody is watching.
        @JsonProperty("algorithm") public String algorithm;

        // Age in whole years, for the sleep duration score's age-specific
        // recommendation. Sent rather than derived because the birthdate lives
        // in orb's accounts table and this service has no database.
        //
        // Zero means unknown, which the duration score treats as an adult: the
        // age band is only consulted below 18.
        @JsonProperty("age") public int age;

        // The Sense's factory dust calibration, null when the device has never
        // been calibrated.
        //
        // Sent because this service has no database. Without it the air quality
        // condition on the timeline is computed from uncalibrated counts and
        // reads high: on this deployment about 213 counts, enough to report
        // particulates as WARNING where the reference says IDEAL, and enough to
        // move the environment score and the night's score with it.
        //
        // Integer rather than int: an offset of ZERO is a real calibration that
        // derives a delta of +300, and is a different thing from no calibration
        // at all. A primitive would silently turn the second into the first.
        @JsonProperty("dust_offset") public Integer dustOffset;
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static final class Sensor {
        public long tsMillis;
        @JsonProperty("ts")
        public void setTs(final String ts) { this.tsMillis = org.joda.time.DateTime.parse(ts).getMillis(); }

        @JsonProperty("temperature")                public Integer temperature;
        @JsonProperty("humidity")                   public Integer humidity;
        @JsonProperty("light")                      public Integer light;
        @JsonProperty("light_variance")             public Integer lightVariance;
        @JsonProperty("air_quality_raw")            public Integer airQualityRaw;
        @JsonProperty("audio_peak_background_db")   public Integer audioPeakBackgroundDB;
        @JsonProperty("audio_peak_energy_db")       public Integer audioPeakEnergyDB;
        @JsonProperty("audio_peak_disturbances_db") public Integer audioPeakDisturbanceDB;
        @JsonProperty("audio_num_disturbances")     public Integer audioNumDisturbances;
        @JsonProperty("wave_count")                 public Integer waveCount;
        @JsonProperty("hold_count")                 public Integer holdCount;
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static final class Motion {
        public long tsMillis;
        @JsonProperty("ts")
        public void setTs(final String ts) { this.tsMillis = org.joda.time.DateTime.parse(ts).getMillis(); }

        @JsonProperty("svm_no_gravity")    public Long svmNoGravity;
        @JsonProperty("motion_range")      public Long motionRange;
        @JsonProperty("kickoff_counts")    public Integer kickoffCounts;
        @JsonProperty("on_duration_secs")  public Integer onDurationSecs;
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static final class Feedback {
        @JsonProperty("event_type") public int eventType;
        @JsonProperty("old_time")   public String oldTime;
        @JsonProperty("new_time")   public String newTime;

        // When the correction was made, in real UTC. OnlineHmm filters
        // feedback to the night's own window by this value, so it is what
        // decides whether a correction is learned from at all.
        public long createdMillis;
        @JsonProperty("created_at")
        public void setCreatedAt(final String ts) {
            this.createdMillis = org.joda.time.DateTime.parse(ts).getMillis();
        }
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static final class Result {
        @JsonProperty("algorithm") public String algorithm;
        @JsonProperty("status")    public String status;

        @JsonProperty("in_bed")      public String inBed;
        @JsonProperty("sleep")       public String sleep;
        @JsonProperty("wake_up")     public String wakeUp;
        @JsonProperty("out_of_bed")  public String outOfBed;

        @JsonProperty("updated_model")      public byte[] updatedModel;
        @JsonProperty("updated_scratchpad") public byte[] updatedScratchpad;

        // Everything below is computed from the night's raw samples, which is
        // the line the seam is drawn on: if it needs the samples it is computed
        // here, if it is presentation it is done in Go. See the DECISION
        // section of knowledgebase/CONSOLIDATION-PLAN.md.
        //
        // No message strings, no valid_actions, no condition bands. Those are
        // the app's wire contract and they change more often than the maths.
        @JsonProperty("segments") public List<Segment> segments = new ArrayList<>();

        // The per-sensor conditions behind the app's coloured dots. A list
        // rather than five fields because a sensor with no usable samples is
        // omitted entirely, and the app renders however many arrive.
        @JsonProperty("conditions") public List<Condition> conditions = new ArrayList<>();

        // Flat rather than a nested "stats" object, because orb already has
        // these exact fields on its Result and already persists them to
        // sleep_stats. A nested object would be tidier and would mean touching
        // the storage path to gain nothing.
        @JsonProperty("sleep_score")         public Integer sleepScore;
        @JsonProperty("sleep_duration_mins") public Integer totalSleepMins;
        @JsonProperty("sound_sleep_mins")    public Integer soundSleepMins;
        @JsonProperty("light_sleep_mins")    public Integer lightSleepMins;
        @JsonProperty("medium_sleep_mins")   public Integer mediumSleepMins;
        @JsonProperty("times_awake")         public Integer timesAwake;
        @JsonProperty("sleep_onset_mins")    public Integer timeToSleepMins;
        @JsonProperty("uninterrupted_mins")  public Integer uninterruptedMins;
    }

    /**
     * One row of the timeline as the algorithms produce it.
     *
     * `type` is suripu's Event.Type name (GOT_IN_BED, IN_BED, LIGHTS_OUT,
     * GENERIC_SOUND, ...). orb maps it to the app's event_type and decides what
     * sentence to show; the name is not itself the wire value.
     */
    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static final class Segment {
        @JsonProperty("ts")              public String ts;
        @JsonProperty("duration_millis") public long durationMillis;
        @JsonProperty("type")            public String type;
        @JsonProperty("sleep_depth")     public int sleepDepth;
        @JsonProperty("offset_ms")       public int offsetMillis;
        @JsonProperty("sleep_period")    public String sleepPeriod;
    }

    /**
     * One sensor's condition over the sleep period.
     *
     * `sensor` is suripu's Sensor name (temperature, humidity, particulates,
     * light, sound) and `condition` is IDEAL, WARNING, ALERT or UNKNOWN. Both
     * are sent as the vendor's own spelling; naming them for the app is orb's
     * job, as it is for every other value on this contract.
     */
    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static final class Condition {
        @JsonProperty("sensor")    public String sensor;
        @JsonProperty("condition") public String condition;
    }

    private Json() {}
}
