package telegram

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type UpdateSource interface {
	GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error)
}

type UpdateHandler interface {
	HandleUpdate(ctx context.Context, update Update) error
}

type UpdateStore interface {
	Cursor(ctx context.Context) (int64, error)
	Begin(ctx context.Context, updateID int64) (bool, error)
	Complete(ctx context.Context, updateID int64) error
}

type Poller struct {
	source     UpdateSource
	handler    UpdateHandler
	store      UpdateStore
	poll       time.Duration
	retryDelay time.Duration
	mu         sync.RWMutex
	lastPollOK time.Time
}

func NewPoller(source UpdateSource, handler UpdateHandler, store UpdateStore, poll, retryDelay time.Duration) *Poller {
	return &Poller{source: source, handler: handler, store: store, poll: poll, retryDelay: retryDelay}
}

// Run long-polls sequentially. The cursor advances only after successful
// processing, so a temporary dependency or send failure is retried after restart.
func (p *Poller) Run(ctx context.Context) error {
	cursor, err := p.readCursor(ctx)
	if err != nil {
		return err
	}
	for {
		updates, err := p.source.GetUpdates(ctx, cursor+1, p.poll)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !waitContext(ctx, p.retryDelay) {
				return nil
			}
			continue
		}
		p.mu.Lock()
		p.lastPollOK = time.Now()
		p.mu.Unlock()
		sort.Slice(updates, func(i, j int) bool { return updates[i].UpdateID < updates[j].UpdateID })
		retry := false
		for _, update := range updates {
			if update.UpdateID <= cursor {
				continue
			}
			process, err := p.store.Begin(ctx, update.UpdateID)
			if err != nil {
				retry = true
				break
			}
			if process {
				if err := p.handler.HandleUpdate(ctx, update); err != nil {
					retry = true
					break
				}
			}
			if err := p.store.Complete(ctx, update.UpdateID); err != nil {
				retry = true
				break
			}
			cursor = update.UpdateID
		}
		if retry && !waitContext(ctx, p.retryDelay) {
			return nil
		}
	}
}

// Ready reports whether Telegram polling has succeeded recently. This keeps
// /readyz honest when the token or Bot API endpoint is unavailable.
func (p *Poller) Ready(context.Context) error {
	p.mu.RLock()
	lastPollOK := p.lastPollOK
	p.mu.RUnlock()
	if lastPollOK.IsZero() || time.Since(lastPollOK) > 2*p.poll+2*p.retryDelay {
		return errors.New("telegram poller unavailable")
	}
	return nil
}

func (p *Poller) readCursor(ctx context.Context) (int64, error) {
	for {
		cursor, err := p.store.Cursor(ctx)
		if err == nil {
			return cursor, nil
		}
		if ctx.Err() != nil || !waitContext(ctx, p.retryDelay) {
			return 0, ctx.Err()
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
