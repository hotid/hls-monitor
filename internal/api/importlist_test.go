package api

import (
	"net/http"
	"testing"

	"github.com/hotid/hls-monitor/internal/types/importstatus"
)

const channelList = `#EXTM3U
#EXTINF:-1 tvg-id="one" tvg-name="One, the first" group-title="News",One HD
http://origin.example/one/index.m3u8
#EXTINF:-1,One HD
#EXTVLCOPT:http-user-agent=x
http://origin.example/one-backup/index.m3u8
#EXTGRP:Films
http://origin.example/films/kino/index.m3u8
Two;http://origin.example/two/index.m3u8
http://origin.example/one/index.m3u8
udp://239.0.0.1:1234
`

func TestChannelList(t *testing.T) {
	got := parseChannelList(channelList)
	want := []listEntry{
		{Name: "One HD", URL: "http://origin.example/one/index.m3u8", Group: "News"},
		{Name: "One HD", URL: "http://origin.example/one-backup/index.m3u8"},
		{Name: "films/kino", URL: "http://origin.example/films/kino/index.m3u8", Group: "Films"},
		{Name: "Two", URL: "http://origin.example/two/index.m3u8"},
		{Name: "one", URL: "http://origin.example/one/index.m3u8"},
		{Name: "239.0.0.1:1234", URL: "udp://239.0.0.1:1234"},
	}
	if len(got) != len(want) {
		t.Fatalf("read %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestImportOfAList(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")
	var res importResult
	body := map[string]any{"text": channelList, "dry_run": true, "settings": map[string]any{"level": "playlist"}}
	admin.want(http.StatusOK, "POST", "/api/v1/streams/import", body, &res)
	if res.New != 4 || res.Created != 0 || res.Invalid != 1 || len(res.Entries) != 6 {
		t.Fatalf("a dry run: %+v", res)
	}
	var list []map[string]any
	admin.want(http.StatusOK, "GET", "/api/v1/streams", nil, &list)
	if len(list) != 0 {
		t.Fatalf("a dry run added %d streams", len(list))
	}

	body["dry_run"] = false
	admin.want(http.StatusOK, "POST", "/api/v1/streams/import", body, &res)
	if res.Created != 4 || res.Entries[1].Name != "One HD (2)" || res.Entries[4].Status != importstatus.Duplicate() || res.Entries[5].Status != importstatus.Invalid() {
		t.Fatalf("the import: %+v", res)
	}
	admin.want(http.StatusOK, "GET", "/api/v1/streams", nil, &list)
	if len(list) != 4 {
		t.Fatalf("%d streams after the import, want 4", len(list))
	}
	// The same list again adds nothing.
	admin.want(http.StatusOK, "POST", "/api/v1/streams/import", body, &res)
	if res.Created != 0 || res.Existing != 4 {
		t.Errorf("the same list again: %+v", res)
	}
	admin.want(http.StatusBadRequest, "POST", "/api/v1/streams/import", map[string]any{"text": "# nothing here"}, nil)
	e.login("viewer").want(http.StatusForbidden, "POST", "/api/v1/streams/import", body, nil)
}
