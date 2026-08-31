package api

import (
	"fmt"
	"net/http"

	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// Devices is the shape of GET /v2/devices.
//
// Taken from the running stack's actual response rather than from
// DeviceResource, because the two previous endpoints both had a field whose
// type or meaning the source did not make obvious.
type Devices struct {
	Senses []Sense `json:"senses"`
	Pills  []Pill  `json:"pills"`
}

type Sense struct {
	ID string `json:"id"`
	// Hex, lower case, no prefix: the Sense reports 4513 and the app is shown
	// "11a1". Sending the decimal renders a firmware version that exists
	// nowhere and matches no support article.
	FirmwareVersion string    `json:"firmware_version"`
	HWVersion       string    `json:"hw_version"`
	LastUpdated     int64     `json:"last_updated"`
	State           string    `json:"state"`
	Color           string    `json:"color"`
	WiFiInfo        *WiFiInfo `json:"wifi_info"`
}

type WiFiInfo struct {
	SSID        string `json:"ssid"`
	RSSI        int32  `json:"rssi"`
	Condition   string `json:"condition"`
	LastUpdated int64  `json:"last_updated"`
}

type Pill struct {
	ID string `json:"id"`
	// Decimal, unlike the Sense's. The two devices are not consistent with
	// each other and matching them to each other would be wrong for one.
	FirmwareVersion string `json:"firmware_version"`
	LastUpdated     int64  `json:"last_updated"`
	BatteryLevel    int32  `json:"battery_level"`
	BatteryType     string `json:"battery_type"`
	State           string `json:"state"`
	Color           string `json:"color"`
}

func (h *Handler) getDevices(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	senses, pills, err := h.store.DevicesFor(r.Context(), accountID)
	if err != nil {
		h.log.Error("devices", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Both lists are built empty rather than nil so they marshal as [] and not
	// null. An app that iterates a null list is an app that shows nothing and
	// says nothing about why.
	out := Devices{Senses: []Sense{}, Pills: []Pill{}}

	for _, s := range senses {
		var wifi *WiFiInfo
		if s.WiFiSSID != nil {
			wifi = &WiFiInfo{
				SSID:      *s.WiFiSSID,
				RSSI:      derefI32(s.WiFiRSSI),
				Condition: wifiCondition(derefI32(s.WiFiRSSI)),
				// The WiFi reading's own timestamp, not the Sense's. They are
				// different events: the Sense reports every minute, the WiFi
				// record changes only when the network does.
				LastUpdated: wifiSeen(s),
			}
		}
		out.Senses = append(out.Senses, Sense{
			ID:              s.DeviceID,
			FirmwareVersion: fmt.Sprintf("%x", s.FirmwareVersion),
			HWVersion:       readableHWVersion(s.HWVersion),
			LastUpdated:     s.LastSeenAt.UnixMilli(),
			State:           "NORMAL",
			Color:           "UNKNOWN",
			WiFiInfo:        wifi,
		})
	}

	for _, p := range pills {
		out.Pills = append(out.Pills, Pill{
			ID:              p.PillID,
			FirmwareVersion: fmt.Sprintf("%d", p.FirmwareVersion),
			LastUpdated:     p.LastSeenAt.UnixMilli(),
			BatteryLevel:    p.BatteryLevel,
			BatteryType:     "UNKNOWN",
			State:           "NORMAL",
			Color:           "BLUE",
		})
	}

	writeJSON(w, http.StatusOK, out)
}

// wifiCondition buckets signal strength the way the app expects to receive it.
// suripu's thresholds, from WifiInfo.Condition.
func wifiCondition(rssi int32) string {
	switch {
	case rssi == 0:
		return "GOOD"
	case rssi > -50:
		return "GOOD"
	case rssi > -70:
		return "FAIR"
	default:
		return "BAD"
	}
}

// wifiSeen falls back to last_seen_at only when no WiFi timestamp was ever
// recorded, which is true for a Sense paired since the migration.
func wifiSeen(s store.SenseRow) int64 {
	if s.WiFiUpdatedAt != nil {
		return s.WiFiUpdatedAt.UnixMilli()
	}
	return s.LastSeenAt.UnixMilli()
}

func derefI32(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

// AppStatsUnread is the shape of GET /v1/app/stats/unread.
type AppStatsUnread struct {
	HasUnreadInsights      bool `json:"has_unread_insights"`
	HasUnansweredQuestions bool `json:"has_unanswered_questions"`
}

func (h *Handler) getAppStatsUnread(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	unreadInsights, err := h.store.HasUnreadInsights(r.Context(), accountID)
	if err != nil {
		h.log.Error("app stats", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	unansweredQuestions, err := h.store.HasUnansweredQuestions(r.Context(), accountID)
	if err != nil {
		h.log.Error("app stats questions", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AppStatsUnread{
		HasUnreadInsights:      unreadInsights,
		HasUnansweredQuestions: unansweredQuestions,
	})
}

// readableHWVersion maps the raw X-Hello-Sense-HW value the device reported
// (recorded by the edge on ingest) to the string the iOS app switches on. The
// app gates its ENTIRE voice setup path on seeing "SENSE_WITH_VOICE" here;
// everything else, including an unreported version, is a plain "SENSE". The
// integers are the reference's HardwareVersion enum: 1 = SENSE_ONE, 4 =
// SENSE_ONE_FIVE (the voice unit).
func readableHWVersion(raw *string) string {
	if raw != nil && *raw == "4" {
		return "SENSE_WITH_VOICE"
	}
	return "SENSE"
}

// The device-removal endpoints behind the app's "Remove Sense/Pill" buttons,
// ported from suripu-app's DeviceResource. All three answer 204 No Content,
// and all three are idempotent: removing something already gone is a success,
// which is what lets the app retry a half-finished removal cleanly.
//
// {sense_id}/all is the factory-reset variant: it drops EVERY account's link
// to the Sense, not just the caller's, so a shared unit can be fully released.
// It is registered as a more specific pattern than {sense_id}, so net/http
// routes it first.

func (h *Handler) deleteSense(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	senseID := r.PathValue("sense_id")
	if err := h.store.UnpairSense(r.Context(), accountID, senseID); err != nil {
		h.log.Error("unpair sense", "account", accountID, "sense", senseID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.log.Info("unpaired sense", "account", accountID, "sense", senseID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deletePill(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	pillID := r.PathValue("pill_id")
	if err := h.store.UnpairPill(r.Context(), accountID, pillID); err != nil {
		h.log.Error("unpair pill", "account", accountID, "pill", pillID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.log.Info("unpaired pill", "account", accountID, "pill", pillID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteSenseAll(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	senseID := r.PathValue("sense_id")
	if err := h.store.UnlinkAllFromSense(r.Context(), senseID); err != nil {
		h.log.Error("factory reset sense", "account", accountID, "sense", senseID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.log.Info("unlinked all accounts from sense", "by_account", accountID, "sense", senseID)
	w.WriteHeader(http.StatusNoContent)
}
