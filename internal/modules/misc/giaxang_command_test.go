package misc

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// petrolimexFixture mirrors the live response shape: records arrive sorted by
// LastModified, not display order, and may include entries with no price.
const petrolimexFixture = `{"Objects":[
	{"Title":"DO 0,001S-V","Zone1Price":32090,"Zone2Price":32730,"DIsplayOrder":6,"LastModified":"2026-09-24T07:48:49.563Z"},
	{"Title":"Xăng E10 RON 95-III","Zone1Price":27080,"Zone2Price":27620,"DIsplayOrder":4,"LastModified":"2026-09-24T07:47:51.496Z"},
	{"Title":"Mazút","Zone1Price":0,"Zone2Price":0,"DIsplayOrder":9,"LastModified":"2026-09-24T08:30:00Z"},
	{"Title":"  ","Zone1Price":1,"Zone2Price":1,"DIsplayOrder":1,"LastModified":"2026-09-24T07:00:00Z"}
]}`

const giaxangFixtureReply = "⛽ Giá bán lẻ xăng dầu Petrolimex (đ/lít)\n" +
	"Giá áp dụng từ 14:48 24/09/2026\n" +
	"\n<pre>" +
	"Mặt hàng             Vùng 1  Vùng 2\n" +
	"Xăng E10 RON 95-III  27.080  27.620\n" +
	"DO 0,001S-V          32.090  32.730" +
	"</pre>"

// stubPetrolimex points the command at a test server for the duration of t.
func stubPetrolimex(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	orig := petrolimexSearchURL
	petrolimexSearchURL = server.URL + "/search"
	t.Cleanup(func() { petrolimexSearchURL = orig })
}

func TestGiaxang_RepliesPriceTable(t *testing.T) {
	stubPetrolimex(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("object-identity"); got != "search" {
			t.Errorf("object-identity = %q, want search", got)
		}
		filter, err := base64.StdEncoding.DecodeString(r.URL.Query().Get("x-request"))
		if err != nil || string(filter) != petrolimexPriceFilter {
			t.Errorf("x-request = %q (err %v), want encoded price filter", filter, err)
		}
		_, _ = w.Write([]byte(petrolimexFixture))
	})
	rb, _ := installMisc(t, 999)

	// Public: a non-admin sender gets the table.
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/giaxang"))

	sent := rb.LastSent()
	if got := sent.Text(); got != giaxangFixtureReply {
		t.Errorf("/giaxang reply =\n%s\nwant\n%s", got, giaxangFixtureReply)
	}
	if got := sent.Form["parse_mode"]; got != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", got)
	}
}

func TestGiaxang_UpstreamFailureRepliesError(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"non-200": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "blocked", http.StatusForbidden)
		},
		"bad json": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>"))
		},
		"no prices": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"Objects":[{"Title":"Mazút","Zone1Price":0}]}`))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			stubPetrolimex(t, handler)
			rb, _ := installMisc(t, 999)

			rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/giaxang"))

			if got := rb.LastSent().Text(); got != giaxangErrorText {
				t.Errorf("/giaxang reply = %q, want %q", got, giaxangErrorText)
			}
		})
	}
}

func TestFormatThousands(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999: "999", 27080: "27.080", 1234567: "1.234.567"} {
		if got := formatThousands(in); got != want {
			t.Errorf("formatThousands(%v) = %q, want %q", in, got, want)
		}
	}
}
