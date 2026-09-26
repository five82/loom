package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/library"
)

func TestCatalogAndControlEndpoints(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	t.Cleanup(func() { _ = catalog.Close() })
	scans := library.NewManager(nil, 0, slog.Default())
	shutdown := make(chan struct{}, 1)
	api := New(catalog, scans, nil, channels.New(catalog), shutdown, ListenAddresses{})
	id := strconv.FormatInt(itemID, 10)
	for _, tc := range []struct {
		name, method, path, body string
		local                    bool
		status                   int
		contains                 string
	}{
		{"health", "GET", "/api/v1/health", "", false, 200, `"ok"`},
		{"libraries", "GET", "/api/v1/libraries", "", false, 200, `"items"`},
		{"children", "GET", "/api/v1/items/" + id + "/children?limit=1&offset=0", "", false, 200, `"items"`},
		{"children invalid ID", "GET", "/api/v1/items/no/children", "", false, 400, "positive integer"},
		{"children missing", "GET", "/api/v1/items/99999/children", "", false, 404, "item not found"},
		{"children pagination", "GET", "/api/v1/items/" + id + "/children?limit=0", "", false, 400, "limit"},
		{"continue watching", "GET", "/api/v1/continue-watching?limit=2", "", false, 200, `"items"`},
		{"next up", "GET", "/api/v1/next-up?limit=2", "", false, 200, `"items"`},
		{"recently added", "GET", "/api/v1/recently-added?limit=2", "", false, 200, `"items"`},
		{"recently played", "GET", "/api/v1/recently-played?limit=2", "", false, 200, `"items"`},
		{"bad continue watching", "GET", "/api/v1/continue-watching?limit=0", "", false, 400, "limit"},
		{"bad next up", "GET", "/api/v1/next-up?limit=201", "", false, 400, "limit"},
		{"bad recently added", "GET", "/api/v1/recently-added?limit=x", "", false, 400, "limit"},
		{"bad recently played", "GET", "/api/v1/recently-played?limit=0", "", false, 400, "limit"},
		{"status", "GET", "/_loom/status", "", true, 200, `"running":true`},
		{"unmatched", "GET", "/_loom/unmatched", "", true, 200, `"items"`},
		{"metadata search disabled", "GET", "/_loom/metadata/search?type=movie&query=test", "", true, 503, "disabled"},
		{"metadata match disabled", "POST", "/_loom/metadata/match", `{"item_id":1,"tmdb_id":2}`, true, 503, "disabled"},
		{"scan invalid library", "POST", "/api/v1/scan", `{"library":"unknown"}`, false, 400, "library"},
		{"scan bad JSON", "POST", "/api/v1/scan", `{"extra":true}`, false, 400, "invalid scan body"},
		{"scan movies", "POST", "/api/v1/scan", `{"library":"movies"}`, false, 202, "started"},
		{"scan already queued", "POST", "/api/v1/scan", `{"library":"movies"}`, false, 409, "already running"},
		{"scan status", "GET", "/api/v1/scan", "", false, 200, `"running":true`},
		{"stop", "POST", "/_loom/stop", "", true, 202, "stopping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := api.PublicHandler()
			if tc.local {
				handler = api.LocalHandler()
			}
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.contains) {
				t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.path, response.Code, response.Body.String())
			}
		})
	}
	select {
	case <-shutdown:
	default:
		t.Fatal("stop did not signal shutdown")
	}
}

func TestChildrenOfShowAndUnmatchedCatalog(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/items/"+strconv.FormatInt(itemID, 10)+"/children", nil)
	response := httptest.NewRecorder()
	api.PublicHandler().ServeHTTP(response, req)
	var result struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(bytes.NewReader(response.Body.Bytes())).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(result.Items) != 0 {
		t.Fatalf("children response = %d %s", response.Code, response.Body.String())
	}
}
