package drawpixel

// Limite de débit par utilisateur : N pixels par minute, comptés dans Firestore (ratelimits/{user_id}).
//
// Fenêtre fixe d'une minute, calculée sur l'heure de PUBLICATION du message (pas sur l'heure de traitement) :
// le résultat dépend du message seulement, donc un rejeu donne la même décision.
// Limites connues : une rafale à cheval sur deux minutes peut atteindre 2 x N ; au-delà de N, le pixel est
// abandonné (accusé de réception, jamais réessayé) : l'utilisateur n'en est pas prévenu par le worker.

import (
	"errors"
	"os"
	"strconv"
	"time"
)

// defaultPerMinute : valeur du sujet (« par exemple 20 pixels par minute »). Réglable par RATE_LIMIT_PER_MINUTE,
// et par utilisateur (champ limit du document, que la commande /admin ratelimit écrira).
const defaultPerMinute = 20

var errRateLimited = errors.New("limite de débit atteinte")

// rateState : le document ratelimits/{user_id}.
type rateState struct {
	Window int64 `firestore:"window"` // minute en cours (secondes unix / 60)
	Count  int   `firestore:"count"`  // pixels acceptés dans cette minute
	// Limit remplace la valeur par défaut pour cet utilisateur ; absent = défaut ; 0 = utilisateur bloqué.
	Limit *int `firestore:"limit"`
}

// perMinuteDefault lit RATE_LIMIT_PER_MINUTE ; une valeur absente ou invalide donne la valeur du sujet.
func perMinuteDefault() int {
	if v, err := strconv.Atoi(os.Getenv("RATE_LIMIT_PER_MINUTE")); err == nil && v >= 0 {
		return v
	}
	return defaultPerMinute
}

// next décide du sort d'un pixel publié à l'instant at, et renvoie le nouvel état.
func (s rateState) next(at time.Time, def int) (rateState, bool) {
	limit := def
	if s.Limit != nil {
		limit = *s.Limit
	}
	if limit < 0 {
		limit = 0
	}
	window := at.Unix() / 60
	if window > s.Window {
		// nouvelle minute : le compteur repart de zéro
		s.Window = window
		s.Count = 0
	}
	// window <= s.Window : message arrivé en retard, d'une minute déjà close : compté dans la fenêtre en cours,
	// sinon un message ancien rouvrirait une fenêtre passée et contournerait la limite
	if s.Count >= limit {
		return s, false
	}
	s.Count++
	return s, true
}
