package driver

// DocStore + BlobStore semantics, run on a fresh strict store.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/cloud/doc"
)

var confIndexes = IndexRegistry{
	"conformance__items":      {Scalar: []string{"n", "s", "b", "ts", "grp", "mixed", "nested.x"}, Array: []string{"tags"}},
	"conformance__a__cgItems": {Scalar: []string{"run", "n"}},
	"conformance__b__cgItems": {Scalar: []string{"run", "n"}},
	"cgItems":                 {Scalar: []string{"run", "n"}},
}

func yes() *bool { b := true; return &b }

func open(t testing.TB, dir string, c ...func(*Config)) *Ports {
	t.Helper()
	cfg := Config{DataDir: dir, PublicURL: "http://opm.test/base", ConsoleURL: "http://console.test", Strict: yes()}
	for _, f := range c {
		f(&cfg)
	}
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func confPorts(t *testing.T) *Ports {
	return open(t, t.TempDir(), func(c *Config) { c.Indexes = confIndexes })
}
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func json1(v any) string { return doc.MarshalString(v) }
func eq(t testing.TB, got, want any, msg ...any) {
	t.Helper()
	if json1(got) != json1(want) {
		t.Fatalf("%v: got %s, want %s", msg, json1(got), json1(want))
	}
}
func ids(t testing.TB, q *Query) []string {
	t.Helper()
	s, err := q.Get()
	must(t, err)
	out := []string{}
	for _, d := range s.Docs {
		out = append(out, d.ID())
	}
	return out
}
func code(t testing.TB, err error, want string) {
	t.Helper()
	if StoreErrorCode(err) != want {
		t.Fatalf("expected %s, got %v", want, err)
	}
}
func data(t testing.TB, r *DocRef) *doc.Map {
	t.Helper()
	s, err := r.Get()
	must(t, err)
	return s.Data()
}

func TestDocuments(t *testing.T) {
	p := confPorts(t)
	st := p.Store
	const run = "rx"
	t.Run("set get exists delete refs", func(t *testing.T) {
		ref := st.Doc("conformance/" + run + "/docs/a")
		s, err := ref.Get()
		must(t, err)
		if s.Exists() || s.Data() != nil {
			t.Fatal("missing doc")
		}
		must(t, ref.Set(doc.Obj("x", 1, "u", doc.Undefined, "m", doc.Obj("k", "v", "gone", doc.Undefined), "arr", []any{1, "two", doc.Obj("three", 3)})))
		s, _ = ref.Get()
		if !s.Exists() || s.ID() != "a" {
			t.Fatal(s)
		}
		eq(t, s.Data(), doc.Obj("x", 1, "m", doc.Obj("k", "v"), "arr", []any{1, "two", doc.Obj("three", 3)}))
		eq(t, s.Get("m.k"), "v")
		eq(t, ref.Parent().Path, "conformance/rx/docs")
		eq(t, ref.Parent().Parent().Path, "conformance/rx")
		if st.Collection("conformance").Parent() != nil {
			t.Fatal("root has no parent")
		}
		eq(t, ref.Collection("sub").Path, "conformance/rx/docs/a/sub")
		if id := st.Collection("conformance/rx/docs").Doc().ID(); !regexp.MustCompile(`^[A-Za-z0-9]{20}$`).MatchString(id) {
			t.Fatal("auto id", id)
		}
		must(t, ref.Set(doc.Obj("y", 2)))
		eq(t, data(t, ref), doc.Obj("y", 2))
		must(t, ref.Delete())
		must(t, ref.Delete())
		s, _ = ref.Get()
		if s.Exists() {
			t.Fatal("deleted")
		}
	})
	t.Run("merge", func(t *testing.T) {
		ref := st.Doc("conformance/rx/docs/merge")
		must(t, ref.Set(doc.Obj("a", doc.Obj("x", 1, "y", 1, "deep", doc.Obj("k", 1)), "arr", []any{1, 2}, "keep", true)))
		must(t, ref.Set(doc.Obj("a", doc.Obj("y", 2, "z", 3, "deep", doc.Obj("j", 2)), "arr", []any{3}), Merge))
		eq(t, data(t, ref), doc.Obj("a", doc.Obj("x", 1, "y", 2, "deep", doc.Obj("k", 1, "j", 2), "z", 3), "arr", []any{3}, "keep", true))
		n := st.Doc("conformance/rx/docs/merge-new")
		must(t, n.Set(doc.Obj("fresh", 1), Merge))
		eq(t, data(t, n), doc.Obj("fresh", 1))
	})
	t.Run("update dotted", func(t *testing.T) {
		ref := st.Doc("conformance/rx/docs/upd")
		must(t, ref.Set(doc.Obj("a", doc.Obj("b", doc.Obj("c", 1, "d", 2)), "m", doc.Obj("p", 1, "q", 2), "top", 0)))
		must(t, ref.Update(doc.Obj("a.b.c", 5, "a.new.leaf", "x", "m", doc.Obj("r", 3), "top", "y")))
		eq(t, data(t, ref), doc.Obj("a", doc.Obj("b", doc.Obj("c", 5, "d", 2), "new", doc.Obj("leaf", "x")), "m", doc.Obj("r", 3), "top", "y"))
	})
	t.Run("transforms", func(t *testing.T) {
		ref := st.Doc("conformance/rx/docs/tr")
		must(t, ref.Set(doc.Obj("n", 1, "tags", []any{"a", "b"}, "gone", 1, "nested", doc.Obj("c", 1))))
		must(t, ref.Update(doc.Obj("n", doc.Increment(2), "tags", doc.ArrayUnion("b", "c"), "gone", doc.DeleteField(), "nested.c", doc.Increment(1), "fresh", doc.Increment(5), "at", doc.ServerTimestamp())))
		d := data(t, ref)
		eq(t, d.Val("n"), 3)
		eq(t, d.Val("tags"), []any{"a", "b", "c"})
		if d.Has("gone") {
			t.Fatal("gone")
		}
		eq(t, d.Map("nested").Val("c"), 2)
		eq(t, d.Val("fresh"), 5)
		at, ok := d.Val("at").(doc.Timestamp)
		if !ok || time.Since(at.Time()) > time.Minute {
			t.Fatal(d.Val("at"))
		}
		must(t, ref.Set(doc.Obj("tags", doc.ArrayRemove("a"), "n", doc.Increment(1)), Merge))
		d = data(t, ref)
		eq(t, d.Val("tags"), []any{"b", "c"})
		eq(t, d.Val("n"), 4)
	})
	t.Run("create / update preconditions", func(t *testing.T) {
		ref := st.Doc("conformance/rx/docs/once")
		must(t, ref.Create(doc.Obj("v", 1)))
		code(t, ref.Create(doc.Obj("v", 2)), AlreadyExists)
		eq(t, data(t, ref).Val("v"), 1)
		m := st.Doc("conformance/rx/docs/missing")
		code(t, m.Update(doc.Obj("v", 1)), NotFound)
		if s, _ := m.Get(); s.Exists() {
			t.Fatal("created")
		}
	})
	t.Run("timestamp + bytes", func(t *testing.T) {
		ref := st.Doc("conformance/rx/docs/types")
		ts := doc.Timestamp{Seconds: 1_700_000_000, Nanos: 123_000_000}
		must(t, ref.Set(doc.Obj("ts", ts, "bytes", doc.Bytes{0, 1, 2, 255}, "nested", doc.Obj("list", []any{ts, doc.Bytes("hi")}), "n", nil, "f", 1.5, "neg", -3, "s", "ü€")))
		d := data(t, ref)
		if d.Val("ts") != ts {
			t.Fatal(d.Val("ts"))
		}
		if !reflect.DeepEqual(d.Val("bytes"), doc.Bytes{0, 1, 2, 255}) {
			t.Fatal(d.Val("bytes"))
		}
		l := d.Map("nested").Arr("list")
		if l[0] != ts || string(l[1].(doc.Bytes)) != "hi" {
			t.Fatal(l)
		}
		eq(t, d.Val("n"), nil)
		eq(t, d.Val("f"), 1.5)
		eq(t, d.Val("neg"), -3)
		eq(t, d.Val("s"), "ü€")
	})
	t.Run("getAll", func(t *testing.T) {
		must(t, st.Doc("conformance/rx/docs/g1").Set(doc.Obj("i", 1)))
		r, err := st.GetAll(st.Doc("conformance/rx/docs/g1"), st.Doc("conformance/rx/docs/nope"))
		must(t, err)
		if !r[0].Exists() || r[1].Exists() {
			t.Fatal("order")
		}
	})
}

func seedItems(t *testing.T, st *Store) *CollectionRef {
	items := st.Collection("conformance/rx/items")
	b := st.Batch()
	for i := 0; i < 10; i++ {
		tags := []any{"all"}
		if i%3 == 0 {
			tags = []any{"three", "all"}
		}
		grp := "hi"
		if i < 5 {
			grp = "lo"
		}
		var mixed any = fmt.Sprint(i)
		if i < 5 {
			mixed = i
		}
		b.Set(items.Doc(fmt.Sprintf("d%d", i)), doc.Obj("n", i, "s", fmt.Sprintf("s%d", i), "b", i%2 == 0, "ts", doc.TimestampFromMillis(1_700_000_000_000+int64(i)*1000), "grp", grp, "tags", tags, "mixed", mixed, "nested", doc.Obj("x", 10-i)))
	}
	must(t, b.Commit())
	return items
}

func TestQueries(t *testing.T) {
	p := confPorts(t)
	st := p.Store
	items := seedItems(t, st)
	type A = []string
	t.Run("filters", func(t *testing.T) {
		eq(t, ids(t, items.Where("n", "==", 3)), A{"d3"})
		eq(t, ids(t, items.Where("s", "==", "s4")), A{"d4"})
		eq(t, ids(t, items.Where("b", "==", true)), A{"d0", "d2", "d4", "d6", "d8"})
		eq(t, ids(t, items.Where("n", "in", []any{1, 5, 42})), A{"d1", "d5"})
		eq(t, ids(t, items.Where("tags", "array-contains", "three")), A{"d0", "d3", "d6", "d9"})
		eq(t, ids(t, items.Where("n", "<", 2)), A{"d0", "d1"})
		eq(t, ids(t, items.Where("n", "<=", 2)), A{"d0", "d1", "d2"})
		eq(t, ids(t, items.Where("n", ">", 7)), A{"d8", "d9"})
		eq(t, ids(t, items.Where("n", ">=", 7)), A{"d7", "d8", "d9"})
		eq(t, ids(t, items.Where("nested.x", ">=", 9)), A{"d1", "d0"})
		eq(t, ids(t, items.Where("ts", ">", doc.TimestampFromMillis(1_700_000_007_500))), A{"d8", "d9"})
		eq(t, ids(t, items.Where("grp", "==", "lo").Where("n", ">", 2)), A{"d3", "d4"})
	})
	t.Run("typed", func(t *testing.T) {
		eq(t, ids(t, items.Where("mixed", "==", 1)), A{"d1"})
		eq(t, ids(t, items.Where("mixed", "==", "1")), A{})
		eq(t, ids(t, items.Where("mixed", "==", "7")), A{"d7"})
		eq(t, ids(t, items.Where("mixed", ">", 2)), A{"d3", "d4"})
		eq(t, ids(t, items.Where("mixed", ">=", "")), A{"d5", "d6", "d7", "d8", "d9"})
		eq(t, ids(t, items.Where("n", ">", "")), A{})
		eq(t, ids(t, items.Where("b", "==", 1)), A{})
	})
	t.Run("orderBy", func(t *testing.T) {
		eq(t, ids(t, items.OrderBy("n", Desc).Limit(3)), A{"d9", "d8", "d7"})
		eq(t, ids(t, items.OrderBy("grp").OrderBy("n", Desc)), A{"d9", "d8", "d7", "d6", "d5", "d4", "d3", "d2", "d1", "d0"})
		eq(t, ids(t, items.OrderBy("grp", Asc)), A{"d5", "d6", "d7", "d8", "d9", "d0", "d1", "d2", "d3", "d4"})
		eq(t, ids(t, items.OrderBy("grp", Desc)), A{"d4", "d3", "d2", "d1", "d0", "d9", "d8", "d7", "d6", "d5"})
		eq(t, ids(t, items.Query), A{"d0", "d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8", "d9"})
		eq(t, ids(t, items.Where("tags", "array-contains", "all").OrderBy("ts", Desc).Limit(2)), A{"d9", "d8"})
	})
	t.Run("startAfter", func(t *testing.T) {
		q := items.OrderBy("grp").OrderBy("n", Desc)
		p1, err := q.Limit(4).Get()
		must(t, err)
		eq(t, ids(t, q.Limit(4)), A{"d9", "d8", "d7", "d6"})
		eq(t, ids(t, q.StartAfterSnap(p1.Docs[3]).Limit(4)), A{"d5", "d4", "d3", "d2"})
		snap, _ := items.Doc("d2").Get()
		eq(t, ids(t, q.StartAfterSnap(snap)), A{"d1", "d0"})
		eq(t, ids(t, items.OrderBy("n").StartAfter(6)), A{"d7", "d8", "d9"})
		eq(t, ids(t, items.OrderBy("n", Desc).StartAfter(2)), A{"d1", "d0"})
		eq(t, ids(t, q.StartAfter("hi", 7).Limit(2)), A{"d6", "d5"})
		eq(t, ids(t, items.OrderBy("grp").StartAfter("hi")), A{"d0", "d1", "d2", "d3", "d4"})
		eq(t, ids(t, items.OrderBy("ts").StartAfter(doc.TimestampFromMillis(1_700_000_008_000))), A{"d9"})
		g := items.OrderBy("grp")
		a, _ := g.Limit(2).Get()
		eq(t, ids(t, g.StartAfterSnap(a.Docs[1]).Limit(2)), A{"d7", "d8"})
	})
	t.Run("select", func(t *testing.T) {
		s, err := items.Where("n", "==", 4).Select("n", "nested.x").Get()
		must(t, err)
		eq(t, s.Docs[0].Data(), doc.Obj("n", 4, "nested", doc.Obj("x", 6)))
		e, err := items.Where("n", "<", 2).Select().Get()
		must(t, err)
		if e.Size() != 2 || e.Docs[0].Data().Len() != 0 || e.Empty() {
			t.Fatal("existence only")
		}
	})
	t.Run("collectionGroup", func(t *testing.T) {
		w := st.Batch()
		w.Set(st.Doc("conformance/rx/a/p1/cgItems/x1"), doc.Obj("run", "rx", "n", 3))
		w.Set(st.Doc("conformance/rx/a/p2/cgItems/x2"), doc.Obj("run", "rx", "n", 1))
		w.Set(st.Doc("conformance/rx/b/p3/cgItems/x3"), doc.Obj("run", "rx", "n", 2))
		w.Set(st.Doc("deployments/cfrx/cgItems/x4"), doc.Obj("run", "rx", "n", 0))
		w.Set(st.Doc("deployments/cfrxx/cgItems/x5"), doc.Obj("run", "rx", "n", 4))
		w.Set(st.Doc("conformance/rx/a/p1/notCg/x9"), doc.Obj("run", "rx", "n", 9))
		must(t, w.Commit())
		cg := st.CollectionGroup("cgItems").Where("run", "==", "rx")
		all := ids(t, cg)
		sort.Strings(all)
		eq(t, all, A{"x1", "x2", "x3", "x4", "x5"})
		eq(t, ids(t, cg.OrderBy("n").Limit(3)), A{"x4", "x2", "x3"})
		page1, err := cg.OrderBy("n", Desc).Limit(2).Get()
		must(t, err)
		eq(t, ids(t, cg.OrderBy("n", Desc).Limit(2)), A{"x5", "x1"})
		eq(t, ids(t, cg.OrderBy("n", Desc).StartAfterSnap(page1.Docs[1])), A{"x3", "x2", "x4"})
		eq(t, len(ids(t, cg.Where("n", ">=", 2))), 3)
		s, _ := cg.Get()
		for _, d := range s.Docs {
			if d.ID() == "x4" {
				eq(t, d.Ref().Path, "deployments/cfrx/cgItems/x4")
			}
		}
	})
}

func TestTransactions(t *testing.T) {
	p := confPorts(t)
	st := p.Store
	items := seedItems(t, st)
	t.Run("N parallel increments", func(t *testing.T) {
		ref := st.Doc("conformance/rx/counters/c")
		must(t, ref.Set(doc.Obj("v", 0)))
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := st.RunTransaction(func(tx *Tx) error {
					s, err := tx.Get(ref)
					if err != nil {
						return err
					}
					tx.Update(ref, doc.Obj("v", s.Data().Num("v")+1))
					return nil
				}, 50); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		eq(t, data(t, ref).Val("v"), 10)
	})
	t.Run("read after write", func(t *testing.T) {
		ref := st.Doc("conformance/rx/counters/rw")
		err := st.RunTransaction(func(tx *Tx) error {
			tx.Set(ref, doc.Obj("v", 1))
			_, err := tx.Get(ref)
			return err
		})
		if err == nil {
			t.Fatal("expected an error")
		}
		if s, _ := ref.Get(); s.Exists() {
			t.Fatal("written")
		}
	})
	t.Run("tx query + getAll + failing write", func(t *testing.T) {
		r, err := RunTx(st, func(tx *Tx) (int, error) {
			q, err := tx.Query(items.Where("grp", "==", "hi"))
			if err != nil {
				return 0, err
			}
			g, err := tx.GetAll(items.Doc("d0"), items.Doc("nope"))
			if err != nil {
				return 0, err
			}
			tx.Set(st.Doc("conformance/rx/counters/fromTx"), doc.Obj("size", q.Size(), "a", g[0].Exists(), "b", g[1].Exists()))
			return q.Size(), nil
		})
		must(t, err)
		eq(t, r, 5)
		eq(t, data(t, st.Doc("conformance/rx/counters/fromTx")), doc.Obj("size", 5, "a", true, "b", false))
		must(t, st.Doc("conformance/rx/counters/exists").Set(doc.Obj("v", 1)))
		err = st.RunTransaction(func(tx *Tx) error {
			tx.Set(st.Doc("conformance/rx/counters/notWritten"), doc.Obj("v", 1))
			tx.Create(st.Doc("conformance/rx/counters/exists"), doc.Obj("v", 2))
			return nil
		})
		if err == nil {
			t.Fatal("expected an error")
		}
		if s, _ := st.Doc("conformance/rx/counters/notWritten").Get(); s.Exists() {
			t.Fatal("written")
		}
	})
	t.Run("batch atomic", func(t *testing.T) {
		b := st.Batch()
		b.Set(st.Doc("conformance/rx/batch/one"), doc.Obj("v", 1))
		b.Update(st.Doc("conformance/rx/batch/missing"), doc.Obj("v", 1))
		code(t, b.Commit(), NotFound)
		if s, _ := st.Doc("conformance/rx/batch/one").Get(); s.Exists() {
			t.Fatal("written")
		}
		ok := st.Batch()
		ok.Set(st.Doc("conformance/rx/batch/one"), doc.Obj("v", 1)).Set(st.Doc("deployments/cfrx/batch/two"), doc.Obj("v", 2))
		ok.Delete(st.Doc("conformance/rx/batch/none"))
		must(t, ok.Commit())
		eq(t, data(t, st.Doc("deployments/cfrx/batch/two")).Val("v"), 2)
	})
	t.Run("bulkWriter", func(t *testing.T) {
		w := st.NewBulkWriter()
		w.OnWriteError(func(error, int) bool { return false })
		a := w.Create(st.Doc("conformance/rx/bulk/a"), doc.Obj("v", 1))
		must(t, st.Doc("conformance/rx/bulk/b").Set(doc.Obj("v", 0)))
		dup := w.Create(st.Doc("conformance/rx/bulk/b"), doc.Obj("v", 1))
		w.Close()
		must(t, a.Wait())
		if dup.Wait() == nil {
			t.Fatal("expected a conflict")
		}
		eq(t, data(t, st.Doc("conformance/rx/bulk/a")).Val("v"), 1)
		eq(t, data(t, st.Doc("conformance/rx/bulk/b")).Val("v"), 0)
	})
}

func TestBlobsConformance(t *testing.T) {
	p := confPorts(t)
	b := p.Blobs
	const P = "conformance/rx"
	body := []byte("0123456789abcdef")
	must(t, b.Put(P+"/a.bin", body, PutOptions{ContentType: "application/x-test", Meta: map[string]string{"opmSha256": "abc"}}))
	got, err := b.Get(P + "/a.bin")
	must(t, err)
	eq(t, string(got), string(body))
	if x, _ := b.Get(P + "/missing"); x != nil {
		t.Fatal("missing")
	}
	if x, _ := b.Head(P + "/missing"); x != nil {
		t.Fatal("missing")
	}
	h, err := b.Head(P + "/a.bin")
	must(t, err)
	eq(t, h.Size, 16)
	eq(t, h.ContentType, "application/x-test")
	eq(t, h.Meta["opmSha256"], "abc")
	if h.CreatedMs <= time.Now().UnixMilli()-600_000 {
		t.Fatal(h.CreatedMs)
	}
	must(t, b.SetMeta(P+"/a.bin", map[string]*string{"extra": Ptr("1")}))
	h, _ = b.Head(P + "/a.bin")
	eq(t, h.Meta["extra"], "1")
	read := func(r *[2]int64) string {
		s, err := b.Stream(P+"/a.bin", r)
		must(t, err)
		defer s.Close()
		x, _ := io.ReadAll(s)
		return string(x)
	}
	eq(t, read(&[2]int64{2, 5}), "2345")
	eq(t, read(nil), string(body))
	w, err := b.WriteStream(P+"/dir/s.txt", PutOptions{ContentType: "text/plain"})
	must(t, err)
	_, _ = w.Write([]byte("hello "))
	_, _ = w.Write([]byte("world"))
	must(t, w.Close())
	got, _ = b.Get(P + "/dir/s.txt")
	eq(t, string(got), "hello world")
	must(t, b.Put(P+"/dir/part-1", []byte("1"), PutOptions{}))
	must(t, b.Put(P+"/dir/part-2", []byte("22"), PutOptions{}))
	must(t, b.Copy(P+"/a.bin", P+"/copy.bin"))
	h, _ = b.Head(P + "/copy.bin")
	eq(t, h.ContentType, "application/x-test")
	l, err := b.List(P + "/")
	must(t, err)
	var names []string
	for _, x := range l {
		names = append(names, x.Path[len(P)+1:])
	}
	eq(t, names, []string{"a.bin", "copy.bin", "dir/part-1", "dir/part-2", "dir/s.txt"})
	parts, _ := b.List(P + "/dir/part-")
	if len(parts) != 2 || parts[0].Path != P+"/dir/part-1" || parts[0].Size != 1 || parts[1].Path != P+"/dir/part-2" || parts[1].Size != 2 {
		t.Fatal(parts)
	}
	must(t, b.DeletePrefix(P+"/dir/"))
	l, _ = b.List(P + "/dir/")
	eq(t, len(l), 0)
	must(t, b.Delete(P+"/copy.bin"))
	must(t, b.Delete(P+"/copy.bin"))
	if x, _ := b.Get(P + "/copy.bin"); x != nil {
		t.Fatal("deleted")
	}
}

// blobReq calls ServeBlob like the HTTP server does.
func blobReq(b *BlobStore, url string, hdr map[string]string, method string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://opm.test"+url, nil)
	r.RequestURI = url
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	ServeBlob(w, r, b, []string{"http://console.test"})
	return w
}

func TestLocalBlobHTTP(t *testing.T) {
	p := open(t, t.TempDir())
	b := p.Blobs
	body := "abcdefghijklmnopqrstuvwxyz"
	must(t, b.Put("files/abc", []byte(body), PutOptions{ContentType: "text/plain", CacheControl: "private, max-age=60"}))
	url, err := b.SignedReadURL("files/abc", time.Minute, `attachment; filename="a b.txt"`)
	must(t, err)
	if !regexp.MustCompile(`^http://opm\.test/base/_blob/files/abc\?exp=\d+&sig=[A-Za-z0-9_-]+&rd=`).MatchString(url) {
		t.Fatal(url)
	}
	rel := strings.TrimPrefix(url, "http://opm.test")
	org := map[string]string{"Origin": "http://console.test"}
	r := blobReq(b, rel, org, "GET")
	eq(t, r.Code, 200)
	eq(t, r.Body.String(), body)
	h := r.Result().Header
	get := func(k string) string { return strings.Join(r.Header()[k], ",") }
	_ = h
	eq(t, get("Content-Length"), "26")
	eq(t, get("Content-Type"), "text/plain")
	eq(t, get("Content-Disposition"), `attachment; filename="a b.txt"`)
	eq(t, get("Cache-Control"), "private, max-age=60")
	eq(t, get("Access-Control-Allow-Origin"), "http://console.test")
	if !strings.Contains(get("Access-Control-Expose-Headers"), "Content-Range") {
		t.Fatal("expose")
	}
	r = blobReq(b, rel, map[string]string{"Origin": "http://evil.test"}, "GET")
	eq(t, get("Access-Control-Allow-Origin"), "")
	r = blobReq(b, rel, nil, "HEAD")
	eq(t, r.Code, 200)
	eq(t, r.Body.Len(), 0)
	r = blobReq(b, rel, map[string]string{"Range": "bytes=2-4"}, "GET")
	eq(t, r.Code, 206)
	eq(t, get("Content-Range"), "bytes 2-4/26")
	eq(t, r.Body.String(), "cde")
	r = blobReq(b, rel, map[string]string{"Range": "bytes=-3"}, "GET")
	eq(t, r.Body.String(), "xyz")
	r = blobReq(b, rel, map[string]string{"Range": "bytes=20-"}, "GET")
	eq(t, r.Body.String(), "uvwxyz")
	r = blobReq(b, rel, map[string]string{"Range": "bytes=26-30"}, "GET")
	eq(t, r.Code, 416)
	eq(t, get("Content-Range"), "bytes */26")
	eq(t, blobReq(b, strings.Replace(rel, "files/abc", "files/abd", 1), nil, "GET").Code, 403)
	idx := strings.Index(rel, "rd=")
	eq(t, blobReq(b, rel[:idx]+"rd=inline", nil, "GET").Code, 403)
	otherExp := regexp.MustCompile(`exp=\d+`).ReplaceAllStringFunc(rel, func(e string) string {
		n, _ := strconv.ParseInt(e[4:], 10, 64)
		return "exp=" + strconv.FormatInt(n+1, 10)
	})
	eq(t, blobReq(b, otherExp, nil, "GET").Code, 403)
	old, _ := b.SignedReadURL("files/abc", -time.Second, "")
	eq(t, blobReq(b, strings.TrimPrefix(old, "http://opm.test"), nil, "GET").Code, 403)
	eq(t, blobReq(b, rel, org, "OPTIONS").Code, 204)
	eq(t, blobReq(b, "/base/_blob/..%2Fsecrets?exp=1&sig=x", nil, "GET").Code, 400)
	eq(t, blobReq(b, rel, nil, http.MethodPost).Code, 405)
}

func TestBlobPathsSweepStreams(t *testing.T) {
	d := t.TempDir()
	p := open(t, d)
	b := p.Blobs
	for _, x := range []string{"../x", "/abs", "a//b", "a/./b", ""} {
		if b.Put(x, []byte("x"), PutOptions{}) == nil {
			t.Fatal(x)
		}
	}
	must(t, b.Put("staging/old", []byte("1"), PutOptions{}))
	must(t, b.Put("staging/new", []byte("2"), PutOptions{}))
	must(t, b.Put("blobs/sha256/aa", []byte("3"), PutOptions{}))
	side := d + "/blobmeta/staging/old.json"
	raw, _ := os.ReadFile(side)
	m, _ := doc.ParseMap(raw)
	m.Set("createdMs", time.Now().UnixMilli()-2*86_400_000)
	must(t, os.WriteFile(side, []byte(doc.MarshalString(m)), 0o644))
	paths := func(l []ListedBlob) []string {
		out := []string{}
		for _, x := range l {
			out = append(out, x.Path)
		}
		return out
	}
	l, _ := b.List("")
	eq(t, paths(l), []string{"blobs/sha256/aa", "staging/new", "staging/old"})
	n, err := b.Sweep([]LifecycleRule{{[]string{"staging/"}, 1}}, time.Now().UnixMilli())
	must(t, err)
	eq(t, n, 1)
	l, _ = b.List("staging/")
	eq(t, paths(l), []string{"staging/new"})
	w, err := b.WriteStream("transfers/D/t1", PutOptions{})
	must(t, err)
	_, _ = w.Write([]byte("partial"))
	w.Abort()
	if h, _ := b.Head("transfers/D/t1"); h != nil {
		t.Fatal("visible")
	}
	e, _ := os.ReadDir(d + "/tmp")
	eq(t, len(e), 0)
	_, err = b.SignedReadURL("staging/new", time.Second, "")
	must(t, err)
	st, err := os.Stat(d + "/secrets/blob-hmac.key")
	must(t, err)
	eq(t, int(st.Mode().Perm()), 0o600)
}
