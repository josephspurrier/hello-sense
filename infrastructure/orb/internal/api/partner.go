package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// The partner link.
//
// The reference had no endpoint for this: a partner was whoever else was
// paired to your Sense. Here the link is explicit and lives on the account,
// so two people can share a bed while each keeps their own Sense in the room.
// Linking is symmetric: PUT from either side links both, DELETE from either
// side unlinks both.
//
// These are orb's own routes, not the app's. Nothing in the iOS app calls
// them; they exist for setup by hand:
//
//	curl -X PUT -H "Authorization: Bearer <token>" \
//	     -d '{"email":"partner@example.com"}' https://<host>/v1/account/partner

type partnerResponse struct {
	Partner *partnerBody `json:"partner"`
}

type partnerBody struct {
	AccountID int64  `json:"account_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
}

func (h *Handler) getPartner(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	h.writePartner(w, r, accountID)
}

func (h *Handler) putPartner(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	partnerID, found, err := h.store.AccountIDByEmail(r.Context(), strings.TrimSpace(body.Email))
	if err != nil {
		h.log.Error("partner lookup", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err := h.store.SetPartner(r.Context(), accountID, partnerID); err != nil {
		if errors.Is(err, store.ErrSamePartner) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		h.log.Error("set partner", "account", accountID, "partner", partnerID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.log.Info("partners linked", "account", accountID, "partner", partnerID)
	h.writePartner(w, r, accountID)
}

func (h *Handler) deletePartner(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := h.store.ClearPartner(r.Context(), accountID); err != nil {
		h.log.Error("clear partner", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.log.Info("partners unlinked", "account", accountID)
	writeJSON(w, http.StatusOK, partnerResponse{})
}

func (h *Handler) writePartner(w http.ResponseWriter, r *http.Request, accountID int64) {
	p, found, err := h.store.PartnerOf(r.Context(), accountID)
	if err != nil {
		h.log.Error("partner", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	out := partnerResponse{}
	if found {
		out.Partner = &partnerBody{AccountID: p.AccountID, Email: p.Email, Name: p.Name}
	}
	writeJSON(w, http.StatusOK, out)
}
