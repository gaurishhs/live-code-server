package eventapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc, suffix string) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := NewClient(s.URL+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClaimTrailingSlashAndAuth(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/claim" {
			t.Errorf("path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer event-pass" {
			t.Errorf("auth header %q", got)
		}
		fmt.Fprint(w, `{"lease":"lease-secret","expiresAt":1893456000000,"tunnelToken":"tunnel-secret","publicUrl":"https://ws.example.test"}`)
	}, "/")
	got, err := c.Claim(context.Background(), "event-pass")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lease != "lease-secret" || got.TunnelToken != "tunnel-secret" || got.PublicURL != "https://ws.example.test" {
		t.Fatalf("claim %#v", got)
	}
}

func TestClaimStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{401, ErrInvalidPassword}, {409, ErrSlotOccupied}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"message":"secret response"}`)
			}, "")
			_, err := c.Claim(context.Background(), "pw")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
			if strings.Contains(err.Error(), "secret response") {
				t.Fatal("unsafe response leaked")
			}
		})
	}
}

func TestRenewAndRelease(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer lease-value" {
			t.Errorf("wrong lease header")
		}
		switch r.URL.Path {
		case "/renew":
			fmt.Fprint(w, `{"ok":true,"expiresAt":1893456000000}`)
		case "/release":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}, "")
	if _, err := c.Renew(context.Background(), "lease-value"); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(context.Background(), "lease-value"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

func TestRenewUnauthorizedAndMalformedClaim(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/renew" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"lease":"partial"}`)
	}, "")
	if _, err := c.Renew(context.Background(), "lease"); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("renew: %v", err)
	}
	_, err := c.Claim(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("claim: %v", err)
	}
}

func TestMalformedAndUnavailable(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "not-json") }, "")
	_, err := c.Claim(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed: %v", err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	c, _ = NewClient(s.URL, nil)
	s.Close()
	_, err = c.Claim(context.Background(), "pw")
	if err == nil || !strings.Contains(err.Error(), "unable to contact livecode event service") {
		t.Fatalf("offline: %v", err)
	}
}
