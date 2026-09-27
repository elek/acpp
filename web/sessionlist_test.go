package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/elek/acpp/db"
)

func getSessions(t *testing.T, s *Server, query string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sessions"+query, nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestListSessionsPaginates(t *testing.T) {
	store := db.NewMemStore()
	ctx := context.Background()
	base := time.Now()
	total := sessionsPerPage + 5
	for i := 0; i < total; i++ {
		// Dir encodes creation order; newest (highest i) is listed first.
		if err := store.InsertSession(ctx, fmt.Sprintf("s%03d", i), "web", "agent", fmt.Sprintf("/dir-%03d", i), "", "", "", "", nil, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	s := New(store, ":0")

	first := getSessions(t, s, "")
	if !strings.Contains(first, fmt.Sprintf("/dir-%03d", total-1)) {
		t.Error("page 1 should contain the newest session")
	}
	if strings.Contains(first, "/dir-000<") {
		t.Error("page 1 should not contain the oldest session")
	}
	if !strings.Contains(first, "Page 1 of 2") || !strings.Contains(first, `href="/sessions?page=2"`) {
		t.Error("page 1 should show the pager with a link to page 2")
	}

	second := getSessions(t, s, "?page=2")
	if got := strings.Count(second, "?view=summary"); got != 5 {
		t.Errorf("page 2 has %d rows, want 5", got)
	}
	if !strings.Contains(second, "/dir-000<") {
		t.Error("page 2 should contain the oldest session")
	}

	if beyond := getSessions(t, s, "?page=9"); !strings.Contains(beyond, "No sessions found.") {
		t.Error("a page past the end should render empty")
	}
}
