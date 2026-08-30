package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The error names are a wire protocol: the app switches on the literal enum
// text in the body to pick the message a person sees. These cases are
// Registration.validate's behavior, including its order (the first failure is
// the only one reported).
func TestRegistrationValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want string // expected message, "" for anything not-400
	}{
		{"no name at all", `{"email":"a@b.co","password":"hunter22"}`,
			"MISSING_FIRSTNAME_AND_NAME"},
		{"empty firstname", `{"firstname":"","email":"a@b.co","password":"hunter22"}`,
			"MISSING_FIRSTNAME"},
		{"name too long", `{"name":"` + strings.Repeat("x", 101) + `","email":"a@b.co","password":"hunter22"}`,
			"NAME_TOO_LONG"},
		{"empty name", `{"name":"","email":"a@b.co","password":"hunter22"}`,
			"NAME_TOO_SHORT"},
		{"short password", `{"firstname":"Ada","email":"a@b.co","password":"ab1"}`,
			"PASSWORD_TOO_SHORT"},
		{"common password", `{"firstname":"Ada","email":"a@b.co","password":"letmein"}`,
			"PASSWORD_INSECURE"},
		// Password is checked before email, so a bad password on a bad email
		// reports the password.
		{"password before email", `{"firstname":"Ada","email":"nope","password":"letmein"}`,
			"PASSWORD_INSECURE"},
		{"bad email", `{"firstname":"Ada","email":"nope","password":"hunter22"}`,
			"EMAIL_INVALID"},
		// The reference's EmailValidator rejects TLD-less domains, which is
		// exactly what someone tries against a local stack.
		{"tld-less email", `{"firstname":"Ada","email":"ada@localhost","password":"hunter22"}`,
			"EMAIL_INVALID"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := &Handler{}
			r := httptest.NewRequest("POST", "/v1/account", strings.NewReader(c.body))
			w := httptest.NewRecorder()
			h.postAccount(w, r)

			if w.Code != 400 {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			var body errorBody
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the JsonError shape: %v", err)
			}
			if body.Message != c.want {
				t.Errorf("message = %q, want %q", body.Message, c.want)
			}
		})
	}
}

func TestValidEmail(t *testing.T) {
	for _, c := range []struct {
		email string
		want  bool
	}{
		{"orb@example.com", true},
		{"first.last@sub.example.co.uk", true},
		{"a@b.co", true},
		{"ada@localhost", false}, // no TLD, the documented reference behavior
		{"@example.com", false},
		{"ada@", false},
		{"ada", false},
		{"ada@.com", false},
		{"ada@example.", false},
		{"a da@example.com", false},
		{"ada@ex@ample.com", false},
		{"", false},
	} {
		if got := validEmail(c.email); got != c.want {
			t.Errorf("validEmail(%q) = %v, want %v", c.email, got, c.want)
		}
	}
}
