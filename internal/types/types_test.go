package types_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/enum"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/node"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/seconds"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/status"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

// A value of a closed set is its name outside the program and nothing but
// itself inside: it is read from the name, refuses a name the set does not
// have, and is not to be used before it is set.
func TestValuesOfAClosedSet(t *testing.T) {
	sev, err := severity.Parse("warn")
	if err != nil || sev != severity.Warn() || sev.String() != "warn" {
		t.Fatalf("warn read as %v, %v", sev, err)
	}
	if _, err := severity.Parse("fatal"); err == nil {
		t.Error("a severity there is none of was read")
	}
	if !severity.Crit().Worse(severity.Warn()) || severity.Info().Worse(severity.Warn()) {
		t.Error("the severities are not in order")
	}
	if status.Of(severity.Crit()) != status.Fail() || status.Of(severity.Info()) != status.OK() || status.Fail().Rank() != 2 {
		t.Error("a severity does not make the status it should")
	}

	type doc struct {
		Kind  problemkind.Kind          `json:"kind"`
		Sev   severity.Severity         `json:"sev,omitzero"`
		Count map[severity.Severity]int `json:"count"`
		Mode  picturemode.Mode          `json:"mode"`
		Kinds []problemkind.Kind        `json:"kinds"`
	}
	b, err := json.Marshal(doc{Kind: problemkind.PlaylistStale(), Count: map[severity.Severity]int{severity.Crit(): 2}, Mode: picturemode.Check(), Kinds: []problemkind.Kind{problemkind.SeqJump()}})
	if err != nil || string(b) != `{"kind":"playlist_stale","count":{"crit":2},"mode":"","kinds":["sequence_jump"]}` {
		t.Fatalf("written as %s, %v", b, err)
	}
	var back doc
	if err := json.Unmarshal(b, &back); err != nil || back.Kind != problemkind.PlaylistStale() || back.Count[severity.Crit()] != 2 || back.Mode != picturemode.Check() || !back.Sev.IsZero() {
		t.Fatalf("read back as %+v, %v", back.Kind, err)
	}
	if err := json.Unmarshal([]byte(`{"kind":"no_such_problem"}`), &back); err == nil {
		t.Error("a kind of problem there is none of was read")
	}
	// What was never set is not written as if it were something.
	if _, err := json.Marshal(doc{}); err == nil {
		t.Error("a kind that was never set was written")
	}
	defer func() {
		if recover() == nil {
			t.Error("a severity that was never set had a name")
		}
	}()
	_ = severity.Severity{}.String()
}

func TestValuesInADatabase(t *testing.T) {
	v, err := severity.Crit().Value()
	if err != nil || v != "crit" {
		t.Fatalf("stored as %v, %v", v, err)
	}
	var sev severity.Severity
	if err := sev.Scan("info"); err != nil || sev != severity.Info() {
		t.Errorf("read as %v, %v", sev, err)
	}
	if err := sev.Scan([]byte("warn")); err != nil || sev != severity.Warn() {
		t.Errorf("read from bytes as %v, %v", sev, err)
	}
	if sev.Scan("dire") == nil || sev.Scan(int64(1)) == nil {
		t.Error("what is no severity was read as one")
	}
	if _, err := (severity.Severity{}).Value(); err == nil {
		t.Error("a severity that was never set was stored")
	}
}

// The figures of a problem keep the kind of value each has, and go over
// the wire and into the database as they always did.
func TestFigures(t *testing.T) {
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	s := detail.Of(
		detail.UnchangedFor(15*time.Second), detail.SegmentSequence(uint64(41)), detail.PlaylistServer(node.Of("edge-1", netip.MustParseAddr("10.0.0.5"))),
		detail.PlaylistRedirects([]string{"HTTP 302 http://a/"}), detail.Since(at), detail.ComparedWith(variantkey.OfURI("lo.m3u8")),
		detail.ClipSignature([]uint64{17, 3}), detail.MediaRate(0.33349), detail.PlaylistError(""), detail.PlaylistURL("http://a/"))
	// The first value stays; a later one replaces it only when asked to.
	s.Add(detail.PlaylistURL("http://b/"))
	b, err := json.Marshal(s)
	want := `{"clip_signature":"3,11","compared_with":"lo.m3u8","media_rate":0.333,"playlist_redirects":["HTTP 302 http://a/"],"playlist_server":"edge-1 10.0.0.5","playlist_url":"http://a/","segment_sequence":41,"since":"2026-03-01T11:00:00Z","unchanged_for":15}`
	if err != nil || string(b) != want {
		t.Fatalf("written as\n%s, %v\nwant\n%s", b, err, want)
	}
	s.Replace(detail.PlaylistURL("http://b/"))
	if s.Text(detail.KeyPlaylistURL()) != "http://b/" || s.Has(detail.KeyPlaylistError()) {
		t.Errorf("after a replacement: %q, an empty text kept: %v", s.Text(detail.KeyPlaylistURL()), s.Has(detail.KeyPlaylistError()))
	}

	var back detail.Set
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if n, ok := back.Number(detail.KeyUnchangedFor()); !ok || n != 15 {
		t.Errorf("unchanged_for = %v, %v", n, ok)
	}
	if at := back.Server(detail.KeyPlaylistServer()); at.Host() != "edge-1" || at.IP().String() != "10.0.0.5" {
		t.Errorf("server = %+v", at)
	}
	if h := back.Hashes(detail.KeyClipSignature()); len(h) != 2 || h[0] != 3 || h[1] != 17 {
		t.Errorf("clip = %v", h)
	}
	if again, _ := json.Marshal(back); string(again) != string(b) {
		t.Errorf("read and written again as %s", again)
	}

	// From another copy of the program, a figure this one does not know is
	// a result it cannot take; from its own records of long ago, it is a
	// figure left out.
	strange := `{"playlist_url":"http://a/","a_figure_of_tomorrow":1,"unchanged_for":"long"}`
	if err := json.Unmarshal([]byte(strange), &back); err == nil {
		t.Error("figures this version does not know were read off the wire")
	}
	old := detail.Stored(strange)
	if old.Len() != 1 || old.Text(detail.KeyPlaylistURL()) != "http://a/" {
		t.Errorf("of an old record %d figures were read", old.Len())
	}
	if !detail.Stored("").IsZero() || !detail.Stored("not json").IsZero() {
		t.Error("figures were read out of nothing")
	}
}

// Identifiers are numbers outside the program, each of its own kind inside.
func TestIdentifiers(t *testing.T) {
	type doc struct {
		Stream id.Stream        `json:"stream"`
		Agent  id.Agent         `json:"agent,omitzero"`
		By     map[id.Agent]int `json:"by"`
	}
	b, err := json.Marshal(doc{Stream: id.StreamOf(7), By: map[id.Agent]int{id.AgentOf(2): 1}})
	if err != nil || string(b) != `{"stream":7,"by":{"2":1}}` {
		t.Fatalf("written as %s, %v", b, err)
	}
	var back doc
	if err := json.Unmarshal([]byte(`{"stream":7,"agent":null,"by":{"2":1}}`), &back); err != nil || back.Stream != id.StreamOf(7) || !back.Agent.IsZero() || back.By[id.AgentOf(2)] != 1 {
		t.Fatalf("read as %+v, %v", back, err)
	}
	if json.Unmarshal([]byte(`{"stream":"seven"}`), &back) == nil || json.Unmarshal([]byte(`{"stream":7.5}`), &back) == nil {
		t.Error("what is no whole number was read as an identifier")
	}
	if got := fmt.Sprintf("%d %v %s %q", id.StreamOf(7), id.StreamOf(7), id.StreamOf(7), id.StreamOf(7)); got != `7 7 7 "7"` {
		t.Errorf("printed as %s", got)
	}
	var a id.Agent
	if err := a.Scan(int64(5)); err != nil || a != id.AgentOf(5) || a.Scan("5") == nil {
		t.Errorf("read from a database as %v, %v", a, err)
	}
	if a.Scan(nil) != nil || !a.IsZero() {
		t.Error("NULL is not the identifier of nothing")
	}
	if a.Parse("12") != nil || a.Int64() != 12 || a.Parse("twelve") == nil {
		t.Error("an identifier is not read from an address as it should be")
	}
	if id.StreamOf(1).Compare(id.StreamOf(2)) >= 0 {
		t.Error("identifiers are not in order")
	}
}

func TestServersAndPlaylists(t *testing.T) {
	n, err := node.Parse("edge-1.example.tv:8080 2001:db8::1")
	if err != nil || n.Host() != "edge-1.example.tv:8080" || n.IP().String() != "2001:db8::1" || n.String() != "edge-1.example.tv:8080 2001:db8::1" {
		t.Errorf("a server read as %+v, %v", n, err)
	}
	if only, _ := node.Parse("edge-2"); only.Host() != "edge-2" || only.IP().IsValid() {
		t.Errorf("a server without an address read as %+v", only)
	}
	if _, err := node.Parse(" "); err == nil || !(node.Node{}).IsZero() || (node.Node{}).String() != "" {
		t.Error("no server is not told from one")
	}
	if variantkey.Master() == variantkey.Media() || variantkey.OfURI("a") != variantkey.OfURI("a") || variantkey.OfURI("a").Compare(variantkey.OfURI("b")) >= 0 {
		t.Error("keys of playlists do not compare as they should")
	}
	if _, err := variantkey.Parse(""); err == nil {
		t.Error("an empty key was read")
	}
	if _, err := json.Marshal(struct{ K variantkey.Key }{}); err == nil || !strings.Contains(err.Error(), "never set") {
		t.Errorf("a key that was never set was written: %v", err)
	}
}

// A set is what its package made it: nothing is added while the program
// runs. What is worse than what is said, not read off the order of
// definition, and the numbers the series hold are fixed.
func TestSetsAreClosed(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a value was added to a set after its package was loaded")
		}
	}()
	for st, want := range map[status.Status]float64{status.OK(): 0, status.Warn(): 1, status.Fail(): 2} {
		back, ok := status.OfRank(want)
		if st.Rank() != want || !ok || back != st {
			t.Errorf("status %v has the rank %v and reads back as %v", st, st.Rank(), back)
		}
	}
	for _, v := range []float64{-1, 0.5, 1.5, 3, math.NaN()} {
		if st, ok := status.OfRank(v); ok {
			t.Errorf("%v was read as the status %v", v, st)
		}
	}
	if !severity.Crit().Worse(severity.Warn()) || !severity.Warn().Worse(severity.Info()) || severity.Info().Worse(severity.Info()) {
		t.Error("severities are not in order")
	}
	if seconds.Of(1.5).Duration() != 1500*time.Millisecond {
		t.Errorf("1.5 seconds is %v", seconds.Of(1.5))
	}
	if b, err := json.Marshal(seconds.Of(2)); err != nil || string(b) != "2" {
		t.Errorf("two seconds written as %s, %v", b, err)
	}
	_ = enum.Def[extraDomain]("late")
}

type extraDomain struct{}

var extraSet = func() *enum.Set { s := enum.NewSet("test"); s.Seal(); return s }()

func (extraDomain) Set() *enum.Set { return extraSet }
