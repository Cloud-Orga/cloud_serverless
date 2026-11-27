package proxy

// Cache de l'identité Discord : jeton -> identifiant utilisateur.
//
// Pourquoi : le front envoie un POST /draw par pixel, donc un trait de pinceau de 25 pixels appellerait 25 fois
// l'API de Discord (/users/@me), qui limite le débit et répondrait en 429. Un jeton ne change pas d'identité :
// on garde la réponse en mémoire de l'instance.
//
// Choix de sécurité :
//   - la clé est l'empreinte SHA-256 du jeton : le jeton lui-même n'est jamais gardé ;
//   - un jeton valide est gardé 5 minutes : un jeton révoqué à Discord reste accepté au plus 5 minutes ;
//   - un jeton refusé est gardé 30 secondes : sans cela, envoyer des jetons faux ferait rappeler Discord à chaque fois ;
//   - les pannes (Discord injoignable, 5xx, délai dépassé) ne sont JAMAIS gardées : on réessaie à la requête suivante ;
//   - un seul appel à la fois par jeton : les requêtes simultanées attendent le résultat du premier.

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

// Variables pour que les tests règlent la durée, la taille et l'horloge.
var (
	positiveTTL     = 5 * time.Minute
	negativeTTL     = 30 * time.Second
	maxCacheEntries = 10000
	now             = time.Now
)

type cacheEntry struct {
	userID  string
	invalid bool // jeton refusé par Discord
	expires time.Time
}

// call : un appel à Discord en cours, dont les autres requêtes du même jeton attendent le résultat.
type call struct {
	done   chan struct{}
	userID string
	err    error
}

var (
	authMu    sync.Mutex
	authCache = map[[32]byte]cacheEntry{}
	inflight  = map[[32]byte]*call{}
)

// resetAuthCache vide le cache (tests).
func resetAuthCache() {
	authMu.Lock()
	defer authMu.Unlock()
	authCache = map[[32]byte]cacheEntry{}
	inflight = map[[32]byte]*call{}
}

// discordUserID donne l'identifiant Discord du porteur du jeton, en interrogeant Discord seulement si nécessaire.
func discordUserID(ctx context.Context, token string) (string, error) {
	key := sha256.Sum256([]byte(token))

	// Deux tours au plus : si le premier appelant échoue pour une raison qui lui est propre (son délai a expiré),
	// une requête qui l'attendait réessaie elle-même une fois.
	for attempt := 0; ; attempt++ {
		authMu.Lock()
		if e, ok := authCache[key]; ok && now().Before(e.expires) {
			authMu.Unlock()
			if e.invalid {
				return "", errUnauthorized
			}
			return e.userID, nil
		}

		if c, ok := inflight[key]; ok {
			// un appel est déjà en cours pour ce jeton : on attend son résultat
			authMu.Unlock()
			select {
			case <-c.done:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if c.err != nil && isCallerError(c.err) && ctx.Err() == nil && attempt == 0 {
				continue
			}
			return c.userID, c.err
		}

		c := &call{done: make(chan struct{})}
		inflight[key] = c
		authMu.Unlock()

		c.userID, c.err = fetchDiscordUserID(ctx, token)

		authMu.Lock()
		delete(inflight, key)
		switch {
		case c.err == nil:
			store(key, cacheEntry{userID: c.userID, expires: now().Add(positiveTTL)})
		case errors.Is(c.err, errUnauthorized):
			store(key, cacheEntry{invalid: true, expires: now().Add(negativeTTL)})
		}
		authMu.Unlock()
		close(c.done)
		return c.userID, c.err
	}
}

// isCallerError : l'échec vient du contexte de celui qui a fait l'appel, pas de Discord.
func isCallerError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// store range une entrée en gardant la taille bornée (authMu doit être tenu).
func store(key [32]byte, e cacheEntry) {
	if len(authCache) >= maxCacheEntries {
		t := now()
		for k, v := range authCache {
			if !t.Before(v.expires) {
				delete(authCache, k)
			}
		}
		if len(authCache) >= maxCacheEntries {
			// toujours plein d'entrées valides : on repart de zéro plutôt que de grossir sans limite
			authCache = map[[32]byte]cacheEntry{}
		}
	}
	authCache[key] = e
}
