// Package ops implements the outbound agent channel behind @ChatBot's server
// diagnostics. A user runs daskgram-ops on their own VDS; the agent enrols with
// a one-time code, then polls us outbound and runs the checks locally. We never
// dial the user's host and never see a password -- the agent holds a bearer
// token whose hash we store.
package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnauthorized is returned for an unknown, revoked or expired credential.
var ErrUnauthorized = errors.New("ops: unauthorized")

// ErrNoAgents means the user has not enrolled any agent yet.
var ErrNoAgents = errors.New("ops: no agents enrolled")

// KindDiagnostics is the only job kind for now: the fixed read-only report.
const KindDiagnostics = "diagnostics"

// Service owns the agent/job tables.
type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Agent is one enrolled installation.
type Agent struct {
	ID         int64
	Name       string
	CreatedAt  time.Time
	LastSeenAt *time.Time
}

// Job is one unit of work handed to an agent.
type Job struct {
	ID   int64
	Kind string
}

// CreateEnrollCode mints a one-time code bound to userID. The plaintext is
// returned once and never stored.
func (s *Service) CreateEnrollCode(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	code, err := randomToken("dgm_")
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `
		insert into ops_enroll_codes (code_hash, user_id, expires_at)
		values ($1, $2, now() + $3::interval)`,
		hashToken(code), userID, fmt.Sprintf("%d seconds", int(ttl.Seconds()))); err != nil {
		return "", fmt.Errorf("create enroll code: %w", err)
	}
	return code, nil
}

// RedeemCode exchanges a one-time code for a long-lived agent token. The code is
// consumed in the same statement that creates the agent, so two concurrent
// redemptions cannot both win.
func (s *Service) RedeemCode(ctx context.Context, code, name string) (int64, string, error) {
	token, err := randomToken("dgma_")
	if err != nil {
		return 0, "", err
	}
	var agentID int64
	err = s.pool.QueryRow(ctx, `
		with consumed as (
			delete from ops_enroll_codes
			where code_hash = $1 and used_at is null and expires_at > now()
			returning user_id
		)
		insert into ops_agents (user_id, name, token_hash)
		select user_id, $2, $3 from consumed
		returning id`,
		hashToken(code), strings.TrimSpace(name), hashToken(token)).Scan(&agentID)
	if err != nil {
		return 0, "", ErrUnauthorized
	}
	return agentID, token, nil
}

// Agents lists the caller's active agents.
func (s *Service) Agents(ctx context.Context, userID int64) ([]Agent, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name, created_at, last_seen_at
		from ops_agents
		where user_id = $1 and revoked_at is null
		order by id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var agent Agent
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.CreatedAt, &agent.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, rows.Err()
}

// CreateJobs queues kind for every active agent of userID and returns the ids.
func (s *Service) CreateJobs(ctx context.Context, userID int64, kind string) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		insert into ops_jobs (agent_id, kind)
		select id, $2 from ops_agents where user_id = $1 and revoked_at is null
		returning id`, userID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNoAgents
	}
	return ids, nil
}

// Poll claims the oldest pending job for the calling agent. It returns nil when
// there is nothing to do.
func (s *Service) Poll(ctx context.Context, token string) (*Job, error) {
	agentID, err := s.agentByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	var job Job
	err = s.pool.QueryRow(ctx, `
		update ops_jobs set status = 'running', started_at = now()
		where id = (
			select id from ops_jobs
			where agent_id = $1 and status = 'pending'
			order by id
			limit 1
			for update skip locked
		)
		returning id, kind`, agentID).Scan(&job.ID, &job.Kind)
	if err != nil {
		return nil, nil
	}
	return &job, nil
}

// Complete stores the agent's output for a job it owns.
func (s *Service) Complete(ctx context.Context, token string, jobID int64, output string) error {
	agentID, err := s.agentByToken(ctx, token)
	if err != nil {
		return err
	}
	if len(output) > 8000 {
		output = output[:8000]
	}
	tag, err := s.pool.Exec(ctx, `
		update ops_jobs set status = 'done', finished_at = now(), output = $3
		where id = $1 and agent_id = $2`, jobID, agentID, output)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnauthorized
	}
	return nil
}

// WaitOutput polls for a finished job. It returns ok=false on timeout.
func (s *Service) WaitOutput(ctx context.Context, jobID int64, timeout time.Duration) (string, bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		var status, output string
		err := s.pool.QueryRow(ctx, `
			select status, output from ops_jobs where id = $1`, jobID).Scan(&status, &output)
		if err != nil {
			return "", false, err
		}
		if status == "done" {
			return output, true, nil
		}
		if time.Now().After(deadline) {
			return "", false, nil
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) agentByToken(ctx context.Context, token string) (int64, error) {
	var agentID int64
	err := s.pool.QueryRow(ctx, `
		update ops_agents set last_seen_at = now()
		where token_hash = $1 and revoked_at is null
		returning id`, hashToken(token)).Scan(&agentID)
	if err != nil {
		return 0, ErrUnauthorized
	}
	return agentID, nil
}

func hashToken(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func randomToken(prefix string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
