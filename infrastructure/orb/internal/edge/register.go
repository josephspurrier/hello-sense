package edge

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"

	"github.com/josephspurrier/hello-orb/orb/internal/pb/ble"
	"github.com/josephspurrier/hello-orb/orb/internal/sense"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// POST /register/sense and /register/pill: the pairing endpoints, ported from
// suripu-service's RegisterResource. They are DEVICE endpoints: the phone
// talks BLE to Sense during onboarding, Sense forwards the MorpheusCommand
// here inside its usual signed envelope, and the reply goes back over BLE to
// the phone. The command carries the phone's own OAuth token as accountId,
// which is how a pairing binds a device to the person holding the phone
// rather than to whoever owned the Sense first.
//
// Everything the caller learns arrives as a SIGNED MorpheusCommand, errors
// included, because the device relays the body to the app and drops anything
// it cannot verify. Plain HTTP errors are reserved for the cases the
// reference used them: a body that will not parse, or a device whose key is
// unknown, where there is nothing to sign with.

// pairAction distinguishes the two endpoints inside the shared flow.
type pairAction int

const (
	pairSense pairAction = iota
	pairPill
)

func (h *Handler) registerSense(w http.ResponseWriter, r *http.Request) {
	h.register(w, r, pairSense)
}

func (h *Handler) registerPill(w http.ResponseWriter, r *http.Request) {
	h.register(w, r, pairPill)
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request, action pairAction) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		h.fail(w, "read register body", err, http.StatusBadRequest)
		return
	}

	payload, iv, sig, err := sense.ParseSigned(body)
	if err != nil {
		h.fail(w, "parse signed register", err, http.StatusBadRequest)
		return
	}
	var cmd ble.MorpheusCommand
	if err := proto.Unmarshal(payload, &cmd); err != nil {
		h.fail(w, "unmarshal morpheus command", err, http.StatusBadRequest)
		return
	}

	deviceID := cmd.GetDeviceId()
	token := cmd.GetAccountId()

	// The response skeleton. deviceId echoes the request; accountId carries
	// the token back for the sense flow and is cleared for the pill flow,
	// both straight from the reference (old firmware overflows a buffer on a
	// pill response that includes it).
	resp := &ble.MorpheusCommand{
		Version:  proto.Int32(0),
		DeviceId: proto.String(deviceID),
	}
	setErr := func(e ble.ErrorType) {
		resp.Type = ble.MorpheusCommand_MORPHEUS_COMMAND_ERROR.Enum()
		resp.Error = e.Enum()
	}

	// Which Sense signs this exchange. Pairing a Sense: the device id in the
	// command IS the Sense. Pairing a pill: the device id is the pill, and
	// the Sense is named by its own id header, falling back to the account's
	// paired Sense as the reference does for old firmware.
	senseID := deviceID
	accountID, tokenErr := h.store.AccountByWireToken(ctx, token)
	if action == pairPill {
		senseID = r.Header.Get(senseIDHeader)
		if senseID == "" && tokenErr == nil {
			senseID, err = h.store.ActiveSenseID(ctx, accountID)
			if err != nil {
				h.fail(w, "resolve sense for pill", err, http.StatusInternalServerError)
				return
			}
		}
		if senseID == "" {
			// No header and no paired Sense: nothing to validate or sign with.
			h.fail(w, "register pill without a sense",
				fmt.Errorf("account %d", accountID), http.StatusBadRequest)
			return
		}
	}

	key, found, err := h.store.SenseKey(ctx, senseID)
	if err != nil {
		h.fail(w, "sense key", err, http.StatusInternalServerError)
		return
	}
	if !found {
		h.fail(w, "register with unknown sense",
			fmt.Errorf("sense %s", senseID), http.StatusUnauthorized)
		return
	}
	if err := sense.Verify(key, payload, iv, sig); err != nil {
		h.fail(w, "verify register signature",
			fmt.Errorf("sense %s: %w", senseID, err), http.StatusUnauthorized)
		return
	}

	// From here every outcome is a signed response.
	switch {
	case tokenErr != nil && errors.Is(tokenErr, store.ErrNoToken):
		// The phone's token was bad. The reference calls this
		// INTERNAL_OPERATION_FAILED rather than anything more specific, and
		// the app maps it to a retryable pairing error.
		h.log.Warn("register with unusable token", "sense", senseID)
		setErr(ble.ErrorType_INTERNAL_OPERATION_FAILED)
	case tokenErr != nil:
		h.log.Error("register token lookup", "err", tokenErr)
		setErr(ble.ErrorType_INTERNAL_OPERATION_FAILED)
	case action == pairSense && cmd.GetType() != ble.MorpheusCommand_MORPHEUS_COMMAND_PAIR_SENSE,
		action == pairPill && cmd.GetType() != ble.MorpheusCommand_MORPHEUS_COMMAND_PAIR_PILL:
		h.log.Warn("register with wrong command type",
			"sense", senseID, "type", cmd.GetType().String())
		setErr(ble.ErrorType_INTERNAL_DATA_ERROR)
	case action == pairSense:
		resp.AccountId = proto.String(token)
		outcome, err := h.store.PairSense(ctx, accountID, deviceID)
		h.finishPairing(resp, setErr, outcome, err,
			"paired sense", "sense", deviceID, accountID)
	default: // pairPill
		outcome, err := h.store.PairPill(ctx, accountID, deviceID)
		h.finishPairing(resp, setErr, outcome, err,
			"paired pill", "pill", deviceID, accountID)
	}

	// A success left Type unset; it becomes the echo of the request the
	// firmware expects. Anything that went wrong already set ERROR.
	if resp.Type == nil {
		if action == pairSense {
			resp.Type = ble.MorpheusCommand_MORPHEUS_COMMAND_PAIR_SENSE.Enum()
		} else {
			resp.Type = ble.MorpheusCommand_MORPHEUS_COMMAND_PAIR_PILL.Enum()
		}
	}

	out, err := proto.Marshal(resp)
	if err != nil {
		h.fail(w, "marshal register response", err, http.StatusInternalServerError)
		return
	}
	signed, err := sense.Sign(key, out)
	if err != nil {
		h.fail(w, "sign register response", err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	// One write: the device reads the reply with a single recv().
	_, _ = w.Write(signed)
}

// finishPairing folds a store pairing outcome into the response. On success
// the command type is left nil for the caller to set to its PAIR_* value; on
// conflict or failure it becomes the error the reference sends.
func (h *Handler) finishPairing(resp *ble.MorpheusCommand, setErr func(ble.ErrorType),
	outcome store.PairOutcome, err error, msg, what, id string, accountID int64) {

	switch {
	case err != nil:
		h.log.Error("pairing failed", "kind", what, "id", id, "account", accountID, "err", err)
		setErr(ble.ErrorType_INTERNAL_OPERATION_FAILED)
	case outcome == store.PairConflict:
		h.log.Warn("pairing conflict", "kind", what, "id", id, "account", accountID)
		setErr(ble.ErrorType_DEVICE_ALREADY_PAIRED)
	case outcome == store.PairedAlready:
		h.log.Info(msg+" (already)", "kind", what, "id", id, "account", accountID)
	default:
		h.log.Info(msg, "kind", what, "id", id, "account", accountID)
	}
}
