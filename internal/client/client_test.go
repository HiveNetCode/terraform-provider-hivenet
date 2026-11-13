package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL+"/v1/", "secret-token", "test-agent")
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestListInstancesFollowsCursor(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("free_text_search"); got != "tf-acc-" {
			t.Errorf("free_text_search = %q", got)
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"instance_id":"a","name":"tf-acc-1","status":"RUNNING"}],"pagination":{"next":"c2","size":1}}`))
		case "c2":
			_, _ = w.Write([]byte(`{"data":[{"instance_id":"b","name":"tf-acc-2","status":"STOPPED"}],"pagination":{"next":null,"size":1}}`))
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	})

	got, err := c.ListInstances(t.Context(), ListInstancesOptions{FreeTextSearch: "tf-acc-"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("got %+v", got)
	}
}

func TestRetriesOn429(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"instance_id":"a","status":"TERMINATING"}`))
	})

	if err := c.TerminateInstance(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestErrorEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT","message":"Conflict.","request_id":"r1"}}`))
	})

	err := c.TerminateInstance(t.Context(), "a")
	if !IsStatus(err, http.StatusConflict) {
		t.Fatalf("err = %v, want 409", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "CONFLICT" || apiErr.RequestID != "r1" {
		t.Fatalf("got %+v", apiErr)
	}
}

func TestGetInstance(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/instances/a":
			_, _ = w.Write([]byte(`{"instance_id":"a","name":"web","status":8,"cpu_count":4,"guest_ready":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"Not found.","request_id":"r2"}}`))
		}
	})

	got, err := c.GetInstance(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web" || got.Status != "STOPPED" || got.CPUCount != 4 || got.GuestReady != "READY" {
		t.Fatalf("got %+v", got)
	}
	if _, err := c.GetInstance(t.Context(), "missing"); !IsStatus(err, http.StatusNotFound) {
		t.Fatalf("err = %v, want 404", err)
	}
}

func TestListRegions(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/regions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"name":"UAE"},{"name":"FR"}]`))
	})

	got, err := c.ListRegions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "FR" {
		t.Fatalf("got %+v", got)
	}
}

func TestListPresetsFiltersAndFollowsCursor(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("region") != "FR" || q.Get("sku") != "" || q.Get("free_text_search") != "RTX 4090" {
			t.Errorf("query = %v", q)
		}
		switch q.Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","hourly_price":"3.2000"}],"pagination":{"next":"c2","size":1}}`))
		case "c2":
			_, _ = w.Write([]byte(`{"data":[{"id":"p2","price_discounted":""}],"pagination":{"next":null,"size":1}}`))
		default:
			t.Errorf("unexpected cursor %q", q.Get("cursor"))
		}
	})

	got, err := c.ListPresets(t.Context(), ListPresetsOptions{Region: "FR", FreeTextSearch: "RTX 4090"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].HourlyPrice != "3.2000" || got[1].ID != "p2" {
		t.Fatalf("got %+v", got)
	}
}
