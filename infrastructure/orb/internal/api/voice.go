package api

import (
	"encoding/json"
	"net/http"

	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// The app-facing voice endpoints for the Sense with Voice: the settings behind
// the Voice screen's volume/mute/primary controls, and the "what can I say"
// command catalog. Ported from suripu-app's DeviceResource voice methods and
// VoiceCommandsResource.
//
// These make the Voice screen render and its controls work. They are separate
// from the actual voice PIPELINE (the device streaming audio to speech.hello.is
// and getting a spoken answer), which is its own, larger surface.

// voiceSettings is the shape of GET/PATCH /v2/devices/sense/{id}/voice. The
// field names are the app's (SENSenseVoiceSettings): a JSON number volume, and
// two bools.
type voiceSettings struct {
	Volume    int32 `json:"volume"`
	IsPrimary bool  `json:"is_primary_user"`
	Muted     bool  `json:"muted"`
}

func (h *Handler) getVoiceSettings(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	deviceID := r.PathValue("sense_id")

	v, found, err := h.store.VoiceSettings(r.Context(), accountID, deviceID)
	if err != nil {
		h.log.Error("voice settings", "account", accountID, "sense", deviceID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		// The reference's defaults: full volume, unmuted, not primary.
		v = voiceDefaults()
	}
	writeJSON(w, http.StatusOK, voiceSettings{
		Volume: v.Volume, Muted: v.Muted, IsPrimary: v.IsPrimary})
}

func (h *Handler) patchVoiceSettings(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	deviceID := r.PathValue("sense_id")

	// Pointers so a PATCH that carries only one field leaves the rest alone,
	// which is exactly how the app sends volume, mute and primary separately.
	var body struct {
		Volume    *int32 `json:"volume"`
		IsPrimary *bool  `json:"is_primary_user"`
		Muted     *bool  `json:"muted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid voice settings."})
		return
	}

	v, err := h.store.PutVoiceSettings(r.Context(), accountID, deviceID,
		body.Volume, body.Muted, body.IsPrimary)
	if err != nil {
		h.log.Error("put voice settings", "account", accountID, "sense", deviceID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, voiceSettings{
		Volume: v.Volume, Muted: v.Muted, IsPrimary: v.IsPrimary})
}

func voiceDefaults() store.VoiceSettingsRow { return store.VoiceSettingsRow{Volume: 100} }

// getVoiceCommands answers GET /v2/voice/commands with the catalog the Voice
// screen renders as "what can I say". The topics mirror the command handlers
// the voice pipeline implements; a phrase listed here is one the device is
// meant to understand.
func (h *Handler) getVoiceCommands(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, voiceCommandCatalog)
}

// getSpeechOnboarding answers GET /v1/speech/onboarding. The reference returned
// the commands a person had recently spoken, used only to personalize the
// onboarding tutorial. Nothing here has a speech history yet, so it is an empty
// list, which the tutorial handles.
func (h *Handler) getSpeechOnboarding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}
