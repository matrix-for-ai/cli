package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppservicePagesAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			w.Write([]byte(`{"members":[{"user":"@a"}],"next_cursor":"c2"}`))
		case "c2":
			w.Write([]byte(`{"members":[{"user":"@b"}],"next_cursor":null}`))
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(400)
			w.Write([]byte(`{"status":400,"code":"invalid_room","detail":"not a room id"}`))
		}
	}))
	defer srv.Close()

	items, err := appservicePages(srv.URL+"/m", "", "members")
	if err != nil || len(items) != 2 || string(items[1]) != `{"user":"@b"}` {
		t.Fatalf("pages: %s, %v", items, err)
	}
	_, err = appservicePages(srv.URL+"/m?cursor=bad", "", "members")
	if err == nil || !strings.HasPrefix(err.Error(), "not a room id (invalid_room)\na ROOM is a room ID") {
		t.Errorf("problem: %v", err)
	}
}
