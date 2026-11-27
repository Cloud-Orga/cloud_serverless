package drawpixel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) // pile sur une minute

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func intPtr(v int) *int { return &v }

// feed envoie n pixels publiés à l'instant at et renvoie combien sont acceptés
func feed(s *rateState, n int, when time.Time, def int) int {
	ok := 0
	for i := 0; i < n; i++ {
		var accepted bool
		*s, accepted = s.next(when, def)
		if accepted {
			ok++
		}
	}
	return ok
}

func TestLaLimiteParDefautEstAppliquee(t *testing.T) {
	var s rateState
	if got := feed(&s, 25, at(5), 20); got != 20 {
		t.Errorf("%d pixels acceptés sur 25, 20 attendus", got)
	}
	if s.Count != 20 {
		t.Errorf("compteur %d, 20 attendu (un pixel refusé ne compte pas)", s.Count)
	}
}

func TestLaMinuteSuivanteRepartDeZero(t *testing.T) {
	var s rateState
	feed(&s, 20, at(5), 20)
	if got := feed(&s, 1, at(30), 20); got != 0 {
		t.Error("le 21e pixel de la même minute doit être refusé")
	}
	if got := feed(&s, 20, at(65), 20); got != 20 {
		t.Errorf("minute suivante : %d acceptés, 20 attendus", got)
	}
}

func TestSurchargeParUtilisateur(t *testing.T) {
	// limite plus haute que le défaut
	s := rateState{Limit: intPtr(100)}
	if got := feed(&s, 120, at(1), 20); got != 100 {
		t.Errorf("surcharge à 100 : %d acceptés", got)
	}
	// limite plus basse
	s = rateState{Limit: intPtr(3)}
	if got := feed(&s, 10, at(1), 20); got != 3 {
		t.Errorf("surcharge à 3 : %d acceptés", got)
	}
	// 0 bloque l'utilisateur
	s = rateState{Limit: intPtr(0)}
	if got := feed(&s, 5, at(1), 20); got != 0 {
		t.Errorf("limite 0 : %d acceptés, utilisateur bloqué attendu", got)
	}
	// une limite négative vaut 0, jamais « illimité »
	s = rateState{Limit: intPtr(-5)}
	if got := feed(&s, 5, at(1), 20); got != 0 {
		t.Errorf("limite négative : %d acceptés", got)
	}
}

func TestSansSurchargeLeDefautServ(t *testing.T) {
	var s rateState
	if got := feed(&s, 50, at(1), 7); got != 7 {
		t.Errorf("défaut 7 : %d acceptés", got)
	}
}

func TestLaSurchargeSurvitAuChangementDeMinute(t *testing.T) {
	s := rateState{Limit: intPtr(5)}
	feed(&s, 5, at(1), 20)
	feed(&s, 1, at(70), 20)
	if s.Limit == nil || *s.Limit != 5 {
		t.Error("la surcharge ne doit pas être perdue quand le compteur repart")
	}
}

func TestUnMessageAncienNeContourneLaLimite(t *testing.T) {
	var s rateState
	feed(&s, 20, at(65), 20) // minute 2 pleine
	// un message de la minute 1 arrive en retard : il ne rouvre pas la minute passée
	if got := feed(&s, 1, at(10), 20); got != 0 {
		t.Error("un message en retard ne doit pas contourner la limite en cours")
	}
}

func TestPerMinuteDefault(t *testing.T) {
	cases := map[string]int{"": 20, "abc": 20, "-3": 20, "0": 0, "60": 60, "1.5": 20}
	for in, want := range cases {
		os.Setenv("RATE_LIMIT_PER_MINUTE", in)
		if got := perMinuteDefault(); got != want {
			t.Errorf("RATE_LIMIT_PER_MINUTE=%q -> %d, attendu %d", in, got, want)
		}
	}
	os.Unsetenv("RATE_LIMIT_PER_MINUTE")
}

// --- gestionnaire --------------------------------------------------------------------------------------------

func pushRequest(t *testing.T) *http.Request {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"x": 1, "y": 2, "color": 1, "user_id": "u1"})
	body, _ := json.Marshal(map[string]any{"message": map[string]any{"data": data, "messageId": "m1", "publishTime": t0}})
	return httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
}

func TestUnPixelAbandonneEstAcquitte(t *testing.T) {
	os.Setenv("PALETTE_JSON", `{"colors":[{"id":1,"name":"blanc"}]}`)
	save = func(context.Context, drawEvent, time.Time) error { return errRateLimited }
	t.Cleanup(func() { save = saveFirestore })
	rec := httptest.NewRecorder()
	handle(rec, pushRequest(t))
	// 200 : Pub/Sub n'envoie pas à nouveau un message que personne ne pourra jamais accepter
	if rec.Code != http.StatusOK {
		t.Errorf("limite atteinte : code %d, 200 attendu", rec.Code)
	}
}

func TestUnePanneNEstPasUneLimite(t *testing.T) {
	os.Setenv("PALETTE_JSON", `{"colors":[{"id":1,"name":"blanc"}]}`)
	save = func(context.Context, drawEvent, time.Time) error { return context.DeadlineExceeded }
	t.Cleanup(func() { save = saveFirestore })
	rec := httptest.NewRecorder()
	handle(rec, pushRequest(t))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panne : code %d, 500 attendu (réessai utile)", rec.Code)
	}
}
