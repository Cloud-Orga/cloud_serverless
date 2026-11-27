package rendergrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPalette = `{"colors":[{"id":1,"name":"blanc","hex":"#FFFFFF"},{"id":2,"name":"rouge","hex":"#FF0000"},{"id":3,"name":"bleu","hex":"#0000FF"}]}`

func TestMain(m *testing.M) {
	os.Setenv("PALETTE_JSON", testPalette)
	os.Exit(m.Run())
}

// --- doubles ---------------------------------------------------------------------------------------------

type fakeStore struct {
	mu      sync.Mutex
	tiles   map[string][]byte
	m       *meta
	puts    map[string][]byte
	loadErr error
	putErr  error
	signErr error
}

func newStore() *fakeStore {
	return &fakeStore{tiles: map[string][]byte{}, puts: map[string][]byte{}}
}

func (s *fakeStore) LoadTile(_ context.Context, id string) ([]byte, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.tiles[id], nil
}
func (s *fakeStore) LoadMeta(context.Context) (meta, bool, error) {
	if s.m == nil {
		return meta{}, false, nil
	}
	return *s.m, true, nil
}
func (s *fakeStore) Put(_ context.Context, name, _ string, data []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts[name] = data
	return nil
}
func (s *fakeStore) SignedURL(_ context.Context, name string, _ time.Duration) (string, error) {
	if s.signErr != nil {
		return "", s.signErr
	}
	return "https://signed.example/" + name + "?sig=x", nil
}

type sent struct {
	app, token, content string
	file                []byte
	filename            string
}

type fakeReply struct {
	got []sent
	err error
}

func (f *fakeReply) fn(_ context.Context, app, token, content string, file []byte, filename string) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, sent{app, token, content, file, filename})
	return nil
}

func pal(t *testing.T) color.Palette {
	t.Helper()
	p, err := loadPalette()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// tileWith : un bloc avec un pixel (lx, ly) d'indice idx
func tileWith(t *testing.T, lx, ly int, idx uint8) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, chunkSize, chunkSize), pal(t))
	img.SetColorIndex(lx, ly, idx)
	data, err := encode(img)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func index(img *image.Paletted, x, y int) uint8 { return img.ColorIndexAt(x, y) }

var now = time.Date(2026, 11, 27, 10, 30, 15, 0, time.UTC)

// --- assemblage --------------------------------------------------------------------------------------------

func TestAssemblePlaceChaqueBlocAuBonEndroit(t *testing.T) {
	s := newStore()
	s.tiles["0_0"] = tileWith(t, 3, 4, 2)
	s.tiles["1_0"] = tileWith(t, 0, 0, 3)       // doit tomber en (500, 0)
	s.tiles["0_1"] = tileWith(t, 7, 9, 1)       // en (7, 509)
	s.tiles["19_19"] = tileWith(t, 499, 499, 2) // en (9999, 9999)
	img, err := assemble(context.Background(), s, pal(t))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 10000 || b.Dy() != 10000 {
		t.Fatalf("taille %v", b)
	}
	for _, c := range []struct {
		x, y int
		want uint8
	}{{3, 4, 2}, {500, 0, 3}, {7, 509, 1}, {9999, 9999, 2}} {
		if got := index(img, c.x, c.y); got != c.want {
			t.Errorf("(%d,%d) = %d, attendu %d", c.x, c.y, got, c.want)
		}
	}
	// ailleurs : vide, et les voisins des pixels posés ne sont pas touchés
	for _, c := range [][2]int{{0, 0}, {4, 4}, {499, 0}, {501, 0}, {500, 1}, {9998, 9999}} {
		if got := index(img, c[0], c[1]); got != 0 {
			t.Errorf("(%d,%d) devrait être vide, indice %d", c[0], c[1], got)
		}
	}
}

func TestAssembleSansBlocEstUneGrilleVide(t *testing.T) {
	img, err := assemble(context.Background(), newStore(), pal(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range img.Pix {
		if v != 0 {
			t.Fatal("grille non vide")
		}
	}
}

func TestAssembleRefuseUnBlocAbime(t *testing.T) {
	s := newStore()
	s.tiles["2_2"] = []byte("pas un png")
	if _, err := assemble(context.Background(), s, pal(t)); err == nil || !strings.Contains(err.Error(), "2_2") {
		t.Errorf("une erreur nommant le bloc est attendue : %v", err)
	}
	// un PNG valide mais d'une autre taille n'est pas un bloc
	var buf bytes.Buffer
	small := image.NewPaletted(image.Rect(0, 0, 10, 10), pal(t))
	if err := png_encode(&buf, small); err != nil {
		t.Fatal(err)
	}
	s = newStore()
	s.tiles["0_0"] = buf.Bytes()
	if _, err := assemble(context.Background(), s, pal(t)); err == nil {
		t.Error("un bloc de 10 x 10 ne doit pas être accepté")
	}
}

func TestAssembleRemonteLesErreursDeLecture(t *testing.T) {
	s := newStore()
	s.loadErr = errors.New("bucket indisponible")
	if _, err := assemble(context.Background(), s, pal(t)); err == nil {
		t.Error("une erreur de lecture doit remonter")
	}
}

func TestAssembleRespecteLAnnulation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newStore()
	s.loadErr = ctx.Err()
	if _, err := assemble(ctx, s, pal(t)); err == nil {
		t.Error("un contexte annulé doit faire échouer l'assemblage")
	}
}

// --- commandes ---------------------------------------------------------------------------------------------

func cmd(name string) command {
	return command{InteractionID: "i1", ApplicationID: "app1", Token: "secret-token", Command: name, UserID: "u1"}
}

func TestGetJointLImageQuandElleTient(t *testing.T) {
	s := newStore()
	s.tiles["0_0"] = tileWith(t, 1, 1, 2)
	s.m = &meta{UpdatedUntil: now.Add(-3 * time.Minute)}
	r := &fakeReply{}
	res, err := process(context.Background(), deps{store: s, pal: pal(t), reply: r.fn}, cmd("get"), now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "attachment" || len(s.puts) != 0 || len(r.got) != 1 {
		t.Fatalf("résultat %+v, écritures %d, réponses %d", res, len(s.puts), len(r.got))
	}
	g := r.got[0]
	if g.app != "app1" || g.token != "secret-token" || !bytes.HasPrefix(g.file, []byte("\x89PNG")) || g.filename != "grille-20261127T103015Z.png" {
		t.Errorf("réponse %+v", g)
	}
	if !strings.Contains(g.content, "Dernier pixel pris en compte") {
		t.Errorf("la fraîcheur doit être indiquée : %q", g.content)
	}
}

func TestGetTropLourdDonneUnLien(t *testing.T) {
	old := maxAttachment
	maxAttachment = 10 // tout est « trop lourd »
	defer func() { maxAttachment = old }()
	s := newStore()
	r := &fakeReply{}
	res, err := process(context.Background(), deps{store: s, pal: pal(t), reply: r.fn}, cmd("get"), now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "link" || res.Object != "get/20261127T103015Z.png" {
		t.Fatalf("résultat %+v", res)
	}
	if s.puts[res.Object] == nil || len(r.got[0].file) != 0 || !strings.Contains(r.got[0].content, "https://signed.example/get/") {
		t.Errorf("lien attendu sans pièce jointe : %+v", r.got[0])
	}
}

func TestSnapshotRangeUneImageDateeEtDonneUnLien(t *testing.T) {
	s := newStore()
	s.tiles["0_0"] = tileWith(t, 5, 5, 3)
	r := &fakeReply{}
	res, err := process(context.Background(), deps{store: s, pal: pal(t), reply: r.fn}, cmd("snapshot"), now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Object != "snapshots/20261127T103015Z.png" || s.puts[res.Object] == nil {
		t.Fatalf("résultat %+v", res)
	}
	if !strings.Contains(r.got[0].content, "https://signed.example/snapshots/20261127T103015Z.png") || len(r.got[0].file) != 0 {
		t.Errorf("réponse %+v", r.got[0])
	}
	// l'image rangée est bien la grille : on la relit
	img, err := pngDecode(s.puts[res.Object])
	if err != nil || img.Bounds().Dx() != 10000 {
		t.Fatalf("image rangée illisible : %v", err)
	}
	if got := img.(*image.Paletted).ColorIndexAt(5, 5); got != 3 {
		t.Errorf("pixel (5,5) = %d, attendu 3", got)
	}
}

func TestDeuxSnapshotsGardentChacunLeurNom(t *testing.T) {
	s := newStore()
	r := &fakeReply{}
	d := deps{store: s, pal: pal(t), reply: r.fn}
	process(context.Background(), d, cmd("snapshot"), now)
	process(context.Background(), d, cmd("snapshot"), now.Add(5*time.Minute))
	if len(s.puts) != 2 {
		t.Errorf("l'historique garde un fichier par snapshot : %d", len(s.puts))
	}
}

func TestGrilleVideLeDit(t *testing.T) {
	r := &fakeReply{}
	if _, err := process(context.Background(), deps{store: newStore(), pal: pal(t), reply: r.fn}, cmd("get"), now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.got[0].content, "La grille est vide") {
		t.Errorf("%q", r.got[0].content)
	}
}

func TestErreursRemontent(t *testing.T) {
	d := func(s *fakeStore, r *fakeReply) deps { return deps{store: s, pal: pal(t), reply: r.fn} }
	if _, err := process(context.Background(), d(newStore(), &fakeReply{}), cmd("admin"), now); err == nil {
		t.Error("commande inconnue acceptée")
	}
	s := newStore()
	s.putErr = errors.New("écriture refusée")
	if _, err := process(context.Background(), d(s, &fakeReply{}), cmd("snapshot"), now); err == nil {
		t.Error("erreur d'écriture masquée")
	}
	s = newStore()
	s.signErr = errors.New("signature impossible")
	if _, err := process(context.Background(), d(s, &fakeReply{}), cmd("snapshot"), now); err == nil {
		t.Error("erreur de signature masquée")
	}
	if _, err := process(context.Background(), d(newStore(), &fakeReply{err: errors.New("discord 404")}), cmd("get"), now); err == nil {
		t.Error("erreur Discord masquée")
	}
}

// --- entrée HTTP -------------------------------------------------------------------------------------------

func push(t *testing.T, c command, attempt int) *http.Request {
	t.Helper()
	data, _ := json.Marshal(c)
	body, _ := json.Marshal(map[string]any{
		"message":         map[string]any{"data": data, "messageId": "m1"},
		"deliveryAttempt": attempt,
	})
	return httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
}

func withDeps(t *testing.T, d deps) {
	t.Helper()
	old := buildDeps
	buildDeps = func() (deps, error) { return d, nil }
	t.Cleanup(func() { buildDeps = old })
}

func TestHandleSucces(t *testing.T) {
	r := &fakeReply{}
	withDeps(t, deps{store: newStore(), pal: pal(t), reply: r.fn})
	rec := httptest.NewRecorder()
	handle(rec, push(t, cmd("get"), 1))
	if rec.Code != http.StatusOK || len(r.got) != 1 {
		t.Fatalf("code %d, réponses %d", rec.Code, len(r.got))
	}
}

func TestHandleEchecReessaiPuisMessageALUtilisateur(t *testing.T) {
	s := newStore()
	s.loadErr = errors.New("bucket indisponible")
	r := &fakeReply{}
	withDeps(t, deps{store: s, pal: pal(t), reply: r.fn})

	// tentatives 1 à 4 : 500, Pub/Sub réessaie, l'utilisateur ne reçoit encore rien
	for attempt := 1; attempt < maxAttempts; attempt++ {
		rec := httptest.NewRecorder()
		handle(rec, push(t, cmd("get"), attempt))
		if rec.Code != http.StatusInternalServerError || len(r.got) != 0 {
			t.Fatalf("tentative %d : code %d, réponses %d", attempt, rec.Code, len(r.got))
		}
	}
	// dernière tentative : message d'erreur dans Discord, et acquittement
	rec := httptest.NewRecorder()
	handle(rec, push(t, cmd("get"), maxAttempts))
	if rec.Code != http.StatusOK || len(r.got) != 1 || !strings.Contains(r.got[0].content, "Impossible") {
		t.Fatalf("dernière tentative : code %d, réponses %+v", rec.Code, r.got)
	}
}

func TestHandleRefuseLesMauvaisesRequetes(t *testing.T) {
	rec := httptest.NewRecorder()
	handle(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET : %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handle(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("pas du json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("enveloppe invalide : %d", rec.Code)
	}
	body, _ := json.Marshal(map[string]any{"message": map[string]any{"data": []byte("pas du json")}})
	rec = httptest.NewRecorder()
	handle(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("message illisible : %d", rec.Code)
	}
}

// --- réponse Discord ---------------------------------------------------------------------------------------

func TestReplyTexteSeul(t *testing.T) {
	var gotPath, gotMethod, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotCT = r.URL.Path, r.Method, r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&gotBody)
	}))
	defer srv.Close()
	old := discordAPI
	discordAPI = srv.URL
	defer func() { discordAPI = old }()

	if err := reply(context.Background(), "app1", "tok", "bonjour", nil, ""); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/webhooks/app1/tok/messages/@original" || gotCT != "application/json" || gotBody["content"] != "bonjour" {
		t.Errorf("%s %s %s %v", gotMethod, gotPath, gotCT, gotBody)
	}
}

func TestReplyAvecImageEnMultipart(t *testing.T) {
	var payload map[string]any
	var file []byte
	var filename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Error(err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			switch p.FormName() {
			case "payload_json":
				json.Unmarshal(b, &payload)
			case "files[0]":
				file, filename = b, p.FileName()
			}
		}
	}))
	defer srv.Close()
	old := discordAPI
	discordAPI = srv.URL
	defer func() { discordAPI = old }()

	if err := reply(context.Background(), "app1", "tok", "voici", []byte("PNGDATA"), "grille.png"); err != nil {
		t.Fatal(err)
	}
	if string(file) != "PNGDATA" || filename != "grille.png" || payload["content"] != "voici" {
		t.Errorf("fichier %q (%s), charge utile %v", file, filename, payload)
	}
}

func TestReplyNeLaissePasFuirLeJeton(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unknown Webhook", http.StatusNotFound)
	}))
	url := srv.URL
	old := discordAPI
	discordAPI = url
	defer func() { discordAPI = old }()

	err := reply(context.Background(), "app1", "TOKEN-SECRET", "x", nil, "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("erreur 404 attendue : %v", err)
	}
	srv.Close() // serveur éteint : l'erreur réseau ne doit pas contenir l'URL, donc le jeton
	err = reply(context.Background(), "app1", "TOKEN-SECRET", "x", nil, "")
	if err == nil || strings.Contains(err.Error(), "TOKEN-SECRET") {
		t.Errorf("le jeton ne doit pas figurer dans l'erreur : %v", err)
	}
	if err := reply(context.Background(), "", "tok", "x", nil, ""); err == nil {
		t.Error("application_id manquant accepté")
	}
}

func TestPalette(t *testing.T) {
	p := pal(t)
	if len(p) != 4 || p[0] != (color.NRGBA{}) {
		t.Fatalf("palette %v", p)
	}
	os.Setenv("PALETTE_JSON", "")
	defer os.Setenv("PALETTE_JSON", testPalette)
	if _, err := loadPalette(); err == nil {
		t.Error("palette absente acceptée")
	}
}

// petits utilitaires de test
func png_encode(w io.Writer, img image.Image) error { return png.Encode(w, img) }
func pngDecode(b []byte) (image.Image, error)       { return png.Decode(bytes.NewReader(b)) }
