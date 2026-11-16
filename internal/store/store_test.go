package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var ctx = context.Background()

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := s.CreateStream(ctx, StreamInput{Name: "one", URL: "http://x/a.m3u8", Enabled: true, Level: "segments", AgentIDs: []int64{LocalAgentID}}, now); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, path) // migrations must be a no-op the second time
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.ListStreams(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "one" || len(list[0].AgentIDs) != 1 {
		t.Fatalf("streams after reopen = %+v, err %v", list, err)
	}
}

func TestUsersAndSessions(t *testing.T) {
	s := open(t)
	now := time.Now()
	admin, err := s.CreateUser(ctx, "Admin", "hash", RoleAdmin, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "admin", "hash", RoleViewer, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("usernames must be unique ignoring case, got %v", err)
	}
	if u, err := s.GetUserByName(ctx, "ADMIN"); err != nil || u.ID != admin.ID {
		t.Fatalf("lookup must ignore case: %v %v", u, err)
	}

	// The last administrator cannot be demoted, disabled or deleted.
	viewer, disabled := RoleViewer, true
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdate{Role: &viewer}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demoting the last admin: %v", err)
	}
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdate{Disabled: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disabling the last admin: %v", err)
	}
	if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the last admin: %v", err)
	}

	token := []byte("token-hash")
	if err := s.CreateSession(ctx, token, admin.ID, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if u, err := s.SessionUser(ctx, token, now); err != nil || u.ID != admin.ID {
		t.Fatalf("live session not found: %v", err)
	}
	if _, err := s.SessionUser(ctx, token, now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session must not resolve: %v", err)
	}

	// With a second admin the first can be disabled, which ends its sessions.
	if _, err := s.CreateUser(ctx, "second", "hash", RoleAdmin, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, token, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("session of a disabled user must not resolve: %v", err)
	}
}

func TestAgentEnrollmentIsOneShot(t *testing.T) {
	s := open(t)
	now := time.Now()
	a, err := s.CreateAgent(ctx, "edge-1", []byte("enroll"), now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Enrolled || a.Kind != AgentRemote {
		t.Fatalf("new agent = %+v", a)
	}

	if _, err := s.EnrollAgent(ctx, []byte("enroll"), []byte("k0"), now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token must be refused: %v", err)
	}

	// Ten concurrent attempts with the same token: exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.EnrollAgent(ctx, []byte("enroll"), []byte{byte(i)}, now); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("enrollment succeeded %d times, want exactly 1", wins)
	}

	if err := s.DeleteAgent(ctx, LocalAgentID); !errors.Is(err, ErrLocalAgent) {
		t.Errorf("the local agent must not be deletable: %v", err)
	}
}

func TestStreamAssignment(t *testing.T) {
	s := open(t)
	now := time.Now()
	remote, _ := s.CreateAgent(ctx, "edge-1", []byte("e"), now.Add(time.Hour), now)

	in := StreamInput{Name: "news", URL: "http://x/a.m3u8", Enabled: true, Level: "container",
		Headers: map[string]string{"Authorization": "Bearer x"}, AgentIDs: []int64{LocalAgentID, remote.ID}}
	st, err := s.CreateStream(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateStream(ctx, in, now); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	bad := in
	bad.Name, bad.AgentIDs = "other", []int64{999}
	if _, err := s.CreateStream(ctx, bad, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent: %v", err)
	}
	if list, _ := s.ListStreams(ctx); len(list) != 1 {
		t.Errorf("a failed create must leave nothing behind, got %d streams", len(list))
	}

	targets, err := s.TargetsForAgent(ctx, remote.ID)
	if err != nil || len(targets) != 1 || targets[0].Headers["Authorization"] != "Bearer x" || targets[0].Level != "container" {
		t.Fatalf("targets = %+v, err %v", targets, err)
	}

	in.AgentIDs = []int64{LocalAgentID}
	up, err := s.UpdateStream(ctx, st.ID, in, now)
	if err != nil || up.Revision != st.Revision+1 {
		t.Fatalf("update: %+v %v", up, err)
	}
	if targets, _ := s.TargetsForAgent(ctx, remote.ID); len(targets) != 0 {
		t.Errorf("unassigned agent still has targets: %+v", targets)
	}

	if err := s.SetStreamEnabled(ctx, st.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if targets, _ := s.TargetsForAgent(ctx, LocalAgentID); len(targets) != 0 {
		t.Errorf("disabled stream still handed out: %+v", targets)
	}
	if err := s.DeleteStream(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStream(ctx, st.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

// Migration 3 rebuilds the streams table. Everything that refers to a
// stream must survive it, and the new check level must be accepted after.
func TestStreamsRebuildKeepsReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, 2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO streams (id, name, url, level, created_at, updated_at) VALUES (7, 'old', 'http://x/a.m3u8', 'container', 1, 1)`,
		`INSERT INTO stream_agents (stream_id, agent_id) VALUES (7, 1)`,
		`INSERT INTO variants (id, stream_id, key, kind, first_seen, last_seen) VALUES (3, 7, 'media', 'media', 1, 1)`,
		`INSERT INTO stream_state (stream_id, agent_id, status, checked_at) VALUES (7, 1, 'ok', 1)`,
		`INSERT INTO incidents (stream_id, agent_id, variant_id, kind, severity, message, started_at, last_seen) VALUES (7, 1, 3, 'k', 'warn', 'm', 1, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO streams (name, url, level, created_at, updated_at) VALUES ('h', 'http://x/b.m3u8', 'header', 1, 1)`); err == nil {
		t.Fatal("the old schema was expected to reject the new level")
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.GetStream(ctx, 7)
	if err != nil || st.Name != "old" || st.Level != "container" || len(st.AgentIDs) != 1 {
		t.Fatalf("stream after the rebuild = %+v, err %v", st, err)
	}
	if v, _ := s.ListVariants(ctx, 7); len(v) != 1 {
		t.Errorf("variants lost: %+v", v)
	}
	if states, _ := s.StreamStates(ctx, 7); len(states) != 1 {
		t.Errorf("state lost: %+v", states)
	}
	if inc, _ := s.ListIncidents(ctx, IncidentFilter{StreamID: 7}); len(inc) != 1 {
		t.Errorf("incidents lost: %+v", inc)
	}
	in := StreamInput{Name: "new", URL: "http://x/c.m3u8", Enabled: true, Level: "header", AgentIDs: []int64{LocalAgentID}}
	if _, err := s.CreateStream(ctx, in, time.Now()); err != nil {
		t.Fatalf("the header level must be accepted now: %v", err)
	}
	// Foreign keys are enforced again: deleting a stream takes its rows along.
	if err := s.DeleteStream(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ListVariants(ctx, 7); len(v) != 0 {
		t.Errorf("deleting a stream no longer cascades: %+v", v)
	}
}
