package createsnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

const (
	metaObject = "meta/last_snapshot.json"
	tilePrefix = "tiles/"
)

var (
	clientsOnce sync.Once
	fsClient    *firestore.Client
	gcsClient   *storage.Client
	clientsErr  error
)

// getClients crée les clients au premier appel, puis les réutilise (instance chaude).
func getClients() (*firestore.Client, *storage.Client, error) {
	clientsOnce.Do(func() {
		// context.Background : les clients doivent survivre à la requête qui les crée.
		ctx := context.Background()
		fsClient, clientsErr = firestore.NewClient(ctx, os.Getenv("GCP_PROJECT"))
		if clientsErr != nil {
			return
		}
		gcsClient, clientsErr = storage.NewClient(ctx)
	})
	return fsClient, gcsClient, clientsErr
}

// firestoreSource lit les pixels dans la collection pixels.
type firestoreSource struct{ c *firestore.Client }

func (s firestoreSource) ChangedSince(ctx context.Context, since time.Time, fn func(pixel)) error {
	q := s.c.Collection("pixels").OrderBy("updated_at", firestore.Asc)
	if !since.IsZero() {
		q = q.Where("updated_at", ">", since)
	}
	it := q.Documents(ctx)
	defer it.Stop()
	for {
		snap, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		// Champs écrits par draw-pixel : x, y, color (numéro de palette), updated_at
		var doc struct {
			X         int       `firestore:"x"`
			Y         int       `firestore:"y"`
			Color     int       `firestore:"color"`
			UpdatedAt time.Time `firestore:"updated_at"`
		}
		if err := snap.DataTo(&doc); err != nil {
			logger.Warn("pixel illisible, ignoré", "doc", snap.Ref.ID, "error", err.Error())
			continue
		}
		fn(pixel{X: doc.X, Y: doc.Y, Color: doc.Color, UpdatedAt: doc.UpdatedAt})
	}
}

// bucketStore range les blocs et les métadonnées dans le bucket privé des snapshots.
type bucketStore struct{ b *storage.BucketHandle }

func (s bucketStore) LoadTile(ctx context.Context, id string) ([]byte, error) {
	return s.read(ctx, tilePrefix+id+".png")
}

func (s bucketStore) SaveTile(ctx context.Context, id string, data []byte) error {
	return s.write(ctx, tilePrefix+id+".png", "image/png", data)
}

func (s bucketStore) LoadMeta(ctx context.Context) (meta, bool, error) {
	data, err := s.read(ctx, metaObject)
	if err != nil || data == nil {
		return meta{}, false, err
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return meta{}, false, fmt.Errorf("métadonnées illisibles: %w", err)
	}
	return m, true, nil
}

func (s bucketStore) SaveMeta(ctx context.Context, m meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.write(ctx, metaObject, "application/json", data)
}

// read renvoie nil, nil si l'objet n'existe pas.
func (s bucketStore) read(ctx context.Context, name string) ([]byte, error) {
	r, err := s.b.Object(name).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (s bucketStore) write(ctx context.Context, name, contentType string, data []byte) error {
	w := s.b.Object(name).NewWriter(ctx)
	w.ContentType = contentType
	// les blocs sont relus par la fonction et par le front : pas de cache côté navigateur sans signature
	w.CacheControl = "private, max-age=0"
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
