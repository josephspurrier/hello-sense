// Package edge serves the Sense device.
//
// It replaces suripu-service (ingest), hello-time (clock sync) and messeji
// (command long-poll) with one handler set, and is written to sit behind a TLS
// terminator rather than terminating TLS itself. That is not a preference: Go's
// crypto/tls cannot complete this device's handshake, because the Sense offers
// only ECDHE-RSA-AES256-SHA and sends no supported_groups extension, so Go
// eliminates the only cipher on offer. See
// knowledgebase/CONSOLIDATION-PLAN.md, phase 0.
package edge

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/josephspurrier/hello-orb/orb/internal/alarm"
	"github.com/josephspurrier/hello-orb/orb/internal/ota"
	pbble "github.com/josephspurrier/hello-orb/orb/internal/pb/ble"
	pbdev "github.com/josephspurrier/hello-orb/orb/internal/pb/device"
	pbfile "github.com/josephspurrier/hello-orb/orb/internal/pb/filesync"
	pbntp "github.com/josephspurrier/hello-orb/orb/internal/pb/ntp"
	pbstate "github.com/josephspurrier/hello-orb/orb/internal/pb/state"
	"github.com/josephspurrier/hello-orb/orb/internal/pb/syncresp"
	"github.com/josephspurrier/hello-orb/orb/internal/pill"
	"github.com/josephspurrier/hello-orb/orb/internal/roomstate"
	"github.com/josephspurrier/hello-orb/orb/internal/sense"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// ntpEpochOffset is seconds between 1900-01-01 and 1970-01-01.
//
// The Sense expects NTP-style timestamps. Feeding it Unix seconds puts it
// exactly 70 years in the past (it reports 1956), and the workers then discard
// every sample as more than two hours out of sync. This constant is the whole
// reason time sync is a separate concern rather than "return time.Now()".
const ntpEpochOffset = 2208988800

// maxBody caps a request body. The largest legitimate upload is a batch of a
// few dozen minute-samples plus a 48-byte trailer, comfortably under 64KB.
const maxBody = 1 << 16

type Handler struct {
	store *store.Store
	log   *slog.Logger

	// ReadOnly runs the edge in shadow mode: everything is parsed, verified and
	// logged, but nothing is written and the device is never answered
	// authoritatively. Used to validate against live traffic before cutover.
	ReadOnly bool

	// FirmwareDir is where OTA images are served from. Empty means the
	// /firmware/ route 404s, which is the default and the right default: a
	// server that cannot hand out firmware cannot hand out the wrong firmware.
	FirmwareDir string

	// OTAPolicy is when updates may be offered and how long the device must
	// have been up first.
	//
	// Configurable only so that a supervised update can be run at a sane hour
	// and iterated on without a 20 minute wait between attempts. Neither default
	// is arbitrary: this device is somebody's alarm clock, so an update should
	// not start while they are asleep, and handing an image to something that
	// has just rebooted is how a recoverable fault becomes a brick. Loosen them
	// to watch one happen, then put them back.
	OTAPolicy ota.Policy
}

func New(s *store.Store, log *slog.Logger) *Handler {
	return &Handler{store: s, log: log, OTAPolicy: ota.DefaultPolicy}
}

// Routes dispatches on the Host header, matching how the device addresses three
// separate hostnames that all resolve here.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /in/sense/batch", h.senseBatch)
	mux.HandleFunc("POST /in/pill", h.pillBatch)
	mux.HandleFunc("POST /in/sense/state", h.senseState)
	mux.HandleFunc("POST /in/sense/files", h.senseFiles)
	mux.HandleFunc("POST /receive", h.receive)
	// Pairing, during onboarding: the phone drives Sense over BLE and Sense
	// forwards the pairing command here. /register/morpheus is the deprecated
	// alias for /register/sense that old firmware still calls.
	mux.HandleFunc("POST /register/sense", h.registerSense)
	mux.HandleFunc("POST /register/morpheus", h.registerSense)
	mux.HandleFunc("POST /register/pill", h.registerPill)
	// OTA images. Inert unless FirmwareDir is set; see firmware.go.
	mux.HandleFunc("GET /firmware/{name}", h.firmware)
	mux.HandleFunc("/", h.byHost)
	return mux
}

// hostLabel returns the first label of a Host header, without any port.
//
// "time.hello.is" and "time.orb.example.com" both give "time", which is what
// lets the same routing work whatever domain the firmware was built for.
func hostLabel(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	return host
}

// byHost handles the paths that are distinguished by hostname rather than path.
// hello-time exposes exactly one route at "/", so the device's own path is not
// meaningful there.
func (h *Handler) byHost(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	switch {
	// Matched on the FIRST LABEL, not the whole hello.is name. A firmware built
	// for a different domain asks for time.example.com, and the old substring
	// test silently failed to recognise that as a clock request.
	case hostLabel(host) == "time", hostLabel(host) == "ntp":
		h.timeSync(w, r)

	// A POST to "/" carrying a device id is a clock request whatever the Host
	// header says. hello-time's TimeResource was @Path("/") and never looked at
	// the host, so matching that is the faithful behaviour, not a workaround.
	//
	// It is also load-bearing in practice: sense_server.py rewrote the Host to
	// the upstream's address before forwarding, so orb saw "127.0.0.1:8081" and
	// 404'd every clock request the device made. hello-time had not cared, so
	// the breakage only appeared once orb took over that hostname. The device
	// then retried every 35 seconds for two hours, and the only sign was this
	// function's "unrouted device request" warning. Routing on the device id
	// means a proxy that mangles Host can no longer take the clock down.
	case r.Method == http.MethodPost && r.URL.Path == "/" &&
		r.Header.Get(senseIDHeader) != "":
		h.timeSync(w, r)
	case r.URL.Path == "/logs":
		// The device posts its own logs. Normally accepted and dropped: they
		// are useful only when actively debugging the firmware, and storing
		// them was a meaningful share of the old system's write volume.
		//
		// Under -debug they are printed instead, because they are the ONLY
		// account of what the device did with something we asked it to do. An
		// OTA that is offered, downloaded and then silently not applied leaves
		// no other trace: the server sees a successful GET and nothing more.
		h.deviceLogs(w, r)
	default:
		h.log.Warn("unrouted device request", "host", host, "path", r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// senseBatch ingests /in/sense/batch.
func (h *Handler) senseBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		h.fail(w, "read body", err, http.StatusBadRequest)
		return
	}

	payload, iv, sig, err := sense.ParseSigned(body)
	if err != nil {
		h.fail(w, "parse signed body", err, http.StatusBadRequest)
		return
	}

	var batch pbdev.BatchedPeriodicData
	if err := proto.Unmarshal(payload, &batch); err != nil {
		h.fail(w, "unmarshal batch", err, http.StatusBadRequest)
		return
	}

	deviceID := batch.GetDeviceId()
	dev, err := h.store.SenseByID(ctx, deviceID)
	if err != nil {
		// An unknown device is unauthenticated, so 401 rather than 404: saying
		// "no such device" to an unauthenticated caller confirms which ids exist.
		h.fail(w, "lookup device", err, http.StatusUnauthorized)
		return
	}

	if err := sense.Verify(dev.AESKey, payload, iv, sig); err != nil {
		h.fail(w, "verify signature", fmt.Errorf("device %s: %w", deviceID, err), http.StatusUnauthorized)
		return
	}

	now := time.Now().UTC()
	samples := make([]store.SensorSample, 0, len(batch.GetData()))
	warnedNoTZ := false
	for _, d := range batch.GetData() {
		ts := time.Unix(int64(d.GetUnixTime()), 0).UTC().Truncate(time.Minute)

		// A sample the clock cannot account for would put garbage in the middle
		// of a night, so drop it loudly rather than let it reach the
		// algorithms. A merely LATE sample is kept: see plausibleSampleTime.
		if !plausibleSampleTime(ts, now) {
			h.log.Warn("dropping sample with unsynced clock",
				"device", deviceID, "sample_ts", ts, "server_ts", now, "delta", ts.Sub(now))
			continue
		}

		// The offset in force when the sample was taken, not the account's
		// current one: nights either side of a DST change would otherwise shift
		// by an hour.
		offset, ok, err := h.store.OffsetMSAt(ctx, dev.AccountID, ts)
		if err != nil {
			h.fail(w, "timezone lookup", err, http.StatusInternalServerError)
			return
		}
		if !ok && !warnedNoTZ {
			// Once per batch, not once per sample: a missing zone affects the
			// whole upload and 60 identical lines help nobody.
			h.log.Warn("no timezone history for account; storing offset 0",
				"account", dev.AccountID, "at", ts)
			warnedNoTZ = true
		}

		samples = append(samples, store.SensorSample{
			DeviceID:      deviceID,
			TS:            ts,
			AccountID:     dev.AccountID,
			OffsetMS:      offset,
			Temperature:   i32p(d.Temperature),
			Humidity:      i32p(d.Humidity),
			Light:         i32p(d.Light),
			LightVariance: i32p(d.LightVariability),
			AirQualityRaw: i32p(d.Dust),

			// Audio is NOT stored raw. suripu converts on the way in
			// (DeviceData.Builder, DataUtils.convertAudioRawToDB then
			// floatToDbIntAudioDecibels), and the reader divides by 1000 again
			// to get decibels. Storing the wire value here would make every
			// audio reading read ~2.4% high forever, and would not match the
			// 20,000 rows already migrated.
			AudioPeakBackgroundDB:  i32ptr(audioRawToStored(d.GetAudioPeakBackgroundEnergyDb())),
			AudioPeakEnergyDB:      i32ptr(audioRawToStored(d.GetAudioPeakEnergyDb())),
			AudioPeakDisturbanceDB: i32ptr(audioRawToStored(d.GetAudioPeakDisturbanceEnergyDb())),

			// Absent counts are stored as 0, not NULL. suripu coerces with
			// `hasX() ? getX() : 0` (SenseProcessorUtils.java:155). The device
			// simply omits these when they are zero, so NULL here would mean
			// "unknown" for a value that is really "none".
			AudioNumDisturbances: i32ptr(d.GetAudioNumDisturbances()),
			WaveCount:            i32ptr(d.GetWaveCount()),
			HoldCount:            i32ptr(d.GetHoldCount()),
		})
	}

	if h.ReadOnly {
		h.log.Info("shadow: would ingest",
			"device", deviceID, "samples", len(samples),
			"firmware", batch.GetFirmwareVersion(), "ssid", batch.GetConnectedSsid())
	} else {
		written, err := h.store.InsertSensorSamples(ctx, samples)
		if err != nil {
			h.fail(w, "insert samples", err, http.StatusInternalServerError)
			return
		}
		fw := int32(batch.GetFirmwareVersion())
		if err := h.store.TouchSense(ctx, deviceID, now, &fw, batch.GetConnectedSsid()); err != nil {
			// Liveness is not worth failing an ingest over.
			h.log.Warn("touch sense failed", "device", deviceID, "err", err)
		}
		h.log.Info("ingested", "device", deviceID, "received", len(samples), "written", written)
	}

	// The device needs a signed reply. It is where it learns when to ring, what
	// colour to glow, and, if one has been deliberately armed, where to fetch
	// new firmware.
	//
	// The LAST reading in the batch, not the first and not the mean: the
	// reference judges the room on `i == batch.getDataCount() - 1`, and the
	// freshest minute is the one the LED should be answering for. Nil for an
	// empty batch, which the sync response handles by leaving the colour alone.
	var latest *pbdev.PeriodicData
	if data := batch.GetData(); len(data) > 0 {
		latest = data[len(data)-1]
	}
	h.respondSigned(w, dev.AESKey, h.syncResponse(ctx, dev,
		int32(batch.GetFirmwareVersion()), uptimeOf(&batch), latest))
}

// pillBatch ingests /in/pill, the motion the Sense relays on the pill's behalf.
//
// Two keys are involved. The request as a whole is signed with the SENSE's key
// (the Sense is the one talking to us), while each motion payload inside is
// encrypted with the PILL's key. The Sense cannot read what it forwards.
func (h *Handler) pillBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		h.fail(w, "read body", err, http.StatusBadRequest)
		return
	}

	payload, iv, sig, err := sense.ParseSigned(body)
	if err != nil {
		h.fail(w, "parse signed body", err, http.StatusBadRequest)
		return
	}

	var batch pbble.BatchedPillData
	if err := proto.Unmarshal(payload, &batch); err != nil {
		h.fail(w, "unmarshal pill batch", err, http.StatusBadRequest)
		return
	}

	// device_id on the batch is the SENSE that relayed it.
	senseID := batch.GetDeviceId()
	dev, err := h.store.SenseByID(ctx, senseID)
	if err != nil {
		h.fail(w, "lookup relaying sense", err, http.StatusUnauthorized)
		return
	}
	if err := sense.Verify(dev.AESKey, payload, iv, sig); err != nil {
		h.fail(w, "verify signature", fmt.Errorf("sense %s: %w", senseID, err), http.StatusUnauthorized)
		return
	}

	now := time.Now().UTC()
	var samples []store.PillSample
	var decoded, skipped int

	for _, pd := range batch.GetPills() {
		pillID := pd.GetDeviceId()
		p, err := h.store.PillByID(ctx, pillID)
		if err != nil {
			h.log.Warn("pill not paired, dropping motion", "pill", pillID, "err", err)
			skipped++
			continue
		}
		// A paired pill whose key has not been recovered yet cannot be read.
		// Never fall back to a default key: it would silently produce motion
		// values that are noise, and noise in the middle of a night is worse
		// than a gap.
		if len(p.AESKey) == 0 {
			h.log.Warn("pill has no key, cannot decrypt", "pill", pillID)
			skipped++
			continue
		}

		ts := time.Unix(int64(pd.GetTimestamp()), 0).UTC().Truncate(time.Minute)
		if !plausibleSampleTime(ts, now) {
			h.log.Warn("dropping pill sample with unsynced clock",
				"pill", pillID, "sample_ts", ts, "server_ts", now, "delta", ts.Sub(now))
			skipped++
			continue
		}

		offset, ok, err := h.store.OffsetMSAt(ctx, p.AccountID, ts)
		if err != nil {
			h.fail(w, "timezone lookup", err, http.StatusInternalServerError)
			return
		}
		if !ok {
			h.log.Warn("no timezone history for account; storing offset 0",
				"account", p.AccountID, "at", ts)
		}

		enc := pd.GetMotionDataEntrypted()
		if len(enc) == 0 {
			// A heartbeat with no motion. Still worth recording liveness.
			h.touchPill(ctx, pillID, now, pd)
			continue
		}

		plain, err := pill.Decrypt(p.AESKey, enc)
		if err != nil {
			h.log.Warn("pill decrypt failed", "pill", pillID, "err", err)
			skipped++
			continue
		}
		// Field 5 is the PAYLOAD FORMAT version. suripu-api calls it
		// firmware_version and the newer proto calls it protocol_version; the
		// pill's actual firmware build is a different field entirely.
		m, err := pill.Decode(pd.GetProtocolVersion(), plain)
		if err != nil {
			h.log.Warn("pill decode failed", "pill", pillID,
				"version", pd.GetProtocolVersion(), "err", err)
			skipped++
			continue
		}

		samples = append(samples, store.PillSample{
			PillID:         pillID,
			TS:             ts,
			AccountID:      p.AccountID,
			OffsetMS:       offset,
			SVMNoGravity:   &m.SVMNoGravity,
			MotionRange:    &m.MotionRange,
			KickoffCounts:  i32ptr(int32(m.KickoffCounts)),
			OnDurationSecs: i32ptr(int32(m.OnDurationSecs)),
		})
		decoded++
		h.touchPill(ctx, pillID, now, pd)
	}

	if h.ReadOnly {
		h.log.Info("shadow: would ingest pill", "sense", senseID, "decoded", decoded, "skipped", skipped)
	} else {
		written, err := h.store.InsertPillSamples(ctx, samples)
		if err != nil {
			h.fail(w, "insert pill samples", err, http.StatusInternalServerError)
			return
		}
		h.log.Info("ingested pill", "sense", senseID,
			"decoded", decoded, "written", written, "skipped", skipped)
	}

	h.respondSigned(w, dev.AESKey, nil)
}

func (h *Handler) touchPill(ctx context.Context, pillID string, now time.Time, pd *pbble.PillData) {
	if h.ReadOnly {
		return
	}
	var battery, uptime *int32
	if pd.BatteryLevel != nil {
		battery = i32ptr(pd.GetBatteryLevel())
	}
	if pd.Uptime != nil {
		uptime = i32ptr(pd.GetUptime())
	}
	if err := h.store.TouchPill(ctx, pillID, now, battery, uptime); err != nil {
		h.log.Warn("touch pill failed", "pill", pillID, "err", err)
	}
}

// senseIDHeader carries the device id on requests whose body does not. suripu
// calls it HelloHttpHeader.SENSE_ID.
const senseIDHeader = "X-Hello-Sense-Id"

// authByHeader reads and verifies a request whose protobuf does not name the
// device, using the header instead. Returns the device and the verified payload.
func (h *Handler) authByHeader(r *http.Request) (store.Sense, []byte, error) {
	deviceID := r.Header.Get(senseIDHeader)
	if deviceID == "" {
		return store.Sense{}, nil, fmt.Errorf("missing %s", senseIDHeader)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return store.Sense{}, nil, err
	}
	payload, iv, sig, err := sense.ParseSigned(body)
	if err != nil {
		return store.Sense{}, nil, err
	}
	dev, err := h.store.SenseByID(r.Context(), deviceID)
	if err != nil {
		return store.Sense{}, nil, err
	}
	if err := sense.Verify(dev.AESKey, payload, iv, sig); err != nil {
		return store.Sense{}, nil, fmt.Errorf("device %s: %w", deviceID, err)
	}
	return dev, payload, nil
}

// senseState records what the device says about itself: whether audio is
// enabled, what it is playing. Stored as JSONB rather than columns because it
// is device housekeeping that no query joins on, and the shape has changed
// across firmware versions.
func (h *Handler) senseState(w http.ResponseWriter, r *http.Request) {
	dev, payload, err := h.authByHeader(r)
	if err != nil {
		h.fail(w, "sense state auth", err, http.StatusUnauthorized)
		return
	}

	var st pbstate.SenseState
	if err := proto.Unmarshal(payload, &st); err != nil {
		h.fail(w, "unmarshal sense state", err, http.StatusBadRequest)
		return
	}

	if h.ReadOnly {
		h.log.Info("shadow: would record sense state",
			"device", dev.DeviceID, "audio", st.GetAudioState() != nil)
	} else {
		blob, err := protojson.Marshal(&st)
		if err != nil {
			h.fail(w, "encode sense state", err, http.StatusInternalServerError)
			return
		}
		if err := h.store.SetSenseState(r.Context(), dev.DeviceID, blob); err != nil {
			h.fail(w, "store sense state", err, http.StatusInternalServerError)
			return
		}
		h.log.Info("recorded sense state", "device", dev.DeviceID)
	}

	// Echoing the state back is what suripu does: the device treats the reply
	// as the authoritative state to adopt. An empty reply here would be a
	// command to turn everything off.
	out, err := proto.Marshal(&st)
	if err != nil {
		h.fail(w, "marshal sense state", err, http.StatusInternalServerError)
		return
	}
	h.respondSigned(w, dev.AESKey, out)
}

// senseFiles receives the SD card manifest: which sleep tones and ringtones the
// device has, with SHA1s.
//
// It is answered with an empty manifest, meaning "no files to add or remove".
// Serving real file downloads would mean hosting firmware and audio, which is
// deliberately out of scope: a half-configured download path is how a device
// ends up with a corrupt SD card.
func (h *Handler) senseFiles(w http.ResponseWriter, r *http.Request) {
	dev, payload, err := h.authByHeader(r)
	if err != nil {
		h.fail(w, "sense files auth", err, http.StatusUnauthorized)
		return
	}

	var manifest pbfile.FileManifest
	if err := proto.Unmarshal(payload, &manifest); err != nil {
		h.fail(w, "unmarshal file manifest", err, http.StatusBadRequest)
		return
	}
	h.log.Info("file manifest",
		"device", dev.DeviceID,
		"files", len(manifest.GetFileInfo()),
		"query_delay_seconds", manifest.GetQueryDelay(),
		"shadow", h.ReadOnly)

	// The card's contents, one line per file, at DEBUG.
	//
	// This is the ONLY catalogue of what is actually on the SD card. The
	// server-side `file_info` table covers the 11 sleep tones and nothing else,
	// so the ringtones are recorded nowhere we hold. The device reports every
	// file with its SHA1 on every sync and orb used to count them and throw the
	// rest away.
	//
	// It matters because the audio is gone: the `hello-audio` bucket is empty,
	// and a content-hash sweep of all 135 repositories (24,631 blobs, including
	// unreachable objects) found none of the tones. The card is the only copy
	// left. When it is eventually read, this is the list to verify against, and
	// the SHA1s are what prove a recovered file is the real one.
	//
	// DEBUG, not INFO: it is 34 lines every few minutes, which would bury the
	// log. Turn it up deliberately, capture one manifest, turn it back down.
	if h.log.Enabled(r.Context(), slog.LevelDebug) {
		for _, f := range manifest.GetFileInfo() {
			d := f.GetDownloadInfo()
			h.log.Debug("file manifest entry",
				"device", dev.DeviceID,
				"path", d.GetSdCardPath(),
				"name", d.GetSdCardFilename(),
				"sha1", hex.EncodeToString(d.GetSha1()))
		}
	}

	// Reply with the same manifest minus any download instructions.
	reply := &pbfile.FileManifest{}
	out, err := proto.Marshal(reply)
	if err != nil {
		h.fail(w, "marshal file manifest", err, http.StatusInternalServerError)
		return
	}
	h.respondSigned(w, dev.AESKey, out)
}

// TimeResponse builds the signed reply to a clock request.
//
// Exported and key-taking rather than a handler, because of an unresolved
// problem: the NTP request carries no device id, and the reply must be signed
// with that device's key. hello-time solves this by looking the key up from the
// X-Hello-Sense-Id header. Until the edge does the same, callers supply the key
// and this stays a pure function, which also makes it directly testable.
func TimeResponse(key []byte, reqBody []byte, now time.Time) ([]byte, error) {
	payload, _, _, err := sense.ParseSigned(reqBody)
	if err != nil {
		return nil, err
	}
	var req pbntp.NTPDataPacket
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("edge: unmarshal ntp: %w", err)
	}

	// NTP timestamps are 64-bit fixed point: seconds since 1900 in the high
	// half, fraction in the low half.
	ts := toSigned64(uint64(now.UTC().Unix()+ntpEpochOffset) << 32)

	out, err := proto.Marshal(&pbntp.NTPDataPacket{
		ReferenceTs: &ts,
		ReceiveTs:   &ts,
		TransmitTs:  &ts,
		OriginTs:    proto.Int64(req.GetOriginTs()),
	})
	if err != nil {
		return nil, fmt.Errorf("edge: marshal ntp: %w", err)
	}
	return sense.Sign(key, out)
}

// timeSync answers the device's clock request.
//
// The NTP protobuf carries no device id, so the key comes from the
// X-Hello-Sense-Id header, exactly as hello-time's TimeResource does.
func (h *Handler) timeSync(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get(senseIDHeader)
	if deviceID == "" {
		h.fail(w, "time sync", errors.New("missing "+senseIDHeader), http.StatusBadRequest)
		return
	}
	dev, err := h.store.SenseByID(r.Context(), deviceID)
	if err != nil {
		h.fail(w, "time sync lookup", err, http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		h.fail(w, "read body", err, http.StatusBadRequest)
		return
	}

	signed, err := TimeResponse(dev.AESKey, body, time.Now())
	if err != nil {
		h.fail(w, "time response", err, http.StatusBadRequest)
		return
	}

	// Never serve a clock while shadowing. Two servers answering time sync
	// would race, and handing the device a clock is the one action here that
	// can corrupt a whole night if it is wrong.
	if h.ReadOnly {
		h.log.Info("shadow: would answer time sync", "device", deviceID, "bytes", len(signed))
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Logged at INFO, unlike the per-minute uploads, because it is rare (the
	// firmware syncs every three hours) and because it is the one reply that
	// can silently corrupt a whole night: the device stamps every sample it
	// uploads against the clock handed to it here. Two bugs have already shipped
	// in this arithmetic, and both were invisible until the data was wrong. The
	// decoded value is logged rather than the raw fixed-point field so that a
	// wrong year is readable at a glance.
	h.log.Info("time sync served",
		"device", deviceID,
		"utc", time.Now().UTC().Format(time.RFC3339))

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(signed)))
	w.WriteHeader(http.StatusOK)
	w.Write(signed)
}

// receive is the messeji long-poll: the device holds a request open waiting for
// a command. Replaces a Clojure service plus Redis with one query.
func (h *Handler) receive(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Hello-Sense-Id")
	if deviceID == "" {
		http.Error(w, "missing device id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Poll rather than LISTEN/NOTIFY: one device, a 10 second horizon, and a
	// query that hits a partial index. Notify machinery would be more moving
	// parts for no measurable gain.
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		payload, ok, err := h.store.NextMessage(ctx, deviceID)
		if err != nil {
			// The deadline expiring mid-query is the normal end of a poll that
			// found nothing, not a failure. Checking ctx.Err() rather than the
			// query error keeps a genuine database problem visible as a 500.
			if ctx.Err() != nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.fail(w, "next message", err, http.StatusInternalServerError)
			return
		}
		if ok {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(payload)
			return
		}
		select {
		case <-ctx.Done():
			// No message within the poll window is normal; the device
			// reconnects immediately.
			w.WriteHeader(http.StatusNoContent)
			return
		case <-tick.C:
		}
	}
}

// respondSigned writes headers and body in ONE write.
//
// The Sense reads a reply with a single recv() and only reads again if that
// first read filled its 2048-byte buffer. Writing headers and body separately
// puts them in separate TLS records, so the device decodes a body-less buffer
// and reports "signature validation fail". This is why the response is
// assembled before anything is written.
func (h *Handler) respondSigned(w http.ResponseWriter, key, payload []byte) {
	signed, err := sense.Sign(key, payload)
	if err != nil {
		h.fail(w, "sign response", err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(signed)))
	w.WriteHeader(http.StatusOK)
	w.Write(signed)
}

func (h *Handler) fail(w http.ResponseWriter, what string, err error, code int) {
	h.log.Error("edge: "+what, "err", err, "status", code)
	http.Error(w, http.StatusText(code), code)
}

func i32p(p *int32) *int32 { return p }

func i32ptr(v int32) *int32 { return &v }

// toSigned64 reinterprets a 64-bit NTP timestamp as the signed value the
// protobuf field carries, matching Java's TimeStamp.ntpValue().
//
// Go's uint64 -> int64 conversion preserves the bit pattern, so any present-day
// NTP timestamp (which has the top bit set) comes out NEGATIVE, exactly as it
// does in a Java long. That is the whole job.
//
// This used to subtract 1<<63 instead, which CLEARS the sign bit rather than
// reinterpreting it, producing a different number. The device decoded that as
// 1958, and since every sample it uploads is stamped against the clock it was
// handed, one bad reply corrupts a whole night. It survived review because it
// looks like a plausible range fix and because nothing decoded the result: the
// unit test only checked that the reply was signed. Verified against the live
// Java hello-time by cmd/timecheck; see TestTimeResponseDecodesToNow.
func toSigned64(v uint64) int64 {
	return int64(v)
}

// audioRawToStored converts a raw audio energy value from the device into the
// integer suripu stores, so orb and the existing 20,000 rows agree.
//
//	decibels = raw / 1024.0            DataUtils.convertAudioRawToDB
//	stored   = int(decibels * 1000)    DataUtils.floatToDbIntAudioDecibels
//
// The truncation is Java's (int) cast, not rounding. Readers divide by 1000
// again, so the stored unit is millidecibels. Verified against live traffic:
// raw 45369 -> 44305, byte for byte what the Java stack wrote for the same
// upload.
func audioRawToStored(raw int32) int32 {
	return int32(float32(raw) / 1024.0 * 1000.0)
}

// Bounds on how far a sample's own timestamp may sit from the server clock.
//
// ASYMMETRIC, and the asymmetry is the whole point. A device whose clock has
// not synced starts around 1970 and reads absurdly far in the past, so some
// lower bound is needed. But "far in the past" is also exactly what a healthy
// device produces after an outage: the Sense and the pill buffer readings while
// they cannot upload, then flush the backlog when service returns. A symmetric
// window cannot tell those apart and throws the backlog away with the garbage.
//
// Learned on 2026-08-16. LocalStack's Kinesis died at 23:40 local, the device
// got 500s for nine hours and buffered, and when it recovered it re-sent
// samples up to two and a half hours old. The old +/-2h window rejected them as
// "unsynced clock": 492 warnings covering the night. Most of those samples had
// already been stored on an earlier attempt so little was actually lost, but a
// longer outage would have discarded an entire night of good data.
//
// Ahead of the server is still tight: a sample from the future cannot be a
// backlog, so it is a broken clock. Behind the server is generous enough to
// cover any plausible outage while still rejecting a device that thinks it is
// 1970.
const (
	maxClockAhead  = 2 * time.Hour
	maxClockBehind = 7 * 24 * time.Hour
)

// plausibleSampleTime reports whether a sample's timestamp can be believed.
func plausibleSampleTime(ts, now time.Time) bool {
	delta := ts.Sub(now)
	return delta <= maxClockAhead && delta >= -maxClockBehind
}

// ringDuration is how long the Sense rings for. Two minutes, from the
// reference's default.
const ringDuration = 120 * time.Second

// syncResponse builds the device's instructions, which today means the next
// alarm.
//
// Returns nil on any failure. That is deliberate: nil means "nothing to do",
// and the alternative to a missing alarm is a malformed response that the
// device may reject outright, taking the ingest down with it. A failure here
// costs one missed ring; a failure to reply costs the connection.
// uptimeOf reads the device's reported uptime, or -1 when it did not report
// one. Unknown is not the same as zero: internal/ota refuses on unknown, and
// treating it as zero would refuse for the wrong reason.
func uptimeOf(batch *pbdev.BatchedPeriodicData) time.Duration {
	if batch == nil || batch.UptimeInSecond == nil {
		return -1
	}
	return time.Duration(batch.GetUptimeInSecond()) * time.Second
}

func (h *Handler) syncResponse(ctx context.Context, dev store.Sense, firmwareVersion int32,
	uptime time.Duration, latest *pbdev.PeriodicData) []byte {

	deviceID := dev.DeviceID
	loc, err := h.store.SenseZone(ctx, deviceID)
	if err != nil {
		h.log.Warn("device zone lookup failed", "device", deviceID, "err", err)
		return nil
	}

	// The alarm and the firmware offer are INDEPENDENT, and nesting them was a
	// real defect: an early return for "no alarms set" also skipped OTA, so a
	// device with no alarm could never be offered firmware. They share only the
	// zone and the response.
	alarmMsg, ack := h.nextAlarm(ctx, deviceID, loc)
	files := h.otaFiles(ctx, deviceID, firmwareVersion, uptime, loc)

	resp := &syncresp.SyncResponse{Files: files}
	if alarmMsg != nil {
		resp.Alarm = alarmMsg
		resp.RingTimeAck = ack
	}
	h.setRoomConditions(resp, dev, latest)

	// There is no longer a "nothing to say" case. The room condition is part of
	// every reply, exactly as it is in the reference, because the LED has to be
	// told the room is still fine as surely as it has to be told it is not.
	if resp.Alarm == nil && len(resp.Files) == 0 && resp.RoomConditions == nil {
		return nil
	}

	out, err := proto.Marshal(resp)
	if err != nil {
		h.log.Error("marshal sync response", "device", deviceID, "err", err)
		return nil
	}
	return out
}

// setRoomConditions fills in the two colours the Sense's LED uses.
//
// Both slots, always, when there is a reading to judge. The firmware holds them
// in `room_color[2]` and chooses by whether the room is lit, and both entries
// initialise to green, so a Sense that is sent only the lights-on colour glows
// "ideal" all night whatever the room is doing. That was the state of this
// device until 2026-08-17.
//
// Derived from the reading that just arrived rather than from the database: the
// reference computes it inside the ingest loop for the LAST sample in the batch
// (`i == batch.getDataCount() - 1`), and the device would otherwise be told
// about a room a query older than the one it just described.
//
// lights_off_threshold is NOT sent, and that is a deliberate 3-byte difference
// from the reference. It carries the calibration row's lights_out_delta, orb has
// no column for it, and the only value in play is 100, which is already the
// firmware's compiled-in default (`light_off_threshold` in commands.c). Storing
// a column to send a device the number it already has is not worth a migration.
// Worth revisiting only if a device ever ships with a different delta.
func (h *Handler) setRoomConditions(resp *syncresp.SyncResponse, dev store.Sense, latest *pbdev.PeriodicData) {
	if latest == nil {
		return
	}

	lightsOn, lightsOff := roomstate.Conditions(roomstate.DeviceSample{
		Temperature: latest.GetTemperature(),
		Humidity:    latest.GetHumidity(),
		Light:       latest.GetLight(),
		// DustMax, not Dust: the reference passes getDustMax() here while the
		// app's dial shows the mean, so the LED answers for the worst minute.
		DustMax:                latest.GetDustMax(),
		AudioPeakBackgroundDB:  latest.GetAudioPeakBackgroundEnergyDb(),
		AudioPeakDisturbanceDB: latest.GetAudioPeakDisturbanceEnergyDb(),
	}, dev.DustOffset)

	resp.RoomConditions = wireCondition(lightsOn)
	resp.RoomConditionsLightsOff = wireCondition(lightsOff)
}

// wireCondition maps a condition onto the protobuf enum.
//
// The enum's numbering is its own (IDEAL is 1, not 0) and deliberately has no
// zero value, so an unset field is distinguishable from an ideal room. An
// unrecognised condition returns nil rather than guessing, which leaves the
// field unset and the firmware on its previous colour: a wrong colour is worse
// than a stale one.
func wireCondition(c string) *syncresp.SyncResponse_RoomConditions {
	var v syncresp.SyncResponse_RoomConditions
	switch c {
	case roomstate.Ideal:
		v = syncresp.SyncResponse_IDEAL
	case roomstate.Warning:
		v = syncresp.SyncResponse_WARNING
	case roomstate.Alert:
		v = syncresp.SyncResponse_ALERT
	default:
		return nil
	}
	return &v
}

// nextAlarm builds the alarm portion of the response, or nil when there is
// nothing to ring.
func (h *Handler) nextAlarm(ctx context.Context, deviceID string, loc *time.Location) (*syncresp.SyncResponse_Alarm, *string) {
	alarms, err := h.store.AlarmsForSense(ctx, deviceID)
	if err != nil {
		h.log.Warn("alarm lookup failed", "device", deviceID, "err", err)
		return nil, nil
	}
	if len(alarms) == 0 {
		return nil, nil
	}

	templates := make([]alarm.Alarm, 0, len(alarms))
	for _, a := range alarms {
		templates = append(templates, alarm.Alarm{
			Enabled: a.Enabled, Repeated: a.Repeated,
			Hour: a.Hour, Minute: a.Minute, DayOfWeek: a.DayOfWeek,
			Year: a.Year, Month: a.Month, Day: a.Day, SoundID: a.SoundID,
			Smart: a.Smart,
		})
	}

	// now is read AFTER the queries, matching the reference's own warning that
	// the lookup can take a while. Reading it before would leave the countdown
	// below measuring from a moment that has already passed.
	now := time.Now()
	ring := alarm.Next(templates, now, loc)
	if ring == nil {
		return nil, nil
	}

	// A smart alarm may come forward if the sleeper is already surfacing. Only
	// inside the window, and only on evidence: see alarm.BringForward. The
	// motion query is skipped entirely outside the window so an ordinary sync
	// costs nothing extra.
	if ring.Smart && !now.Before(ring.At.Add(-alarm.SmartWindow)) {
		motion, err := h.store.RecentMotion(ctx, alarms[0].AccountID, now.Add(-alarm.SmartWindow), now)
		if err != nil {
			// Fall through to the alarm as set. A smart alarm that cannot read
			// motion is an ordinary alarm, which is the safe outcome.
			h.log.Warn("smart alarm motion lookup failed", "device", deviceID, "err", err)
		} else if early := alarm.BringForward(ring.At, now, motion); early.Before(ring.At) {
			h.log.Info("smart alarm brought forward", "device", deviceID,
				"set_for", ring.At.In(loc).Format(time.RFC3339),
				"ringing_at", early.In(loc).Format(time.RFC3339))
			ring.At = early
		}
	}

	// The countdown is measured from the UNFLOORED now, which is the trap the
	// reference documents. Matching floors now to the minute, correctly: an
	// alarm set for 07:00 should still fire at 07:00:30. But using that floored
	// value as the base of a countdown puts "now" up to 59 seconds in the past,
	// so the offset overstates the time remaining and the Sense rings late by
	// however far into the minute the request arrived.
	offset := int32(ring.At.Sub(now) / time.Second)
	if offset < 1 {
		// The lookup outlasted the alarm. Ring immediately rather than send a
		// negative offset, which the device would read as nonsense.
		offset = 1
	}

	sound := int32(ring.SoundID)
	path := alarm.SoundPath(ring.SoundID)
	start := uint32(ring.At.Unix())
	end := uint32(ring.At.Add(ringDuration).Unix())
	dur := int32(ringDuration / time.Second)
	ack := strconv.FormatInt(ring.At.UnixMilli(), 10)

	h.log.Info("next alarm", "device", deviceID,
		"at", ring.At.In(loc).Format(time.RFC3339), "in_seconds", offset, "sound", sound)

	return &syncresp.SyncResponse_Alarm{
		StartTime:                 &start,
		EndTime:                   &end,
		RingtoneId:                &sound,
		RingtonePath:              &path,
		RingDurationInSecond:      &dur,
		RingOffsetFromNowInSecond: &offset,
	}, &ack
}

// otaFiles returns the firmware to offer, which is almost always nothing.
//
// Nothing is offered unless a row in firmware_updates names this device AND has
// been armed. See internal/ota for the gates; every one of them can only
// refuse. A refusal is logged with its reason, because an update that quietly
// never happens is as confusing as one that quietly does.
func (h *Handler) otaFiles(ctx context.Context, deviceID string, firmwareVersion int32,
	uptime time.Duration, loc *time.Location) []*syncresp.SyncResponse_FileDownload {

	update, err := h.store.ArmedUpdateFor(ctx, deviceID)
	if err != nil {
		h.log.Error("firmware update lookup failed", "device", deviceID, "err", err)
		return nil
	}
	if update == nil {
		// The state every device is in unless somebody deliberately changed it.
		// Not logged: it is the normal case and would drown the log.
		return nil
	}

	// A device reporting the target version is a flash that worked, and it is
	// the only success signal there is.
	if done, err := h.store.CompleteUpdateIfReached(ctx, deviceID, firmwareVersion); err != nil {
		h.log.Error("completing firmware update failed", "device", deviceID, "err", err)
	} else if done {
		h.log.Info("firmware update completed", "device", deviceID, "version", firmwareVersion)
		return nil
	}

	d := ota.Decide(update, firmwareVersion, uptime, time.Now().In(loc), h.OTAPolicy)
	if !d.Offer {
		h.log.Info("firmware update not offered", "device", deviceID,
			"target", update.ToVersion, "reason", d.Reason)
		return nil
	}

	h.log.Warn("OFFERING FIRMWARE UPDATE", "device", deviceID,
		"from", firmwareVersion, "to", update.ToVersion,
		"bytes", update.FileSize, "host", update.Host)

	if err := h.store.RecordUpdateOffered(ctx, deviceID); err != nil {
		// Already decided to offer; a bookkeeping failure must not change that.
		h.log.Error("recording firmware offer failed", "device", deviceID, "err", err)
	}

	return []*syncresp.SyncResponse_FileDownload{{
		Host:                      &update.Host,
		Url:                       &update.URL,
		Sha1:                      update.SHA1,
		FileSize:                  &update.FileSize,
		CopyToSerialFlash:         &update.CopyToSerialFlash,
		ResetApplicationProcessor: &update.ResetApplicationProcessor,
		ResetNetworkProcessor:     &update.ResetNetworkProcessor,
		SerialFlashFilename:       strPtrOrNil(update.SerialFlashFilename),
		SerialFlashPath:           strPtrOrNil(update.SerialFlashPath),
		SdCardFilename:            strPtrOrNil(update.SDCardFilename),
		SdCardPath:                strPtrOrNil(update.SDCardPath),
	}}
}

// strPtrOrNil omits an empty optional rather than sending "". The device reads
// these as paths, and an empty path is not the same as no path.
func strPtrOrNil(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
