package drawpixel

// Limite de débit effective d'un utilisateur, lue HORS transaction et gardée quelques secondes en mémoire.
//
// Pourquoi hors transaction : une lecture dans une transaction pose un verrou, et mettre à jour le même document
// depuis des centaines de transactions les fait attendre les unes sur les autres. Ici, on a seulement besoin de savoir
// si le compteur est utile : sans surcharge ni limite basse, il ne l'est pas.
// Pourquoi un cache : sans lui, chaque pixel d'un trait de pinceau coûterait une lecture Firestore de plus
// (quota gratuit : 50 000 lectures par jour). Une surcharge posée par un administrateur prend effet en moins de limitTTL.

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// unlimitedFrom : à partir de cette limite par minute, on ne compte plus (voir saveFirestore).
// C'est la valeur choisie pour « laisser dessiner librement » ; 0 reste « utilisateur bloqué ».
const unlimitedFrom = 20000

// Variables pour que les tests règlent la durée, la taille et l'horloge.
var (
	limitTTL        = 10 * time.Second
	maxLimitEntries = 50000
	limitNow        = time.Now

	limitMu    sync.Mutex
	limitCache = map[string]limitEntry{}
)

type limitEntry struct {
	limit   int
	expires time.Time
}

// limitReader lit la surcharge d'un utilisateur : nil si le document ou le champ n'existent pas.
type limitReader func(ctx context.Context, user string) (*int, error)

// readLimitFirestore lit ratelimits/{user}.limit sans transaction.
func readLimitFirestore(ctx context.Context, user string) (*int, error) {
	c, err := getClient()
	if err != nil {
		return nil, err
	}
	snap, err := c.Collection("ratelimits").Doc(user).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state rateState
	if err := snap.DataTo(&state); err != nil {
		return nil, err
	}
	return state.Limit, nil
}

// userLimit donne la limite par minute de l'utilisateur : sa surcharge si elle existe, sinon def.
// Une panne de lecture est une erreur (le message sera réessayé) : on ne devine pas une limite.
func userLimit(ctx context.Context, read limitReader, user string, def int) (int, error) {
	now := limitNow()
	limitMu.Lock()
	if e, ok := limitCache[user]; ok && now.Before(e.expires) {
		limitMu.Unlock()
		return e.limit, nil
	}
	limitMu.Unlock()

	override, err := read(ctx, user)
	if err != nil {
		return 0, err
	}
	limit := def
	if override != nil {
		limit = *override
	}
	if limit < 0 {
		limit = 0
	}

	limitMu.Lock()
	if len(limitCache) >= maxLimitEntries {
		limitCache = map[string]limitEntry{} // borne la mémoire : on repart de zéro
	}
	limitCache[user] = limitEntry{limit: limit, expires: now.Add(limitTTL)}
	limitMu.Unlock()
	return limit, nil
}

// resetLimitCache vide le cache (tests).
func resetLimitCache() {
	limitMu.Lock()
	defer limitMu.Unlock()
	limitCache = map[string]limitEntry{}
}
