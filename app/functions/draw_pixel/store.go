package drawpixel

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	clientOnce sync.Once
	client     *firestore.Client
	clientErr  error
)

// getClient crée le client au premier appel, puis le réutilise (instance chaude).
func getClient() (*firestore.Client, error) {
	clientOnce.Do(func() {
		// context.Background : le client doit survivre à la requête qui le crée.
		client, clientErr = firestore.NewClient(context.Background(), os.Getenv("GCP_PROJECT"))
	})
	return client, clientErr
}

// saveFirestore écrit le pixel dans pixels/{x}_{y}, si l'utilisateur n'a pas dépassé sa limite par minute
// (errRateLimited sinon : le pixel est alors abandonné).
// Pub/Sub ne garantit pas l'ordre : on n'écrase le pixel que si le message est plus récent
// que celui déjà écrit. La lecture et l'écriture sont dans la même transaction, sinon
// deux messages simultanés pourraient lire l'ancienne valeur et le plus vieux gagnerait.
func saveFirestore(ctx context.Context, evt drawEvent, at time.Time) error {
	c, err := getClient()
	if err != nil {
		return fmt.Errorf("client firestore: %w", err)
	}
	ref := c.Collection("pixels").Doc(fmt.Sprintf("%d_%d", evt.X, evt.Y))

	rlRef := c.Collection("ratelimits").Doc(evt.UserID)

	// limited est remis à zéro à chaque tentative : la transaction peut être rejouée en cas de contention
	var limited bool
	err = c.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		limited = false

		// Firestore exige toutes les lectures avant la première écriture
		snap, err := tx.Get(ref)
		notFound := status.Code(err) == codes.NotFound
		if err != nil && !notFound {
			return err
		}
		rlSnap, err := tx.Get(rlRef)
		var state rateState
		switch {
		case status.Code(err) == codes.NotFound:
			// premier pixel de cet utilisateur : compteur à zéro
		case err != nil:
			return err
		default:
			if err := rlSnap.DataTo(&state); err != nil {
				return err
			}
		}

		if !notFound {
			// un message périmé n'est pas une erreur : il n'a plus d'effet, et il ne consomme pas de quota
			// (c'est aussi le cas d'un message rejoué : le pixel porte déjà son heure)
			current, err := snap.DataAt("updated_at")
			if err == nil {
				if t, ok := current.(time.Time); ok && !t.Before(at) {
					return nil
				}
			}
		}

		next, ok := state.next(at, perMinuteDefault())
		if !ok {
			limited = true
			return nil // rien n'est écrit : ni le pixel, ni le compteur
		}
		if err := tx.Set(rlRef, map[string]any{"window": next.Window, "count": next.Count}, firestore.MergeAll); err != nil {
			return err
		}
		return tx.Set(ref, map[string]any{
			"x":          evt.X,
			"y":          evt.Y,
			"color":      evt.Color,
			"user_id":    evt.UserID,
			"chunk":      chunkID(evt.X, evt.Y),
			"updated_at": at,
		})
	})
	if err != nil {
		return err
	}
	if limited {
		return errRateLimited
	}
	return nil
}
