package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// serveurDiscord : un faux Discord qui compte ses appels. L'identité rendue dépend du jeton reçu :
// « tok-X » devient l'utilisateur « user-X ». Les jetons commençant par « mauvais » sont refusés.
func serveurDiscord(t *testing.T, delai time.Duration, status int) *int32 {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(delai)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case status != 0:
			w.WriteHeader(status)
		case strings.HasPrefix(tok, "mauvais"):
			w.WriteHeader(http.StatusUnauthorized)
		default:
			_, _ = w.Write([]byte(`{"id":"user-` + strings.TrimPrefix(tok, "tok-") + `"}`))
		}
	}))
	old := discordAPI
	discordAPI = srv.URL
	resetAuthCache()
	t.Cleanup(func() {
		discordAPI = old
		srv.Close()
		resetAuthCache()
		now = time.Now
		positiveTTL, negativeTTL, maxCacheEntries = 5*time.Minute, 30*time.Second, 10000
	})
	return &calls
}

func TestUnTraitDePinceauNAppelleDiscordQuUneFois(t *testing.T) {
	calls := serveurDiscord(t, 0, 0)
	for i := 0; i < 50; i++ {
		id, err := discordUserID(context.Background(), "tok-A")
		if err != nil || id != "user-A" {
			t.Fatalf("appel %d : %q, %v", i, id, err)
		}
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("%d appels à Discord pour 50 pixels, 1 attendu", got)
	}
}

func TestUnJetonNeDonnePasLIdentiteDUnAutre(t *testing.T) {
	calls := serveurDiscord(t, 0, 0)
	// alternance : la mise en cache ne doit jamais mélanger deux utilisateurs
	for i := 0; i < 20; i++ {
		for _, u := range []string{"A", "B", "C"} {
			id, err := discordUserID(context.Background(), "tok-"+u)
			if err != nil || id != "user-"+u {
				t.Fatalf("jeton %s : identité %q (%v)", u, id, err)
			}
		}
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Errorf("%d appels, 3 attendus (un par jeton)", got)
	}
}

func TestLIdentiteExpire(t *testing.T) {
	calls := serveurDiscord(t, 0, 0)
	horloge := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return horloge }
	discordUserID(context.Background(), "tok-A")
	horloge = horloge.Add(positiveTTL - time.Second)
	discordUserID(context.Background(), "tok-A")
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("avant expiration : %d appels, 1 attendu", got)
	}
	horloge = horloge.Add(2 * time.Second) // juste après les 5 minutes
	discordUserID(context.Background(), "tok-A")
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Errorf("après expiration : %d appels, 2 attendus (un jeton révoqué ne reste pas accepté)", got)
	}
}

func TestUnJetonRefuseNeFaitPasRappelerDiscord(t *testing.T) {
	calls := serveurDiscord(t, 0, 0)
	horloge := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return horloge }
	for i := 0; i < 30; i++ {
		if _, err := discordUserID(context.Background(), "mauvais-1"); !errors.Is(err, errUnauthorized) {
			t.Fatalf("appel %d : %v", i, err)
		}
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("30 jetons faux : %d appels à Discord, 1 attendu", got)
	}
	horloge = horloge.Add(negativeTTL + time.Second)
	discordUserID(context.Background(), "mauvais-1")
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Errorf("après 30 s : %d appels, 2 attendus", got)
	}
}

func TestLesPannesNeSontPasGardees(t *testing.T) {
	calls := serveurDiscord(t, 0, http.StatusInternalServerError)
	for i := 0; i < 3; i++ {
		if _, err := discordUserID(context.Background(), "tok-A"); err == nil || errors.Is(err, errUnauthorized) {
			t.Fatalf("une panne de Discord doit rester une panne : %v", err)
		}
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Errorf("%d appels, 3 attendus : une panne ne doit pas être gardée", got)
	}
}

func TestDesRequetesSimultanéesPartagentUnSeulAppel(t *testing.T) {
	calls := serveurDiscord(t, 80*time.Millisecond, 0)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := discordUserID(context.Background(), "tok-A")
			if err != nil || id != "user-A" {
				t.Errorf("%q, %v", id, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("30 requêtes simultanées : %d appels à Discord, 1 attendu", got)
	}
}

func TestUneRequeteAnnuleeNEmpechePasLesAutres(t *testing.T) {
	calls := serveurDiscord(t, 100*time.Millisecond, 0)
	// la première requête perd patience avant la réponse de Discord
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := discordUserID(ctx, "tok-A"); err == nil {
		t.Fatal("la requête au délai trop court doit échouer")
	}
	// la suivante, avec du temps, réussit, et l'échec de la première n'est pas gardé comme un refus
	id, err := discordUserID(context.Background(), "tok-A")
	if err != nil || id != "user-A" {
		t.Errorf("%q, %v", id, err)
	}
	if got := atomic.LoadInt32(calls); got < 1 {
		t.Errorf("appels : %d", got)
	}
}

func TestLaTailleDuCacheEstBornee(t *testing.T) {
	serveurDiscord(t, 0, 0)
	maxCacheEntries = 5
	for i := 0; i < 50; i++ {
		discordUserID(context.Background(), "tok-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	authMu.Lock()
	n := len(authCache)
	authMu.Unlock()
	if n > maxCacheEntries {
		t.Errorf("%d entrées pour un plafond de %d", n, maxCacheEntries)
	}
}

func TestLeJetonNEstPasLaCleDuCache(t *testing.T) {
	serveurDiscord(t, 0, 0)
	discordUserID(context.Background(), "tok-SECRET-123")
	authMu.Lock()
	defer authMu.Unlock()
	for k := range authCache {
		if strings.Contains(string(k[:]), "SECRET") {
			t.Error("le jeton en clair ne doit pas servir de clé")
		}
	}
}
