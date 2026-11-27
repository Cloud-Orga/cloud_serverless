package drawpixel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lecteur : un faux Firestore qui compte ses lectures
type lecteur struct {
	reads    int32
	override map[string]*int
	err      error
	delai    time.Duration
}

func (l *lecteur) read(_ context.Context, user string) (*int, error) {
	atomic.AddInt32(&l.reads, 1)
	time.Sleep(l.delai)
	if l.err != nil {
		return nil, l.err
	}
	return l.override[user], nil
}

func neuf(t *testing.T) {
	t.Helper()
	resetLimitCache()
	t.Cleanup(func() {
		resetLimitCache()
		limitNow = time.Now
		limitTTL, maxLimitEntries = 10*time.Second, 50000
	})
}

func TestUnTraitDePinceauNeLitLaLimiteQuUneFois(t *testing.T) {
	neuf(t)
	l := &lecteur{}
	for i := 0; i < 2000; i++ {
		got, err := userLimit(context.Background(), l.read, "u1", 20000)
		if err != nil || got != 20000 {
			t.Fatalf("pixel %d : %d, %v", i, got, err)
		}
	}
	if n := atomic.LoadInt32(&l.reads); n != 1 {
		t.Errorf("%d lectures Firestore pour 2000 pixels, 1 attendue", n)
	}
}

func TestUneSurchargePrimeSurLeDefaut(t *testing.T) {
	neuf(t)
	trois, zero := 3, 0
	l := &lecteur{override: map[string]*int{"limite": &trois, "bloque": &zero}}
	for user, want := range map[string]int{"normal": 20000, "limite": 3, "bloque": 0} {
		if got, _ := userLimit(context.Background(), l.read, user, 20000); got != want {
			t.Errorf("%s : %d, attendu %d", user, got, want)
		}
	}
}

func TestUneLimiteNegativeVautZero(t *testing.T) {
	neuf(t)
	neg := -5
	l := &lecteur{override: map[string]*int{"u": &neg}}
	if got, _ := userLimit(context.Background(), l.read, "u", 20000); got != 0 {
		t.Errorf("limite négative : %d, 0 attendu (jamais « illimité »)", got)
	}
}

func TestUneSurchargePoseParUnAdminPrendEffetApresLeCache(t *testing.T) {
	neuf(t)
	horloge := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	limitNow = func() time.Time { return horloge }
	l := &lecteur{}
	userLimit(context.Background(), l.read, "u", 20000)
	// un administrateur fixe une limite de 5
	cinq := 5
	l.override = map[string]*int{"u": &cinq}
	horloge = horloge.Add(limitTTL - time.Second)
	if got, _ := userLimit(context.Background(), l.read, "u", 20000); got != 20000 {
		t.Errorf("pendant le cache : %d", got)
	}
	horloge = horloge.Add(2 * time.Second)
	if got, _ := userLimit(context.Background(), l.read, "u", 20000); got != 5 {
		t.Errorf("après le cache : %d, 5 attendu", got)
	}
}

func TestUnePanneDeLectureNEstJamaisPriseePourIllimite(t *testing.T) {
	neuf(t)
	l := &lecteur{err: errors.New("firestore indisponible")}
	got, err := userLimit(context.Background(), l.read, "u", 20000)
	if err == nil {
		t.Fatalf("une panne doit remonter (le message sera réessayé), obtenu limite %d", got)
	}
	// et la panne n'est pas gardée : la lecture suivante réessaie
	l.err = nil
	if got, err := userLimit(context.Background(), l.read, "u", 20000); err != nil || got != 20000 {
		t.Errorf("après la panne : %d, %v", got, err)
	}
	if n := atomic.LoadInt32(&l.reads); n != 2 {
		t.Errorf("%d lectures, 2 attendues (la panne ne doit pas être gardée)", n)
	}
}

func TestLaTailleDuCacheEstBornee(t *testing.T) {
	neuf(t)
	maxLimitEntries = 5
	l := &lecteur{}
	for i := 0; i < 100; i++ {
		userLimit(context.Background(), l.read, string(rune('a'+i%26))+string(rune('a'+i/26)), 20000)
	}
	limitMu.Lock()
	n := len(limitCache)
	limitMu.Unlock()
	if n > maxLimitEntries {
		t.Errorf("%d entrées pour un plafond de %d", n, maxLimitEntries)
	}
}

func TestPlusieursUtilisateursSimultanes(t *testing.T) {
	neuf(t)
	l := &lecteur{delai: time.Millisecond}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := userLimit(context.Background(), l.read, "u"+string(rune('a'+i%10)), 20000); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}

func TestLeSeuilIllimiteEstCelDeLaConfiguration(t *testing.T) {
	// la valeur par défaut de Terraform (variable.tf) est 20000 : elle doit atteindre le chemin sans compteur
	if unlimitedFrom != 20000 {
		t.Errorf("unlimitedFrom = %d : il doit rester égal à rate_limit_per_minute (infra/serverless/variable.tf)", unlimitedFrom)
	}
	for _, c := range []struct {
		limit int
		libre bool
	}{{20, false}, {19999, false}, {20000, true}, {100000, true}, {0, false}} {
		if got := c.limit >= unlimitedFrom; got != c.libre {
			t.Errorf("limite %d : sans compteur = %v, attendu %v", c.limit, got, c.libre)
		}
	}
}
