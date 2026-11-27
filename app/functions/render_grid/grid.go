package rendergrid

// Assemblage de la grille complète à partir des blocs de 500 x 500 écrits par create-snapshot.
// Cette logique ne connaît ni le bucket ni Discord : les tests la font tourner sur des doubles.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"
	"time"
)

const (
	chunkSize  = 500 // contrat avec create-snapshot, draw-pixel et le front
	chunkCount = 20  // 20 x 20 blocs : grille de 10 000 x 10 000
	gridSize   = chunkSize * chunkCount

	// loadWorkers : lectures de blocs en parallèle (400 objets dans le bucket)
	loadWorkers = 16
)

// meta : le contenu de meta/last_snapshot.json, écrit par create-snapshot.
type meta struct {
	UpdatedUntil time.Time `json:"updated_until"`
	GeneratedAt  time.Time `json:"generated_at"`
}

type tileReader interface {
	// LoadTile renvoie le PNG du bloc, ou nil s'il n'existe pas (aucun pixel dessiné dans ce bloc).
	LoadTile(ctx context.Context, id string) ([]byte, error)
	LoadMeta(ctx context.Context) (meta, bool, error)
}

// assemble lit les 400 blocs et les place dans une image de la grille entière.
// Un bloc absent reste vide (transparent). Un bloc illisible est une erreur : mieux vaut pas d'image
// qu'une image fausse, et le passage complet de create-snapshot (full) le reconstruit.
func assemble(ctx context.Context, r tileReader, pal color.Palette) (*image.Paletted, error) {
	img := image.NewPaletted(image.Rect(0, 0, gridSize, gridSize), pal)

	type job struct{ cx, cy int }
	jobs := make(chan job)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex // protège img (écritures de zones disjointes, mais on garde la règle simple) et firstErr
		firstErr error
	)
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := 0; i < loadWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				data, err := r.LoadTile(ctx, fmt.Sprintf("%d_%d", j.cx, j.cy))
				if err == nil && data != nil {
					err = place(img, &mu, j.cx, j.cy, data)
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("bloc %d_%d: %w", j.cx, j.cy, err)
						cancel()
					}
					mu.Unlock()
				}
			}
		}()
	}
	for cy := 0; cy < chunkCount; cy++ {
		for cx := 0; cx < chunkCount; cx++ {
			select {
			case jobs <- job{cx, cy}:
			case <-ctx.Done():
			}
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	// délai dépassé ou requête annulée : des blocs n'ont pas été lus, l'image serait partielle
	if err := parent.Err(); err != nil {
		return nil, err
	}
	return img, nil
}

// place copie les indices de couleur du bloc à sa position dans la grille.
// Les indices SONT les numéros de couleur : la palette de la grille (courante) s'applique à tous les blocs.
func place(img *image.Paletted, mu *sync.Mutex, cx, cy int, data []byte) error {
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tile, ok := decoded.(*image.Paletted)
	if !ok || tile.Bounds() != image.Rect(0, 0, chunkSize, chunkSize) {
		return fmt.Errorf("image inattendue (%T, %v)", decoded, decoded.Bounds())
	}
	mu.Lock()
	defer mu.Unlock()
	for y := 0; y < chunkSize; y++ {
		dst := (cy*chunkSize+y)*img.Stride + cx*chunkSize
		src := y * tile.Stride
		copy(img.Pix[dst:dst+chunkSize], tile.Pix[src:src+chunkSize])
	}
	return nil
}

// encode : PNG à palette. Compression par défaut : 0,14 s pour une grille réelle, 1,6 s dans le pire cas.
func encode(img *image.Paletted) ([]byte, error) {
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
