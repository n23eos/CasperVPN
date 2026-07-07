// Package rebuild is the "issue a fresh config" queue. When the fleet changes
// (a node goes blocked, is retired, or a user rotates secrets), it asynchronously
// rebuilds the affected users' active Node×Transport sets and invalidates their
// cached subscription set — so users get a working replacement with no manual step.
package rebuild

import (
	"context"
	"log"
	"time"

	"github.com/caspervpn/control-plane/internal/domain"
)

// Rebuilder recomputes and persists one user's set. *usecase.BundleService fits.
type Rebuilder interface {
	Build(ctx context.Context, userID string) (domain.SubscriptionSet, error)
}

type jobKind int

const (
	jobNode jobKind = iota
	jobUser
)

type job struct {
	kind jobKind
	id   string
}

// Queue is an in-memory worker pool implementing domain.RebuildQueue.
type Queue struct {
	jobs      chan job
	sets      domain.SetRepo
	rebuilder Rebuilder
	workers   int
	jobTTL    time.Duration
	logger    *log.Logger
}

// New builds a Queue. buffer sizes the channel; workers is the concurrency.
func New(sets domain.SetRepo, rebuilder Rebuilder, buffer, workers int, logger *log.Logger) *Queue {
	if buffer <= 0 {
		buffer = 1024
	}
	if workers <= 0 {
		workers = 4
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Queue{
		jobs:      make(chan job, buffer),
		sets:      sets,
		rebuilder: rebuilder,
		workers:   workers,
		jobTTL:    30 * time.Second,
		logger:    logger,
	}
}

// Start launches the workers; they stop when ctx is cancelled.
func (q *Queue) Start(ctx context.Context) {
	for i := 0; i < q.workers; i++ {
		go q.worker(ctx)
	}
}

// EnqueueNode schedules a rebuild for all users affected by a node change.
func (q *Queue) EnqueueNode(nodeID string) { q.enqueue(job{kind: jobNode, id: nodeID}) }

// EnqueueUser schedules a rebuild for one user.
func (q *Queue) EnqueueUser(userID string) { q.enqueue(job{kind: jobUser, id: userID}) }

// enqueue never blocks the caller (a request handler): it offloads a full
// channel to a goroutine rather than stalling the request path.
func (q *Queue) enqueue(j job) {
	select {
	case q.jobs <- j:
	default:
		go func() { q.jobs <- j }()
	}
}

func (q *Queue) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-q.jobs:
			q.process(ctx, j)
		}
	}
}

func (q *Queue) process(ctx context.Context, j job) {
	jobCtx, cancel := context.WithTimeout(ctx, q.jobTTL)
	defer cancel()

	switch j.kind {
	case jobUser:
		if _, err := q.rebuilder.Build(jobCtx, j.id); err != nil {
			q.logger.Printf("rebuild: user %s failed: %v", j.id, err)
		}
	case jobNode:
		// Invalidate cache for everyone holding this node, then rebuild them onto
		// the remaining active fleet.
		if err := q.sets.MarkDirtyByNode(jobCtx, j.id); err != nil {
			q.logger.Printf("rebuild: invalidate node %s failed: %v", j.id, err)
		}
		users, err := q.sets.UserIDsByNode(jobCtx, j.id)
		if err != nil {
			q.logger.Printf("rebuild: affected users for node %s failed: %v", j.id, err)
			return
		}
		for _, uid := range users {
			if _, err := q.rebuilder.Build(jobCtx, uid); err != nil {
				q.logger.Printf("rebuild: user %s (node %s) failed: %v", uid, j.id, err)
			}
		}
	}
}
