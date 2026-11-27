package rendergrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/storage"
)

const (
	metaObject = "meta/last_snapshot.json"
	tilePrefix = "tiles/"
)

var (
	clientOnce sync.Once
	gcsClient  *storage.Client
	clientErr  error
)

// getClient crée le client au premier appel, puis le réutilise (instance chaude).
func getClient() (*storage.Client, error) {
	clientOnce.Do(func() {
		// context.Background : le client doit survivre à la requête qui le crée.
		gcsClient, clientErr = storage.NewClient(context.Background())
	})
	return gcsClient, clientErr
}

// objectStore : tout ce que la fonction demande au bucket (et ce que les tests remplacent).
type objectStore interface {
	tileReader
	Put(ctx context.Context, name, contentType string, data []byte) error
	// SignedURL donne un lien de lecture temporaire : le bucket reste privé.
	SignedURL(ctx context.Context, name string, ttl time.Duration) (string, error)
}

type bucketStore struct{ b *storage.BucketHandle }

func (s bucketStore) LoadTile(ctx context.Context, id string) ([]byte, error) {
	return s.read(ctx, tilePrefix+id+".png")
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

func (s bucketStore) Put(ctx context.Context, name, contentType string, data []byte) error {
	w := s.b.Object(name).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// SignedURL : URL V4 signée par l'API IAM (signBlob) avec l'identité de la fonction : il n'y a aucune clé privée.
// Cela demande que la fonction ait le droit « Service Account Token Creator » sur elle-même (voir Terraform).
func (s bucketStore) SignedURL(_ context.Context, name string, ttl time.Duration) (string, error) {
	return s.b.SignedURL(name, &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		Method:         "GET",
		Expires:        time.Now().Add(ttl),
		GoogleAccessID: os.Getenv("SIGNER_EMAIL"),
	})
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
