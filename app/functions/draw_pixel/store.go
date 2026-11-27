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

// saveFirestore écrit le pixel dans pixels/{x}_{y}.
//
// Deux chemins, choisis selon la limite de débit de l'utilisateur :
//   - limite « illimitée » (unlimitedFrom ou plus) : le pixel est écrit seul. C'est le cas courant, et surtout le cas
//     d'un pinceau : tous ses pixels viennent du même utilisateur, et un compteur mis à jour dans chaque transaction
//     les ferait attendre les uns sur les autres (mesuré : 93 % de transactions avortées, 40 pixels par seconde au lieu
//     de 570, pixels perdus en file d'erreurs) ;
//   - limite basse : le pixel et le compteur de l'utilisateur sont écrits dans la même transaction
//     (errRateLimited si la limite est atteinte : le pixel est alors abandonné).
//
// Pub/Sub ne garantit pas l'ordre : on n'écrase le pixel que si le message est plus récent que celui déjà écrit.
// La lecture et l'écriture sont dans la même transaction, sinon deux messages simultanés pourraient lire l'ancienne
// valeur et le plus vieux gagnerait.
func saveFirestore(ctx context.Context, evt drawEvent, at time.Time) error {
	c, err := getClient()
	if err != nil {
		return fmt.Errorf("client firestore: %w", err)
	}
	ref := c.Collection("pixels").Doc(fmt.Sprintf("%d_%d", evt.X, evt.Y))

	limit, err := userLimit(ctx, readLimitFirestore, evt.UserID, perMinuteDefault())
	if err != nil {
		return fmt.Errorf("limite de l'utilisateur: %w", err)
	}
	if limit >= unlimitedFrom {
		return savePixel(ctx, c, ref, evt, at)
	}
	return savePixelCounted(ctx, c, ref, c.Collection("ratelimits").Doc(evt.UserID), evt, at)
}

// pixelIsStale dit si un pixel déjà écrit est plus récent que le message (le message n'a alors plus d'effet).
func pixelIsStale(snap *firestore.DocumentSnapshot, at time.Time) bool {
	current, err := snap.DataAt("updated_at")
	if err != nil {
		return false
	}
	t, ok := current.(time.Time)
	return ok && !t.Before(at)
}

func pixelData(evt drawEvent, at time.Time) map[string]any {
	return map[string]any{
		"x":          evt.X,
		"y":          evt.Y,
		"color":      evt.Color,
		"user_id":    evt.UserID,
		"chunk":      chunkID(evt.X, evt.Y),
		"updated_at": at,
	}
}

// savePixel : le pixel seul, sans compteur.
func savePixel(ctx context.Context, c *firestore.Client, ref *firestore.DocumentRef, evt drawEvent, at time.Time) error {
	return c.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		switch {
		case status.Code(err) == codes.NotFound:
			// premier pixel à cet endroit : on écrit
		case err != nil:
			return err
		case pixelIsStale(snap, at):
			return nil // un message périmé n'est pas une erreur : il n'a simplement plus d'effet
		}
		return tx.Set(ref, pixelData(evt, at))
	})
}

// savePixelCounted : le pixel et le compteur de l'utilisateur, dans la même transaction.
func savePixelCounted(ctx context.Context, c *firestore.Client, ref, rlRef *firestore.DocumentRef, evt drawEvent, at time.Time) error {
	// limited est remis à zéro à chaque tentative : la transaction peut être rejouée en cas de contention
	var limited bool
	err := c.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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

		// un message périmé (ou rejoué) n'a plus d'effet et ne consomme pas de quota
		if !notFound && pixelIsStale(snap, at) {
			return nil
		}

		next, ok := state.next(at, perMinuteDefault())
		if !ok {
			limited = true
			return nil // rien n'est écrit : ni le pixel, ni le compteur
		}
		if err := tx.Set(rlRef, map[string]any{"window": next.Window, "count": next.Count}, firestore.MergeAll); err != nil {
			return err
		}
		return tx.Set(ref, pixelData(evt, at))
	})
	if err != nil {
		return err
	}
	if limited {
		return errRateLimited
	}
	return nil
}
