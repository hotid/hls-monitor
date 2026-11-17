package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Agent kinds.
const (
	AgentLocal  = "local"
	AgentRemote = "remote"
)

// Agent is a vantage point streams are checked from.
type Agent struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Enabled       bool       `json:"enabled"`
	Enrolled      bool       `json:"enrolled"`
	EnrollExpires *time.Time `json:"enroll_expires,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSeen      *time.Time `json:"last_seen"`
	Version       string     `json:"version"`
	RemoteAddr    string     `json:"remote_addr"`
	ClockOffsetMs *float64   `json:"clock_offset_ms"`
}

const agentCols = "id, name, kind, enabled, key_hash IS NOT NULL, enroll_expires, created_at, last_seen, version, remote_addr, clock_offset_ms"

func scanAgent(row interface{ Scan(...any) error }) (*Agent, error) {
	var a Agent
	var created int64
	var enrollExp, lastSeen sql.NullInt64
	var offset sql.NullFloat64
	if err := row.Scan(&a.ID, &a.Name, &a.Kind, &a.Enabled, &a.Enrolled, &enrollExp, &created, &lastSeen, &a.Version, &a.RemoteAddr, &offset); err != nil {
		return nil, notFound(err)
	}
	a.CreatedAt = fromMs(created)
	a.EnrollExpires = fromMsPtr(enrollExp)
	a.LastSeen = fromMsPtr(lastSeen)
	if offset.Valid {
		a.ClockOffsetMs = &offset.Float64
	}
	if a.Kind == AgentLocal {
		a.Enrolled = true
	}
	return &a, nil
}

// CreateAgent registers a remote agent that can enroll with the given token hash.
func (s *Store) CreateAgent(ctx context.Context, name string, enrollHash []byte, enrollExpires, now time.Time) (*Agent, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO agents (id, name, kind, enroll_hash, enroll_expires, created_at) VALUES ("+nextID("agents", "agent")+", ?, 'remote', ?, ?, ?)",
		name, enrollHash, toMs(enrollExpires), toMs(now))
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetAgent(ctx, id)
}

func (s *Store) GetAgent(ctx context.Context, id int64) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE id = ?", id))
}

func (s *Store) ListAgents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+agentCols+" FROM agents ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAgent renames an agent and switches it on or off.
func (s *Store) UpdateAgent(ctx context.Context, id int64, name string, enabled bool) (*Agent, error) {
	err := affected(s.db.ExecContext(ctx, "UPDATE agents SET name = ?, enabled = ? WHERE id = ?", name, enabled, id))
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return s.GetAgent(ctx, id)
}

// ErrLocalAgent is returned for operations that do not apply to the built-in agent.
var ErrLocalAgent = errors.New("the built-in local agent cannot be changed this way")

// DeleteAgent removes a remote agent with everything it reported.
func (s *Store) DeleteAgent(ctx context.Context, id int64) error {
	if id == LocalAgentID {
		return ErrLocalAgent
	}
	err := s.Tx(ctx, func(tx *Tx) error {
		if err := affected(tx.q.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)); err != nil {
			return err
		}
		return tx.retire(ctx, "agent", id)
	})
	if err != nil {
		return err
	}
	return s.dropJournal(ctx, "agent_id", id)
}

// ResetAgentEnrollment invalidates the agent's key and lets it enroll again
// with a new token.
func (s *Store) ResetAgentEnrollment(ctx context.Context, id int64, enrollHash []byte, enrollExpires time.Time) error {
	if id == LocalAgentID {
		return ErrLocalAgent
	}
	return affected(s.db.ExecContext(ctx,
		"UPDATE agents SET key_hash = NULL, enroll_hash = ?, enroll_expires = ? WHERE id = ?",
		enrollHash, toMs(enrollExpires), id))
}

// EnrollAgent exchanges a valid enrollment token for a permanent key. The
// token works once: of concurrent attempts exactly one succeeds.
func (s *Store) EnrollAgent(ctx context.Context, enrollHash, keyHash []byte, now time.Time) (*Agent, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE agents SET key_hash = ?, enroll_hash = NULL, enroll_expires = NULL
		WHERE enroll_hash = ? AND enroll_expires > ? AND kind = 'remote'
		RETURNING id`, keyHash, enrollHash, toMs(now)).Scan(&id)
	if err != nil {
		return nil, notFound(err)
	}
	return s.GetAgent(ctx, id)
}

// AgentByKey finds the agent a key belongs to.
func (s *Store) AgentByKey(ctx context.Context, keyHash []byte) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE key_hash = ?", keyHash))
}

// TouchAgent records that an agent was heard from.
func (s *Store) TouchAgent(ctx context.Context, id int64, now time.Time, version, remoteAddr string, clockOffsetMs *float64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE agents SET last_seen = ?, version = ?, remote_addr = ?, clock_offset_ms = COALESCE(?, clock_offset_ms) WHERE id = ?",
		toMs(now), version, remoteAddr, clockOffsetMs, id)
	return err
}
