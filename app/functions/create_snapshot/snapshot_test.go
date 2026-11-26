package createsnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

const testPalette = `{"colors":[{"id":1,"name":"blanc","hex":"#FFFFFF"},{"id":2,"name":"rouge","hex":"#FF0000"},{"id":3,"name":"bleu","hex":"#0000FF"}]}`

func TestMain(m *testing.M) {
	os.Setenv("PALETTE_JSON", testPalette)
	os.Exit(m.Run())
}

// --- doubles ---------------------------------------------------------------------------------------------

type fakeSource struct {
	pixels []pixel
	err    error
	since  []time.Time // seuils reçus, pour vérifier la reprise
}

func (f *fakeSource) ChangedSince(_ context.Context, since time.Time, fn func(pixel)) error {
	f.since = append(f.since, since)
	if f.err != nil {
		return f.err
	}
	for _, p := range f.pixels {
		if since.IsZero() || p.UpdatedAt.After(since) {
			fn(p)
		}
	}
	return nil
}

type fakeStore struct {
	tiles   map[string][]byte
	m       *meta
	saves   map[string]int
	metaSet int
	failOn  string
}

func newStore() *fakeStore { return &fakeStore{tiles: map[string][]byte{}, saves: map[string]int{}} }

func (s *fakeStore) LoadTile(_ context.Context, id string) ([]byte, error) { return s.tiles[id], nil }
func (s *fakeStore) SaveTile(_ context.Context, id string, data []byte) error {
	if id == s.failOn {
		return errors.New("bucket indisponible")
	}
	s.tiles[id] = data
	s.saves[id]++
	return nil
}
func (s *fakeStore) LoadMeta(context.Context) (meta, bool, error) {
	if s.m == nil {
		return meta{}, false, nil
	}
	return *s.m, true, nil
}
func (s *fakeStore) SaveMeta(_ context.Context, m meta) error {
	s.m = &m
	s.metaSet++
	return nil
}

func palette(t *testing.T) color.Palette {
	t.Helper()
	p, err := loadPalette()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

// same compare deux couleurs par leurs valeurs (le type dépend de l'origine : palette ou PNG relu)
func same(got color.Color, want color.NRGBA) bool {
	gr, gg, gb, ga := got.RGBA()
	wr, wg, wb, wa := want.RGBA()
	return gr == wr && gg == wg && gb == wb && ga == wa
}

func colorAt(t *testing.T, store *fakeStore, id string, x, y int) color.Color {
	t.Helper()
	img, ok := decodeTile(store.tiles[id])
	if !ok {
		t.Fatalf("bloc %s illisible ou absent", id)
	}
	return img.At(x, y)
}

// --- tests ------------------------------------------------------------------------------------------------

func TestPremierPassageEcritLesBlocsTouches(t *testing.T) {
	src := &fakeSource{pixels: []pixel{
		{X: 3, Y: 4, Color: 2, UpdatedAt: at(1)},
		{X: 499, Y: 499, Color: 3, UpdatedAt: at(2)}, // dernier pixel du bloc 0_0
		{X: 500, Y: 0, Color: 1, UpdatedAt: at(3)},   // premier pixel du bloc 1_0
	}}
	store := newStore()
	res, err := run(context.Background(), src, store, palette(t), false, at(10))
	if err != nil {
		t.Fatal(err)
	}
	if res.TilesWritten != 2 || len(store.tiles) != 2 {
		t.Fatalf("2 blocs attendus, résultat %+v", res)
	}
	if store.tiles["0_0"] == nil || store.tiles["1_0"] == nil {
		t.Fatalf("blocs attendus 0_0 et 1_0, présents : %v", keys(store.tiles))
	}
	// coordonnées locales : (500,0) devient (0,0) dans le bloc 1_0
	if got := colorAt(t, store, "1_0", 0, 0); !same(got, color.NRGBA{255, 255, 255, 255}) {
		t.Errorf("(500,0) dans 1_0 : %v", got)
	}
	if got := colorAt(t, store, "0_0", 3, 4); !same(got, color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("(3,4) dans 0_0 : %v", got)
	}
	if got := colorAt(t, store, "0_0", 499, 499); !same(got, color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("(499,499) dans 0_0 : %v", got)
	}
	if got := colorAt(t, store, "0_0", 10, 10); !same(got, color.NRGBA{}) {
		t.Errorf("case vide attendue transparente : %v", got)
	}
	if store.m == nil || !store.m.UpdatedUntil.Equal(at(3)) {
		t.Errorf("seuil attendu %v, obtenu %+v", at(3), store.m)
	}
}

func TestSansChangementRienNestReecrit(t *testing.T) {
	src := &fakeSource{pixels: []pixel{{X: 3, Y: 4, Color: 2, UpdatedAt: at(1)}}}
	store := newStore()
	pal := palette(t)
	if _, err := run(context.Background(), src, store, pal, false, at(5)); err != nil {
		t.Fatal(err)
	}
	// deuxième passage 5 minutes plus tard : le pixel est encore dans la fenêtre de recouvrement (10 min)
	res, err := run(context.Background(), src, store, pal, false, at(10))
	if err != nil {
		t.Fatal(err)
	}
	if store.saves["0_0"] != 1 || res.TilesWritten != 0 {
		t.Errorf("le bloc inchangé ne doit pas être réécrit : écritures %d, résultat %+v", store.saves["0_0"], res)
	}
	if store.metaSet != 1 {
		t.Errorf("le seuil n'a pas avancé : les métadonnées ne doivent pas être réécrites (%d écritures)", store.metaSet)
	}
}

func TestSeulLeBlocModifieEstReecrit(t *testing.T) {
	src := &fakeSource{pixels: []pixel{
		{X: 1, Y: 1, Color: 2, UpdatedAt: at(1)},
		{X: 600, Y: 1, Color: 3, UpdatedAt: at(1)},
	}}
	store := newStore()
	pal := palette(t)
	if _, err := run(context.Background(), src, store, pal, false, at(5)); err != nil {
		t.Fatal(err)
	}
	// un pixel du bloc 1_0 change bien après : le bloc 0_0 reste tel quel
	src.pixels = append(src.pixels, pixel{X: 601, Y: 2, Color: 1, UpdatedAt: at(30)})
	if _, err := run(context.Background(), src, store, pal, false, at(31)); err != nil {
		t.Fatal(err)
	}
	if store.saves["0_0"] != 1 {
		t.Errorf("0_0 réécrit %d fois, 1 attendue", store.saves["0_0"])
	}
	if store.saves["1_0"] != 2 {
		t.Errorf("1_0 réécrit %d fois, 2 attendues", store.saves["1_0"])
	}
	// l'ancien pixel de 1_0 est conservé : l'image existante sert de base
	if got := colorAt(t, store, "1_0", 100, 1); !same(got, color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("pixel précédent perdu : %v", got)
	}
	if got := colorAt(t, store, "1_0", 101, 2); !same(got, color.NRGBA{255, 255, 255, 255}) {
		t.Errorf("nouveau pixel absent : %v", got)
	}
}

func TestPixelRedessineChangeDeCouleur(t *testing.T) {
	src := &fakeSource{pixels: []pixel{{X: 7, Y: 7, Color: 2, UpdatedAt: at(1)}}}
	store := newStore()
	pal := palette(t)
	run(context.Background(), src, store, pal, false, at(2))
	src.pixels = []pixel{{X: 7, Y: 7, Color: 3, UpdatedAt: at(40)}}
	run(context.Background(), src, store, pal, false, at(41))
	if got := colorAt(t, store, "0_0", 7, 7); !same(got, color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("nouvelle couleur attendue (bleu) : %v", got)
	}
}

func TestReprendAvecRecouvrement(t *testing.T) {
	src := &fakeSource{pixels: []pixel{{X: 1, Y: 1, Color: 2, UpdatedAt: at(30)}}}
	store := newStore()
	pal := palette(t)
	run(context.Background(), src, store, pal, false, at(31))
	run(context.Background(), src, store, pal, false, at(36))
	if len(src.since) != 2 || !src.since[0].IsZero() {
		t.Fatalf("seuils : %v", src.since)
	}
	if want := at(30).Add(-overlap); !src.since[1].Equal(want) {
		t.Errorf("seuil de reprise %v, attendu %v", src.since[1], want)
	}
}

func TestMessageRejoueEnRetardEstRattrape(t *testing.T) {
	// un pixel arrive avec une date plus ancienne que le seuil (message rejoué) mais dans la fenêtre de recouvrement
	src := &fakeSource{pixels: []pixel{{X: 1, Y: 1, Color: 2, UpdatedAt: at(30)}}}
	store := newStore()
	pal := palette(t)
	run(context.Background(), src, store, pal, false, at(31))
	src.pixels = append(src.pixels, pixel{X: 9, Y: 9, Color: 3, UpdatedAt: at(25)})
	run(context.Background(), src, store, pal, false, at(36))
	if got := colorAt(t, store, "0_0", 9, 9); !same(got, color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("le pixel rejoué en retard est absent : %v", got)
	}
}

func TestPassageCompletIgnoreLesImagesExistantes(t *testing.T) {
	store := newStore()
	pal := palette(t)
	// image abîmée dans le bucket
	store.tiles["0_0"] = []byte("pas un png")
	store.m = &meta{UpdatedUntil: at(100)}
	src := &fakeSource{pixels: []pixel{{X: 2, Y: 2, Color: 2, UpdatedAt: at(1)}}}
	res, err := run(context.Background(), src, store, pal, true, at(110))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Full || !src.since[0].IsZero() {
		t.Errorf("un passage complet relit tout : seuil %v", src.since[0])
	}
	if got := colorAt(t, store, "0_0", 2, 2); !same(got, color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("bloc reconstruit : %v", got)
	}
}

func TestPixelsInvalidesIgnores(t *testing.T) {
	src := &fakeSource{pixels: []pixel{
		{X: -1, Y: 0, Color: 2, UpdatedAt: at(1)},
		{X: 0, Y: 0, Color: 0, UpdatedAt: at(1)},  // 0 = vide, jamais dessiné
		{X: 0, Y: 0, Color: 99, UpdatedAt: at(1)}, // numéro inconnu de la palette
		{X: 5, Y: 5, Color: 2, UpdatedAt: at(2)},
	}}
	store := newStore()
	res, err := run(context.Background(), src, store, palette(t), false, at(5))
	if err != nil {
		t.Fatal(err)
	}
	if res.PixelsSkipped != 3 || res.TilesWritten != 1 {
		t.Errorf("résultat %+v", res)
	}
}

func TestPremierPassageSansPixelNecritRien(t *testing.T) {
	store := newStore()
	res, err := run(context.Background(), &fakeSource{}, store, palette(t), false, at(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(store.tiles) != 0 || store.m != nil || res.MetaSaved {
		t.Errorf("rien à écrire, résultat %+v", res)
	}
}

func TestErreurDeLectureEtDEcriture(t *testing.T) {
	pal := palette(t)
	if _, err := run(context.Background(), &fakeSource{err: errors.New("firestore indisponible")}, newStore(), pal, false, at(1)); err == nil {
		t.Error("une erreur de lecture doit remonter (pour que Pub/Sub réessaie)")
	}
	store := newStore()
	store.failOn = "0_0"
	src := &fakeSource{pixels: []pixel{{X: 1, Y: 1, Color: 2, UpdatedAt: at(1)}}}
	if _, err := run(context.Background(), src, store, pal, false, at(2)); err == nil {
		t.Error("une erreur d'écriture doit remonter")
	}
	if store.m != nil {
		t.Error("les métadonnées ne doivent pas avancer si un bloc n'a pas pu être écrit")
	}
}

func TestBlocVidePese(t *testing.T) {
	pal := palette(t)
	data, err := encodeTile(blankTile(pal))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bloc vide : %d octets", len(data))
	if len(data) > 5000 {
		t.Errorf("un bloc vide pèse %d octets : la compression ne joue pas", len(data))
	}
	// l'encodage est déterministe : indispensable pour ne pas réécrire un bloc inchangé
	again, _ := encodeTile(blankTile(pal))
	if !bytes.Equal(data, again) {
		t.Error("encodage non déterministe")
	}
}

func TestPalette(t *testing.T) {
	p := palette(t)
	if len(p) != 4 || p[0] != (color.NRGBA{}) {
		t.Fatalf("palette : %v", p)
	}
	for _, bad := range []string{``, `pas du json`, `{"colors":[]}`,
		`{"colors":[{"id":0,"name":"x","hex":"#FFFFFF"}]}`,
		`{"colors":[{"id":1,"name":"x","hex":"rouge"}]}`,
		`{"colors":[{"id":1,"name":"a","hex":"#FFFFFF"},{"id":1,"name":"b","hex":"#000000"}]}`} {
		os.Setenv("PALETTE_JSON", bad)
		if _, err := loadPalette(); err == nil {
			t.Errorf("palette %q acceptée", bad)
		}
	}
	os.Setenv("PALETTE_JSON", testPalette)
}

func TestChunkID(t *testing.T) {
	for _, c := range []struct {
		x, y int
		want string
	}{{0, 0, "0_0"}, {499, 499, "0_0"}, {500, 0, "1_0"}, {0, 500, "0_1"}, {9999, 9999, "19_19"}} {
		if got := chunkID(c.x, c.y); got != c.want {
			t.Errorf("chunkID(%d,%d) = %s, attendu %s", c.x, c.y, got, c.want)
		}
	}
}

// --- entrée HTTP -------------------------------------------------------------------------------------------

func TestHandleRefuseLesMauvaisesRequetes(t *testing.T) {
	rec := httptest.NewRecorder()
	handle(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET : %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handle(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("pas du json"))))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("enveloppe invalide : %d", rec.Code)
	}
}

func TestHandleSansPaletteRenvoie500(t *testing.T) {
	os.Setenv("PALETTE_JSON", "")
	defer os.Setenv("PALETTE_JSON", testPalette)
	body, _ := json.Marshal(map[string]any{"message": map[string]any{"messageId": "1"}})
	rec := httptest.NewRecorder()
	handle(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("sans palette : %d", rec.Code)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
