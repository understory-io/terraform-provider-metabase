package metabase

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClient_SendsAPIKeyAndMapsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("X-Api-Key = %q", r.Header.Get("X-Api-Key"))
		}
		if r.Header.Get("User-Agent") != "ua/1" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		http.Error(w, "Not found.", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL+"/", "k", WithUserAgent("ua/1")).GetCollection(context.Background(), "7")
	if !IsNotFound(err) {
		t.Fatalf("want a not-found APIError, got %v", err)
	}
	if IsConflict(err) {
		t.Fatal("404 reported as conflict")
	}
}

func TestClient_MemoisesGetsUntilAWrite(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			_, _ = io.WriteString(w, `{"revision": 1, "groups": {}}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	ctx := context.Background()

	for range 3 {
		if _, err := c.GetCollectionGraph(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if gets.Load() != 1 {
		t.Fatalf("three reads made %d requests, want 1", gets.Load())
	}
	if err := c.RenameGroup(ctx, 3, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetCollectionGraph(ctx); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 2 {
		t.Fatalf("a read after a write must go to the server; requests = %d", gets.Load())
	}
}

// The graph write must send only the cells that differ, with the revision of
// the graph it diffed against, and on 409 start over from a fresh read.
func TestModifyCollectionGraph_RetriesOnConflictWithFreshRevision(t *testing.T) {
	revision := 10
	var puts []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"revision": revision, "groups": map[string]any{"3": map[string]string{"5": "read"}}})
		case http.MethodPut:
			if r.URL.Query().Get("force") != "" {
				t.Error("force must never be sent")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			puts = append(puts, body)
			if len(puts) == 1 {
				// Someone else wrote between our GET and PUT.
				revision = 11
				http.Error(w, "out of date", http.StatusConflict)
				return
			}
			_, _ = io.WriteString(w, `{"revision": 12}`)
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "k").ModifyCollectionGraph(context.Background(), func(g *CollectionGraph) (map[string]map[string]string, error) {
		if g.Access("3", "5") == "write" {
			return nil, nil
		}
		return map[string]map[string]string{"3": {"5": "write"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(puts) != 2 {
		t.Fatalf("want 2 PUTs (conflict, retry), got %d", len(puts))
	}
	if puts[0]["revision"].(float64) != 10 || puts[1]["revision"].(float64) != 11 {
		t.Errorf("revisions sent: %v then %v, want 10 then 11", puts[0]["revision"], puts[1]["revision"])
	}
}

func TestModifyCollectionGraph_NoChangeWritesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s", r.Method)
		}
		_, _ = io.WriteString(w, `{"revision": 1, "groups": {}}`)
	}))
	defer srv.Close()
	err := New(srv.URL, "k").ModifyCollectionGraph(context.Background(), func(*CollectionGraph) (map[string]map[string]string, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestModifyDataGraph_GivesUpAfterRepeatedConflicts(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"revision": 1, "groups": {}}`)
			return
		}
		puts++
		http.Error(w, "out of date", http.StatusConflict)
	}))
	defer srv.Close()
	err := New(srv.URL, "k").ModifyDataGraph(context.Background(), func(*DataGraph) (map[string]map[string]map[string]any, error) {
		return map[string]map[string]map[string]any{"3": {"1": {"create-queries": "no"}}}, nil
	})
	if !IsConflict(err) {
		t.Fatalf("want the final 409, got %v", err)
	}
	if puts != maxGraphAttempts {
		t.Fatalf("PUTs = %d, want %d", puts, maxGraphAttempts)
	}
}

// Personal collections, and shared-looking collections nested inside them,
// are not the provider's.
func TestListCollections_ExcludesPersonalTrees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("archived") == "true" {
			_, _ = io.WriteString(w, `[{"id": 20, "name": "old", "location": "/4/", "archived": true}]`)
			return
		}
		_, _ = io.WriteString(w, `[
			{"id": "root", "name": "Our analytics"},
			{"id": 3, "name": "someone's", "location": "/", "personal_owner_id": 9},
			{"id": 4, "name": "Shared", "location": "/"},
			{"id": 5, "name": "In shared", "location": "/4/"},
			{"id": 6, "name": "In personal", "location": "/3/"},
			{"id": 7, "name": "Snippets", "location": "/", "namespace": "snippets"},
			{"id": 1, "name": "Trash", "location": "/", "type": "trash"}
		]`)
	}))
	defer srv.Close()
	cols, err := New(srv.URL, "k").ListCollections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, c := range cols {
		ids = append(ids, c.ID)
	}
	want := []int{4, 5, 20}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

func TestCollection_ParentFromLocation(t *testing.T) {
	for loc, want := range map[string]int{"/": 0, "/12/": 12, "/12/34/": 34} {
		got := Collection{Location: loc}.ParentID()
		if (got == nil) != (want == 0) || (got != nil && *got != want) {
			t.Errorf("ParentID(%q) = %v, want %d", loc, got, want)
		}
	}
}
