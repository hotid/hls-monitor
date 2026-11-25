package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/importstatus"
)

// Limits of one import: a list is pasted or uploaded by a person.
const (
	importMaxText    = 2 << 20
	importMaxEntries = 2000
)

// listEntry is one channel of a list.
type listEntry struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Group string `json:"group,omitempty"`
	// Status is what became of it, or would.
	Status importstatus.Status `json:"status" swaggertype:"string" enums:"new,created,exists,duplicate,invalid,failed"`
	Reason string              `json:"reason,omitempty"`
}

// parseChannelList reads a list of channels the way IPTV lists are kept:
// an extended M3U (#EXTINF with a name after the comma and a group-title,
// or #EXTGRP, then the address on a line of its own), a plain list of
// addresses, or lines of a name and an address parted by a comma, a
// semicolon or a tab.
func parseChannelList(text string) []listEntry {
	var out []listEntry
	var name, group string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF:"):
			name, group = extinfName(line), extinfAttr(line, "group-title")
		case strings.HasPrefix(line, "#EXTGRP:"):
			group = strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
		case strings.HasPrefix(line, "#"):
		default:
			u := line
			if i := strings.Index(line, "://"); i > 0 {
				// A name before the address, on the same line.
				if j := strings.LastIndexAny(line[:i], ",;\t"); j >= 0 {
					if name == "" {
						name = strings.Trim(strings.TrimSpace(line[:j]), `"`)
					}
					u = strings.Trim(strings.TrimSpace(line[j+1:]), `"`)
				}
			}
			if name == "" {
				name = nameOfURL(u)
			}
			out = append(out, listEntry{Name: name, URL: u, Group: group})
			name, group = "", ""
		}
	}
	return out
}

// extinfName is what follows the comma that ends the attributes of #EXTINF.
func extinfName(line string) string {
	quoted := false
	for i, r := range line {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

func extinfAttr(line, key string) string {
	_, rest, ok := strings.Cut(line, key+`="`)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, `"`)
	return strings.TrimSpace(v)
}

// nameOfURL makes a name for a channel that has none: the directory its
// playlist is in, which is what tells one channel from another on a server.
func nameOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	dir := strings.Trim(path.Dir(u.Path), "/.")
	if dir == "" {
		dir = strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))
	}
	if dir == "" || dir == "." || dir == "/" {
		return u.Host
	}
	return dir
}

type importRequest struct {
	// Text is the list itself.
	Text string `json:"text"`
	// DryRun only tells what would be added.
	DryRun bool `json:"dry_run"`
	// Settings every added stream gets; name and url are not used.
	Settings streamRequest `json:"settings"`
}

type importResult struct {
	Entries []listEntry `json:"entries"`
	// New is how many streams would be added, Created how many were.
	New      int `json:"new"`
	Created  int `json:"created"`
	Existing int `json:"existing"`
	Invalid  int `json:"invalid"`
}

// importStreams godoc
//
//	@Summary		Add streams from a list of channels
//	@Description	Reads an IPTV list (extended M3U, a plain list of addresses, or lines of a name and an address) and adds a stream for every address that is not checked yet. With dry_run it only tells what would be added.
//	@Tags			streams
//	@Accept			json
//	@Produce		json
//	@Param			request	body		importRequest	true	"The list and the settings of the streams"
//	@Success		200		{object}	importResult
//	@Failure		400		{object}	apiError
//	@Router			/api/v1/streams/import [post]
func (s *Server) importStreams(c *gin.Context) {
	var req importRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, importMaxText+64<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body, or a list over 2 MB")
		return
	}
	entries := parseChannelList(req.Text)
	if len(entries) == 0 {
		badRequest(c, "the list has no addresses")
		return
	}
	if len(entries) > importMaxEntries {
		badRequest(c, fmt.Sprintf("the list has %d addresses, at most %d are taken at once", len(entries), importMaxEntries))
		return
	}
	ctx := c.Request.Context()
	have, err := s.st.ListStreams(ctx)
	if s.storeError(c, err) {
		return
	}
	urls, names := map[string]bool{}, map[string]bool{}
	for _, st := range have {
		urls[st.URL], names[st.Name] = true, true
	}
	res := importResult{Entries: entries}
	for i := range res.Entries {
		e := &res.Entries[i]
		e.URL = strings.TrimSpace(e.URL)
		// Names are told apart: a second "News" becomes "News (2)".
		if len(e.Name) > 90 {
			e.Name = strings.TrimSpace(e.Name[:90])
		}
		base := e.Name
		for n := 2; names[e.Name]; n++ {
			e.Name = fmt.Sprintf("%s (%d)", base, n)
		}
		r := req.Settings
		r.Name, r.URL = e.Name, e.URL
		if e.Group != "" && r.Notes == "" {
			r.Notes = e.Group
		}
		in, msg := r.input()
		switch {
		case urls[e.URL] && !seenIn(res.Entries[:i], e.URL):
			e.Status, e.Name = importstatus.Exists(), base
			res.Existing++
			continue
		case seenIn(res.Entries[:i], e.URL):
			e.Status, e.Name = importstatus.Duplicate(), base
			continue
		case msg != "":
			e.Status, e.Reason, e.Name = importstatus.Invalid(), msg, base
			res.Invalid++
			continue
		}
		names[e.Name] = true
		e.Status = importstatus.New()
		res.New++
		if req.DryRun {
			continue
		}
		if _, err := s.st.CreateStream(ctx, in, s.now()); err != nil {
			e.Status, e.Reason = importstatus.Failed(), "could not be added"
			if errors.Is(err, store.ErrNotFound) {
				e.Reason = "agent_ids refers to an agent that does not exist"
			} else if errors.Is(err, store.ErrConflict) {
				e.Reason = "the name is taken"
			}
			res.New--
			res.Invalid++
			continue
		}
		e.Status = importstatus.Created()
		res.Created++
	}
	if res.Created > 0 {
		s.configChanged(ctx)
	}
	c.JSON(http.StatusOK, res)
}

func seenIn(before []listEntry, u string) bool {
	for i := range before {
		if before[i].URL == u {
			return true
		}
	}
	return false
}
