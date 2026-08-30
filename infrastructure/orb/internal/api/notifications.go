package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// pushRegistration is the body the app posts to /v1/notifications/registration.
//
// The field names come from MobilePushRegistration in suripu-core, which is
// what the shipped app serialises. They are not ours to choose: the app is a
// build of the original source and sends what it sends.
type pushRegistration struct {
	OS         string `json:"os"`
	Version    string `json:"version"`
	AppVersion string `json:"app_version"`
	Token      string `json:"token"`
}

// maxDeviceTokenHexLen bounds what is accepted as a token.
//
// A classic APNS token is 32 bytes, 64 hex characters, but Apple has said the
// length is not guaranteed and newer tokens are longer. So this validates the
// shape (hex, sane length) rather than pinning an exact size, which would
// reject valid tokens the day Apple changes it and look like the app failing to
// register.
const maxDeviceTokenHexLen = 200

// validDeviceToken reports whether a token is plausibly an APNS device token.
//
// Worth checking despite the token being opaque to us: it goes into a URL path
// when sending, and an unvalidated value there is how a request ends up
// somewhere other than where it was meant to go.
func validDeviceToken(s string) bool {
	if s == "" || len(s) > maxDeviceTokenHexLen || len(s)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// normalizeDeviceToken lowercases the token so that the same installation
// registering twice in different cases is one row rather than two, which would
// otherwise send every notification to the same phone twice.
func normalizeDeviceToken(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (h *Handler) postNotificationRegistration(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body pushRegistration
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid body."})
		return
	}

	token := normalizeDeviceToken(body.Token)
	if !validDeviceToken(token) {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid device token."})
		return
	}

	if err := h.store.SavePushToken(r.Context(), accountID, token,
		body.OS, body.Version, body.AppVersion); err != nil {
		h.log.Error("save push token", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// The reference returns an empty 200 here and the app checks only the
	// status, so there is nothing to serialise.
	h.log.Info("push registered", "account", accountID, "app_version", body.AppVersion)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteNotificationRegistration(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body pushRegistration
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid body."})
		return
	}

	// Deleting is scoped to the caller's account, so naming another account's
	// token unregisters nothing.
	if err := h.store.DeletePushToken(r.Context(), accountID,
		normalizeDeviceToken(body.Token)); err != nil {
		h.log.Error("delete push token", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// The notification settings screen: GET/PUT /v1/notifications.
//
// The reference is MobilePushRegistrationResource + NotificationSettingsDAO:
// three fixed types, everything defaults to enabled with no rows stored, and
// the wire order is SLEEP_SCORE, SYSTEM, SLEEP_REMINDER
// (NotificationSettingsDynamoDB.ORDERING). The names ride along in the
// payload; the app renders them verbatim.

type notificationSchedule struct {
	Hour   int32 `json:"hour"`
	Minute int32 `json:"minute"`
}

type notificationSetting struct {
	Name     string                `json:"name"`
	Type     string                `json:"type"`
	Enabled  bool                  `json:"enabled"`
	Schedule *notificationSchedule `json:"schedule,omitempty"`
}

// notificationTypes is the full set, in the reference's wire order.
var notificationTypes = []struct{ t, name string }{
	{"SLEEP_SCORE", "Sleep Score"},
	{"SYSTEM", "System Alerts"},
	{"SLEEP_REMINDER", "Sleep Reminder"},
}

func notificationName(t string) string {
	for _, nt := range notificationTypes {
		if nt.t == t {
			return nt.name
		}
	}
	return ""
}

func (h *Handler) getNotificationSettings(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	stored, err := h.store.NotificationSettings(r.Context(), accountID)
	if err != nil {
		h.log.Error("notification settings", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	out := make([]notificationSetting, 0, len(notificationTypes))
	for _, nt := range notificationTypes {
		s := notificationSetting{Name: nt.name, Type: nt.t, Enabled: true}
		if row, found := stored[nt.t]; found {
			s.Enabled = row.Enabled
			if row.Hour != nil && row.Minute != nil {
				s.Schedule = &notificationSchedule{Hour: *row.Hour, Minute: *row.Minute}
			}
		}
		out = append(out, s)
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) putNotificationSettings(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body []struct {
		Type     string                `json:"type"`
		Enabled  bool                  `json:"enabled"`
		Schedule *notificationSchedule `json:"schedule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid settings."})
		return
	}

	rows := make([]store.NotificationSettingRow, 0, len(body))
	echo := make([]notificationSetting, 0, len(body))
	for _, s := range body {
		// Case-insensitive like the reference's Type.fromString; an unknown
		// type is a 400 there (the deserializer throws) and here.
		t := strings.ToUpper(s.Type)
		if notificationName(t) == "" {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code: http.StatusBadRequest, Message: "invalid notification type"})
			return
		}
		row := store.NotificationSettingRow{Type: t, Enabled: s.Enabled}
		if s.Schedule != nil {
			row.Hour, row.Minute = &s.Schedule.Hour, &s.Schedule.Minute
		}
		rows = append(rows, row)
		echo = append(echo, notificationSetting{
			Name: notificationName(t), Type: t, Enabled: s.Enabled, Schedule: s.Schedule})
	}

	if err := h.store.PutNotificationSettings(r.Context(), accountID, rows); err != nil {
		h.log.Error("put notification settings", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// The reference answers with the list it was sent, not a re-read; the
	// app treats the echo as confirmation.
	writeJSON(w, http.StatusOK, echo)
}
