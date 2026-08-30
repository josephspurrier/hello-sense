package api

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
)

// The profile photo endpoints: POST/DELETE /v1/photo/profile and the
// unauthenticated image itself.
//
// The reference streamed the upload to a public S3 bucket and answered with a
// MultiDensityImage: the same URL under "phone_1x", "phone_2x" and
// "phone_3x" (PhotoResource.java never actually resized). Here the bytes go
// in Postgres and the URL points back at orb, but the wire shape is the
// reference's, because the app feeds it straight to SENRemoteImage.
//
// The image URL is served without a bearer token on purpose: the app loads it
// with a plain image fetch that carries no Authorization header, exactly as
// it loaded the S3 URLs. The 128-bit random token in the path is the access
// control, the same scheme as the share pages, and a re-upload mints a new
// token so an old URL stops working.

// maxPhotoUploadBytes bounds the multipart body. The reference bounded
// uploads by configuration; phone photos the app sends are JPEG re-encodes of
// well under a megabyte, so ten is generous without letting a stray client
// grow the database.
const maxPhotoUploadBytes = 10 << 20

// photoPath is where images are served from. The token is the final segment.
const photoPath = "/v1/photo/p/"

// RemoteImage is suripu's MultiDensityImage: one URL per phone density. Never
// resized here, matching the reference, which uploaded one file and repeated
// its URL three times.
type RemoteImage struct {
	Phone1x string `json:"phone_1x"`
	Phone2x string `json:"phone_2x"`
	Phone3x string `json:"phone_3x"`
}

// remoteImageFor builds the wire object for a stored photo token.
//
// The host comes from the request so the URL matches whatever name the phone
// reached orb by; hardcoding one would break the moment the API is reached
// through a different hostname. Scheme is https unconditionally: the app
// refuses plain http, and orb never serves it.
func remoteImageFor(r *http.Request, token string) RemoteImage {
	url := "https://" + r.Host + photoPath + token
	return RemoteImage{Phone1x: url, Phone2x: url, Phone3x: url}
}

// newPhotoToken mints the random path segment protecting an image.
func newPhotoToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// postProfilePhoto stores the uploaded photo and answers with its URLs.
func (h *Handler) postProfilePhoto(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// An oversized body surfaces as a multipart parse error below, answered
	// 400 like the reference's content-length check.
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoUploadBytes)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid photo upload."})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid photo upload."})
		return
	}

	// The part's declared type, verified against the bytes. The app only ever
	// sends image/jpeg or image/png; anything else is refused rather than
	// stored and served back as a mystery.
	contentType := http.DetectContentType(data)
	if contentType != "image/jpeg" && contentType != "image/png" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Unsupported photo type."})
		return
	}
	_ = header // the filename ("file.jpg") carries nothing the bytes do not

	token, err := newPhotoToken()
	if err != nil {
		h.log.Error("photo token", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := h.store.PutProfilePhoto(r.Context(), accountID, token, contentType, data); err != nil {
		h.log.Error("put profile photo", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	h.log.Info("stored profile photo", "account", accountID, "bytes", len(data))
	writeJSON(w, http.StatusOK, remoteImageFor(r, token))
}

// deleteProfilePhoto removes the photo. 204 always, matching the reference's
// unconditional delete.
func (h *Handler) deleteProfilePhoto(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := h.store.DeleteProfilePhoto(r.Context(), accountID); err != nil {
		h.log.Error("delete profile photo", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getProfilePhotoImage serves the bytes behind a token. Unauthenticated; see
// the package comment above.
func (h *Handler) getProfilePhotoImage(w http.ResponseWriter, r *http.Request) {
	contentType, data, found, err := h.store.ProfilePhotoByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		h.log.Error("profile photo image", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// Immutable is honest here: a replaced photo gets a new token, so the
	// bytes behind a given URL never change.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}
