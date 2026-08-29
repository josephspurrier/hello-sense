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
			HWVersion:       "SENSE",
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
