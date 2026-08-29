package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// Account is the shape of GET /v1/account.
//
// Field names and types are suripu's, from com.hello.suripu.core.models.Account.
// They are not ours to tidy: the iOS app being replied to is a fixed binary
// from 2016 that will not be rebuilt, so a renamed field is a broken screen
// with no error anywhere.
//
// Times are epoch milliseconds. `dob` is a date but travels as millis too.
// Every field below was corrected by cmd/apidiff against the running stack.
// Four of them were wrong after reading Account.java carefully, which is the
// argument for the harness in one place:
//
//	id            the EXTERNAL uuid, not the primary key. The app never sees
//	              the integer.
//	dob           an ISO date string "1988-04-17", not epoch millis, even
//	              though `created` and `last_modified` beside it are millis.
//	firstname     the whole stored name. suripu keeps firstname/lastname as
//	              separate columns and this account has everything in the
//	              first, so splitting on a space produced "Orb" not "Orb Owner".
//	email_verified false. Nothing in this deployment verifies an email, and
//	              reporting true was an assumption about what "should" be.
type Account struct {
	ID            string  `json:"id"`
	ExtID         string  `json:"ext_id"`
	Email         string  `json:"email"`
	TZ            int32   `json:"tz"` // offset in milliseconds
	Name          string  `json:"name"`
	FirstName     *string `json:"firstname"`
	LastName      *string `json:"lastname"`
	Gender        string  `json:"gender"`
	GenderOther   string  `json:"gender_other"`
	Height        *int32  `json:"height"` // centimetres
	Weight        *int32  `json:"weight"` // grams
	Created       int64   `json:"created"`
	LastModified  int64   `json:"last_modified"`
	DOB           *string `json:"dob"`
	EmailVerified bool    `json:"email_verified"`
	ProfilePhoto  *string `json:"profile_photo"`
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	a, err := h.store.Account(r.Context(), accountID)
	if err != nil {
		h.log.Error("account", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, renderAccount(a))
}

// renderAccount is shared by the read and the write, so an edited profile comes
// back in exactly the shape a fetched one does. Two renderers would disagree the
// first time either changed, and the app would show one thing on save and
// another on refresh.
func renderAccount(a store.AccountRow) Account {
	var dob *string
	if a.Birthdate != nil {
		s := a.Birthdate.Format("2006-01-02")
		dob = &s
	}

	return Account{
		ID:            a.ExternalID,
		ExtID:         a.ExternalID,
		Email:         a.Email,
		TZ:            a.TZOffsetMS,
		Name:          a.Name,
		FirstName:     a.FirstName,
		LastName:      a.LastName,
		Gender:        a.Gender,
		GenderOther:   a.GenderOther,
		Height:        a.HeightCM,
		Weight:        a.WeightGrams,
		Created:       a.CreatedAt.UnixMilli(),
		LastModified:  a.LastModified.UnixMilli(),
		DOB:           dob,
		EmailVerified: false,
		ProfilePhoto:  nil,
	}
}

// Timezone is the shape of GET /v1/timezone.
//
// `timezone_offset`, not the obvious `offset_millis`, plus the zone id. Both
// halves were guessed wrong on the first attempt in different directions: the
// offset key was invented, and then the zone was dropped on the assumption it
// was not sent. It is.
type Timezone struct {
	OffsetMillis int32  `json:"timezone_offset"`
	ZoneID       string `json:"timezone_id"`
}

func (h *Handler) getTimezone(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// The offset in force now, not the account's stored default: the two differ
	// across a DST change and the app uses this to label times it renders.
	offset, zone, err := h.store.TimezoneAt(r.Context(), accountID, time.Now().UTC())
	if err != nil {
		h.log.Error("timezone", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, Timezone{OffsetMillis: offset, ZoneID: zone})
}

// accountEdit is the profile object the app sends back after editing.
//
// It sends the WHOLE account it last read, not a patch, which is what makes
// last_modified meaningful: it is the version the edit was made against.
type accountEdit struct {
	Name         string  `json:"name"`
	FirstName    *string `json:"firstname"`
	LastName     *string `json:"lastname"`
	Gender       string  `json:"gender"`
	GenderOther  string  `json:"gender_other"`
	Height       *int32  `json:"height"`
	Weight       *int32  `json:"weight"`
	DOB          *string `json:"dob"`
	TZ           int32   `json:"tz"`
	LastModified int64   `json:"last_modified"`
}

// putAccount applies a profile edit.
//
// Guarded by last_modified, and a mismatch is 412 rather than a silent no-op.
// The app sends the entire account object it last read, so without the guard a
// second phone editing the same profile would overwrite the first one's changes
// with values it read before they existed. The app's own recovery is to re-read
// and retry, which only works if it is told.
func (h *Handler) putAccount(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body accountEdit
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid account."})
		return
	}

	u := store.AccountUpdate{
		Name: body.Name, FirstName: body.FirstName, LastName: body.LastName,
		Gender: body.Gender, GenderOther: body.GenderOther,
		HeightCM: body.Height, WeightGrams: body.Weight, TZOffsetMS: body.TZ,
	}
	if body.DOB != nil && *body.DOB != "" {
		d, err := time.Parse(time.DateOnly, *body.DOB)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code: http.StatusBadRequest, Message: "Invalid date of birth."})
			return
		}
		u.Birthdate = &d
	}

	a, err := h.store.UpdateAccount(r.Context(), accountID, body.LastModified, u)
	if errors.Is(err, store.ErrStaleAccount) {
		writeJSON(w, http.StatusPreconditionFailed, errorBody{
			Code: http.StatusPreconditionFailed, Message: "pre condition failed"})
		return
	}
	if err != nil {
		h.log.Error("update account", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, renderAccount(a))
}
