package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Channel is where an org's notifications go: Slack, Discord, Telegram or
// a webhook. Its target (URLs, tokens, signing secret) is encrypted.
type Channel struct {
	ID        string
	Org       string
	Name      string
	Kind      string // slack | discord | telegram | webhook
	Target    ChannelTarget
	Events    []string
	CreatedBy string
	CreatedAt time.Time
}

// ChannelTarget is a channel's secret part.
type ChannelTarget struct {
	URL      string `json:"url,omitempty"`       // slack, discord, webhook
	Secret   string `json:"secret,omitempty"`    // webhook signing secret
	BotToken string `json:"bot_token,omitempty"` // telegram
	ChatID   string `json:"chat_id,omitempty"`   // telegram
}

func (s *Store) SaveChannel(ctx context.Context, c *Channel) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.Marshal(c.Target)
	if err != nil {
		return err
	}
	target, err := s.seal(b, "channels.target:"+c.ID)
	if err != nil {
		return err
	}
	ev, _ := json.Marshal(c.Events)
	_, err = s.db.ExecContext(ctx, `INSERT INTO channels(id, org, name, kind, target, events, created_by, created_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, target=excluded.target, events=excluded.events`,
		c.ID, c.Org, c.Name, c.Kind, target, string(ev), c.CreatedBy, ts(c.CreatedAt))
	return err
}

const channelCols = `id, org, name, kind, target, events, created_by, created_at`

func (s *Store) scanChannel(r scanner) (*Channel, error) {
	var c Channel
	var target, events, created string
	if err := r.Scan(&c.ID, &c.Org, &c.Name, &c.Kind, &target, &events, &c.CreatedBy, &created); err != nil {
		return nil, err
	}
	plain, err := s.open(target, "channels.target:"+c.ID)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(plain, &c.Target); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(events), &c.Events)
	c.CreatedAt, _ = time.Parse(tsFormat, created)
	return &c, nil
}

func (s *Store) GetChannel(ctx context.Context, id string) (*Channel, error) {
	c, err := s.scanChannel(s.db.QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// ListChannels lists an org's channels, or every org's when org is "".
func (s *Store) ListChannels(ctx context.Context, org string) ([]Channel, error) {
	q, args := `SELECT `+channelCols+` FROM channels`, []any{}
	if org != "" {
		q += ` WHERE org = ?`
		args = append(args, org)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := s.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	return err
}
