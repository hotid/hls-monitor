package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hotid/hls-monitor/internal/types/agentkind"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/owner"
)

// Agent is a vantage point streams are checked from.
type Agent struct {
	ID            id.Agent       `json:"id"`
	Name          string         `json:"name"`
	Kind          agentkind.Kind `json:"kind" enums:"local,remote"`
	Enabled       bool           `json:"enabled"`
	Enrolled      bool           `json:"enrolled"`
	EnrollExpires *time.Time     `json:"enroll_expires,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	LastSeen      *time.Time     `json:"last_seen"`
	Version       string         `json:"version"`
	RemoteAddr    string         `json:"remote_addr"`
	ClockOffsetMs *float64       `json:"clock_offset_ms"`
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
	if a.Kind == agentkind.Local() {
		a.Enrolled = true
	}
	return &a, nil
}

// CreateAgent registers a remote agent that can enroll with the given token hash.
func (s *Store) CreateAgent(ctx context.Context, name string, enrollHash []byte, enrollExpires, now time.Time) (*Agent, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO agents (id, name, kind, enroll_hash, enroll_expires, created_at) VALUES ("+nextID(owner.Agent())+", ?, ?, ?, ?, ?)",
		name, agentkind.Remote(), enrollHash, toMs(enrollExpires), toMs(now))
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	n, _ := res.LastInsertId()
	return s.GetAgent(ctx, id.AgentOf(n))
}

func (s *Store) GetAgent(ctx context.Context, agentID id.Agent) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE id = ?", agentID))
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
func (s *Store) UpdateAgent(ctx context.Context, agentID id.Agent, name string, enabled bool) (*Agent, error) {
	err := affected(s.db.ExecContext(ctx, "UPDATE agents SET name = ?, enabled = ? WHERE id = ?", name, enabled, agentID))
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return s.GetAgent(ctx, agentID)
}

// ErrLocalAgent is returned for operations that do not apply to the built-in agent.
var ErrLocalAgent = errors.New("the built-in local agent cannot be changed this way")

// DeleteAgent removes a remote agent with everything it reported.
func (s *Store) DeleteAgent(ctx context.Context, agentID id.Agent) error {
	if agentID == LocalAgentID() {
		return ErrLocalAgent
	}
	err := s.Tx(ctx, func(tx *Tx) error {
		if err := affected(tx.q.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", agentID)); err != nil {
			return err
		}
		return tx.retire(ctx, owner.Agent(), agentID.Int64())
	})
	if err != nil {
		return err
	}
	return s.dropJournal(ctx, owner.Agent(), agentID.Int64())
}

// ResetAgentEnrollment invalidates the agent's key and lets it enroll again
// with a new token.
func (s *Store) ResetAgentEnrollment(ctx context.Context, agentID id.Agent, enrollHash []byte, enrollExpires time.Time) error {
	if agentID == LocalAgentID() {
		return ErrLocalAgent
	}
	return affected(s.db.ExecContext(ctx,
		"UPDATE agents SET key_hash = NULL, enroll_hash = ?, enroll_expires = ? WHERE id = ?",
		enrollHash, toMs(enrollExpires), agentID))
}

// EnrollAgent exchanges a valid enrollment token for a permanent key. The
// token works once: of concurrent attempts exactly one succeeds.
func (s *Store) EnrollAgent(ctx context.Context, enrollHash, keyHash []byte, now time.Time) (*Agent, error) {
	var enrolled id.Agent
	err := s.db.QueryRowContext(ctx, `
		UPDATE agents SET key_hash = ?, enroll_hash = NULL, enroll_expires = NULL
		WHERE enroll_hash = ? AND enroll_expires > ? AND kind = ?
		RETURNING id`, keyHash, enrollHash, toMs(now), agentkind.Remote()).Scan(&enrolled)
	if err != nil {
		return nil, notFound(err)
	}
	return s.GetAgent(ctx, enrolled)
}

// AgentByKey finds the agent a key belongs to.
func (s *Store) AgentByKey(ctx context.Context, keyHash []byte) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE key_hash = ?", keyHash))
}

// TouchAgent records that an agent was heard from.
func (s *Store) TouchAgent(ctx context.Context, agentID id.Agent, now time.Time, version, remoteAddr string, clockOffsetMs *float64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE agents SET last_seen = ?, version = ?, remote_addr = ?, clock_offset_ms = COALESCE(?, clock_offset_ms) WHERE id = ?",
		toMs(now), version, remoteAddr, clockOffsetMs, agentID)
	return err
}
