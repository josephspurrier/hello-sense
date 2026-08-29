package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// The sleep questionnaire.
//
// GET /v1/questions/ returns what the account has been asked and not answered;
// POST /v1/questions/save/ records an answer or a skip.
//
// This is a FUNCTIONAL port, not a faithful one, and the difference is the
// ordering. The reference picks questions through ~670 lines of onboarding
// sequencing, skip-based pausing, per-category feature flags, CBTI goals,
// anomaly questions and inter-question dependencies. This serves the pending
// set oldest-first. The shape of the response is identical; which questions
// come back, and in what order, is not guaranteed to match.
//
// That was a deliberate choice, and the evidence behind it is in the
// knowledgebase: of the five accessors that read answers, four have no callers
// anywhere, and the fifth gates one insight. The selection machinery serves
// coaching features that were never finished.

// QuestionView is one question as the app renders it.
type QuestionView struct {
	ID                int32        `json:"id"`
	AccountQuestionID int64        `json:"account_question_id"`
	Text              string       `json:"text"`
	Choices           []ChoiceView `json:"choices"`
	AskLocalDate      int64        `json:"ask_local_date"`
	Type              string       `json:"type"`
	AskTime           string       `json:"ask_time"`
}

type ChoiceView struct {
	ID         int32  `json:"id"`
	Text       string `json:"text"`
	QuestionID int32  `json:"question_id"`
}

func (h *Handler) getQuestions(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	pending, err := h.store.QuestionsFor(r.Context(), accountID, time.Now().UTC())
	if err != nil {
		h.log.Error("questions", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	ids := make([]int32, 0, len(pending))
	for _, q := range pending {
		ids = append(ids, q.ID)
	}
	choices, err := h.store.ChoicesFor(r.Context(), ids)
	if err != nil {
		h.log.Error("question choices", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Non-nil so an account with nothing pending marshals as [] rather than
	// null. The app treats null as a failure and [] as "nothing to ask".
	out := []QuestionView{}
	for _, q := range pending {
		cs := []ChoiceView{}
		for _, c := range choices[q.ID] {
			cs = append(cs, ChoiceView{ID: c.ID, Text: c.Text, QuestionID: c.QuestionID})
		}
		out = append(out, QuestionView{
			ID: q.ID, AccountQuestionID: q.AccountQuestionID,
			Text: q.Text, Choices: cs,
			AskLocalDate: q.AskLocalDate.UnixMilli(),
			Type:         q.ResponseType, AskTime: q.AskTime,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// saveAnswer is the POST body. The app sends the account_question_id it was
// given, not the question id, because the same question can be asked on many
// days and each asking is answered separately.
type saveAnswer struct {
	AccountQuestionID int64  `json:"account_question_id"`
	QuestionID        int32  `json:"question_id"`
	ResponseID        *int32 `json:"response_id"`
	Skip              *bool  `json:"skip"`
}

// postQuestionResponse records an answer or a skip.
//
// A skip is stored, not discarded. A question the user declined is a decision,
// and treating it as unanswered would ask it again forever.
func (h *Handler) postQuestionResponse(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// The app posts either a single answer or a list of them, because a
	// CHECKBOX question has several. Accept both rather than guessing.
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid response."})
		return
	}
	var answers []saveAnswer
	if err := json.Unmarshal(raw, &answers); err != nil {
		var one saveAnswer
		if err := json.Unmarshal(raw, &one); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code: http.StatusBadRequest, Message: "Invalid response."})
			return
		}
		answers = []saveAnswer{one}
	}
	if len(answers) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid response."})
		return
	}

	for _, a := range answers {
		skip := a.Skip != nil && *a.Skip
		// Ownership is re-checked in the store against the session's account.
		// The id arrives from a browser and answering somebody else's question
		// is exactly what an unchecked one allows.
		owned, err := h.store.SaveQuestionResponse(
			r.Context(), accountID, a.AccountQuestionID, a.ResponseID, skip)
		if err != nil {
			h.log.Error("save question response", "account", accountID, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !owned {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code: http.StatusBadRequest, Message: "Unknown question."})
			return
		}
	}

	h.log.Info("question answered", "account", accountID, "count", len(answers))
	w.WriteHeader(http.StatusAccepted)
}
