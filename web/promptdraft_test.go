package web

import (
	"net/http"
	"strings"
	"testing"
)

// The draft storage layer lives in a shared {{define}} (templates/promptdraft.html)
// because both pages carrying a prompt bar need it and there is no static JS
// bundle to put it in. A `{{template}}` reference to a name that does not exist
// fails at *execution* time, not at ParseFS, so a page that lost the include (or
// a rename of the define) would still build and still serve — with a prompt bar
// that throws on the first keystroke. These render the real pages and check the
// wiring is there.
func TestPromptDraftScriptIsIncluded(t *testing.T) {
	store := seedStore(t)
	s := New(store, ":0").WithProjects(store)

	pages := map[string]string{
		// s1 is the running session, so its prompt bar is rendered at all.
		"session view": "/session/s1",
		"project view": "/projects?project=acpp&session=s1",
	}
	for name, path := range pages {
		t.Run(name, func(t *testing.T) {
			rec := doGet(t, s, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, "window.acppDraft") {
				t.Error("draft storage layer missing: the promptDraftScript include is gone")
			}
			// The page must also still use it; the include alone is inert.
			if !strings.Contains(body, "acppDraft.read(") {
				t.Error("page never restores a draft")
			}
			if !strings.Contains(body, "acppDraft.save(") {
				t.Error("page never saves a draft")
			}
			if !strings.Contains(body, "acppDraft.clear(") {
				t.Error("page never clears a draft on submit")
			}
		})
	}
}
