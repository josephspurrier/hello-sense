package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// The credential endpoints: POST /v1/account (registration), POST
// /v1/account/email and POST /v1/account/password. They arrived together and
// late, when the second Sleep Pill did: a partner needs an account of their
// own, and the sign-out/sign-in path the app reaches them through crosses
// DELETE /v1/oauth2/token (oauth.go) on the way.
//
// The validation error strings are a wire protocol, not log text. The app
// switches on the literal enum names in the response body
// (SENAPIAccount.errorForAPIResponseError) to pick which message the person
// sees, so PASSWORD_TOO_SHORT here must be spelled exactly as suripu's
// Registration.RegistrationError spells it.

// minPasswordLength and maxNameLength are Registration.java's constants.
const (
	minPasswordLength = 6
	maxNameLength     = 100
)

// bcryptCost matches PasswordUtil.encrypt's gensalt(12). The seeded account's
// hash and every migrated one are cost 12; minting at a different cost would
// work but would make the column lie about how it was produced.
const bcryptCost = 12

// commonPasswords is PasswordUtil's list, verbatim, all 100 entries. The
// reference rejects exactly these as PASSWORD_INSECURE and nothing else: no
// entropy rules, no variants, not even case folding. Tempting to improve, but
// the app's sign-up screen was written against exactly this behavior.
var commonPasswords = map[string]bool{
	"password": true, "123456": true, "12345678": true, "qwerty": true,
	"dragon": true, "baseball": true, "football": true, "letmein": true,
	"monkey": true, "696969": true, "abc123": true, "mustang": true,
	"michael": true, "shadow": true, "master": true, "jennifer": true,
	"111111": true, "jordan": true, "superman": true, "harley": true,
	"1234567": true, "fuckme": true, "hunter": true, "fuckyou": true,
	"trustno1": true, "ranger": true, "buster": true, "thomas": true,
	"tigger": true, "robert": true, "soccer": true, "batman": true,
	"killer": true, "hockey": true, "george": true, "charlie": true,
	"andrew": true, "michelle": true, "sunshine": true, "jessica": true,
	"asshole": true, "pepper": true, "daniel": true, "access": true,
	"123456789": true, "654321": true, "joshua": true, "maggie": true,
	"starwars": true, "silver": true, "william": true, "dallas": true,
	"yankees": true, "123123": true, "ashley": true, "666666": true,
	"amanda": true, "orange": true, "biteme": true, "freedom": true,
	"computer": true, "thunder": true, "nicole": true, "ginger": true,
	"heather": true, "hammer": true, "summer": true, "corvette": true,
	"taylor": true, "fucker": true, "austin": true, "merlin": true,
	"matthew": true, "121212": true, "golfer": true, "cheese": true,
	"princess": true, "martin": true, "chelsea": true, "patrick": true,
	"richard": true, "diamond": true, "yellow": true, "bigdog": true,
	"secret": true, "asdfgh": true, "sparky": true, "cowboy": true,
	"camaro": true, "anthony": true, "matrix": true, "falcon": true,
	"iloveyou": true, "bailey": true, "guitar": true, "jackson": true,
	"purple": true, "scooter": true, "phoenix": true, "aaaaaa": true,
}

// validatePassword returns the reference's error name, or "".
func validatePassword(password string) string {
	if len(password) < minPasswordLength {
		return "PASSWORD_TOO_SHORT"
	}
	if commonPasswords[password] {
		return "PASSWORD_INSECURE"
	}
	return ""
}

// validEmail approximates Apache's EmailValidator closely enough for this
// deployment: one @, something on each side, and a dot in the domain. The dot
// rule is load-bearing rather than pedantry: the reference rejects TLD-less
// addresses (user@localhost) with EMAIL_INVALID, and a local stack is exactly
// where someone would try one.
func validEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	if strings.ContainsAny(email, " \t\r\n") || strings.IndexByte(domain, '@') >= 0 {
		return false
	}
	if i := strings.IndexByte(domain, '.'); i <= 0 || i == len(domain)-1 {
		return false
	}
	return true
}

// registration is what the app POSTs to create an account: the SENAccount
// dictionary plus a password. The app sends firstname and no name; suripu's
// Registration keeps both and copies firstname into name when name is absent.
type registration struct {
	Name        *string `json:"name"`
	FirstName   *string `json:"firstname"`
	LastName    *string `json:"lastname"`
	Email       string  `json:"email"`
	Password    string  `json:"password"`
	Gender      string  `json:"gender"`
	GenderOther string  `json:"gender_other"`
	Height      *int32  `json:"height"`
	Weight      *int32  `json:"weight"`
	DOB         *string `json:"dob"`
	TZ          int32   `json:"tz"`
}

// postAccount creates an account. Unauthenticated by nature: it is the
// endpoint that makes the credentials everything else authenticates with.
//
// The validation order is Registration.validate's, because the app shows only
// the first failure and a different order would surface a different message
// than the reference for the same bad form.
func (h *Handler) postAccount(w http.ResponseWriter, r *http.Request) {
	var reg registration
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid registration."})
		return
	}

	badRequest := func(msg string) {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: msg})
	}

	switch {
	case reg.Name == nil && reg.FirstName == nil:
		badRequest("MISSING_FIRSTNAME_AND_NAME")
		return
	case reg.Name == nil && *reg.FirstName == "":
		badRequest("MISSING_FIRSTNAME")
		return
	case reg.Name != nil && len(*reg.Name) > maxNameLength:
		badRequest("NAME_TOO_LONG")
		return
	case reg.Name != nil && *reg.Name == "":
		badRequest("NAME_TOO_SHORT")
		return
	}
	if msg := validatePassword(reg.Password); msg != "" {
		badRequest(msg)
		return
	}
	email := strings.ToLower(strings.TrimSpace(reg.Email))
	if !validEmail(email) {
		badRequest("EMAIL_INVALID")
		return
	}

	var birthdate *time.Time
	if reg.DOB != nil && *reg.DOB != "" {
		// The app sends a bare ISO date; the reference's joda field would also
		// take a full timestamp, so tolerate one by using its date part.
		d, err := time.Parse(time.DateOnly, strings.SplitN(*reg.DOB, "T", 2)[0])
		if err != nil {
			badRequest("Invalid date of birth.")
			return
		}
		birthdate = &d
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(reg.Password), bcryptCost)
	if err != nil {
		h.log.Error("register hash", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	name := reg.Name
	if name == nil {
		name = reg.FirstName
	}
	a, err := h.store.InsertAccount(r.Context(), store.NewAccount{
		Email: email, PasswordHash: string(hash),
		Name: *name, FirstName: reg.FirstName, LastName: reg.LastName,
		Gender: reg.Gender, GenderOther: reg.GenderOther,
		HeightCM: reg.Height, WeightGrams: reg.Weight,
		Birthdate: birthdate, TZOffsetMS: reg.TZ,
	})
	if errors.Is(err, store.ErrDuplicateEmail) {
		writeJSON(w, http.StatusConflict, errorBody{
			Code: http.StatusConflict, Message: "Account already exists."})
		return
	}
	if err != nil {
		h.log.Error("register", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	h.log.Info("registered account", "account", a.ID)
	writeJSON(w, http.StatusOK, renderAccount(a))
}

// postAccountEmail changes the signed-in account's address.
//
// The app sends the whole account object, so last_modified rides along and
// guards the write the same way PUT /v1/account is guarded. The reference
// flattens both failure shapes, stale and duplicate, into a bodyless 409, and
// the app shows its generic message for either.
func (h *Handler) postAccountEmail(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body struct {
		Email        string `json:"email"`
		LastModified int64  `json:"last_modified"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid account."})
		return
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !validEmail(email) {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "EMAIL_INVALID"})
		return
	}

	a, err := h.store.UpdateEmail(r.Context(), accountID, body.LastModified, email)
	if errors.Is(err, store.ErrStaleAccount) || errors.Is(err, store.ErrDuplicateEmail) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if err != nil {
		h.log.Error("update email", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, renderAccount(a))
}

// postAccountPassword changes the signed-in account's password.
//
// 204 on success and a bodyless 409 for a wrong current password, both from
// the reference. Existing tokens survive on purpose: suripu never got to its
// "remove all tokens" TODO, and the phone that just changed the password is
// itself holding one of those tokens.
func (h *Handler) postAccountPassword(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid request."})
		return
	}

	if msg := validatePassword(body.NewPassword); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: msg})
		return
	}

	oldHash, err := h.store.PasswordHash(r.Context(), accountID)
	if err != nil {
		h.log.Error("password hash", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(body.CurrentPassword)) != nil {
		w.WriteHeader(http.StatusConflict)
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcryptCost)
	if err != nil {
		h.log.Error("password hash", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	updated, err := h.store.UpdatePassword(r.Context(), accountID, string(newHash), oldHash)
	if err != nil {
		h.log.Error("update password", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !updated {
		w.WriteHeader(http.StatusConflict)
		return
	}

	h.log.Info("changed password", "account", accountID)
	w.WriteHeader(http.StatusNoContent)
}
