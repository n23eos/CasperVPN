package rebuild

import (
	"context"
	"errors"
	"log"
	"time"
)

// errUnknownKind marks a job whose kind the queue cannot execute; handle() will
// retry then drop it rather than loop forever.
var errUnknownKind = errors.New("rebuild(durable): unknown job kind")

// JobKind is the durable, cross-package form of a rebuild job's kind (the SQL
// column is TEXT). The in-memory Queue uses its own unexported jobKind; the
// durable path uses these string values so the Postgres adapter (a different
// package) can implement JobStore without importing an unexported type.
type JobKind string

const (
	// JobKindNode rebuilds every user affected by a node change.
	JobKindNode JobKind = "node"
	// JobKindUser rebuilds a single user.
	JobKindUser JobKind = "user"
)

// Job is one durable rebuild job as returned by a JobStore.Claim.
type Job struct {
	ID       int64
	Kind     JobKind
	TargetID string
	Attempts int // how many times this job has been claimed (>=1 once claimed)
}

// JobStore is the durable backend for the rebuild queue. A Postgres
// implementation makes pending rebuilds survive a restart and lets several
// control-plane instances share one queue (SELECT ... FOR UPDATE SKIP LOCKED).
type JobStore interface {
	// Enqueue records a pending job. Implementations SHOULD coalesce a job that
	// is already pending for the same (kind, target) so repeated fleet changes
	// don't stampede duplicate rebuilds.
	Enqueue(ctx context.Context, kind JobKind, targetID string) error
	// Claim leases up to max due jobs for leaseFor, marking them so a concurrent
	// worker/instance won't take the same ones, and increments their attempts.
	Claim(ctx context.Context, max int, leaseFor time.Duration) ([]Job, error)
	// Complete removes a finished job.
	Complete(ctx context.Context, jobID int64) error
	// Retry releases a failed job to be claimed again after retryAfter.
	Retry(ctx context.Context, jobID int64, retryAfter time.Duration) error
}

// DurableQueue implements domain.RebuildQueue on top of a JobStore. Enqueue is
// a durable write (fire-and-forget, matching the interface's no-error contract:
// the request path must never block on or fail because of a rebuild); a pool of
// pollers claims and processes jobs, so pending work survives restarts.
type DurableQueue struct {
	store     JobStore
	sets      SetRepo
	rebuilder Rebuilder
	logger    *log.Logger

	workers      int
	pollInterval time.Duration
	batchSize    int
	lease        time.Duration
	jobTTL       time.Duration
	maxAttempts  int
	retryBackoff time.Duration
	enqueueTTL   time.Duration
	now          func() time.Time
}

// SetRepo is the subset of the domain set repository the queue needs (node
// fan-out + cache invalidation). *postgres.SetStore satisfies it.
type SetRepo interface {
	UserIDsByNode(ctx context.Context, nodeID string) ([]string, error)
	MarkDirtyByNode(ctx context.Context, nodeID string) error
}

// DurableConfig tunes a DurableQueue; zero fields fall back to safe defaults.
type DurableConfig struct {
	Workers      int
	PollInterval time.Duration
	BatchSize    int
	Lease        time.Duration
	JobTTL       time.Duration
	MaxAttempts  int
	RetryBackoff time.Duration
}

// NewDurable builds a DurableQueue over a JobStore.
func NewDurable(store JobStore, sets SetRepo, rebuilder Rebuilder, cfg DurableConfig, logger *log.Logger) *DurableQueue {
	if logger == nil {
		logger = log.Default()
	}
	q := &DurableQueue{
		store:        store,
		sets:         sets,
		rebuilder:    rebuilder,
		logger:       logger,
		workers:      cfg.Workers,
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
		lease:        cfg.Lease,
		jobTTL:       cfg.JobTTL,
		maxAttempts:  cfg.MaxAttempts,
		retryBackoff: cfg.RetryBackoff,
		now:          time.Now,
	}
	if q.workers <= 0 {
		q.workers = 4
	}
	if q.pollInterval <= 0 {
		q.pollInterval = time.Second
	}
	if q.batchSize <= 0 {
		q.batchSize = q.workers
	}
	if q.lease <= 0 {
		q.lease = time.Minute
	}
	if q.jobTTL <= 0 {
		q.jobTTL = 30 * time.Second
	}
	if q.maxAttempts <= 0 {
		q.maxAttempts = 5
	}
	if q.retryBackoff <= 0 {
		q.retryBackoff = 10 * time.Second
	}
	q.enqueueTTL = 5 * time.Second
	return q
}

// EnqueueNode implements domain.RebuildQueue (durable write, fire-and-forget).
func (q *DurableQueue) EnqueueNode(nodeID string) { q.enqueue(JobKindNode, nodeID) }

// EnqueueUser implements domain.RebuildQueue (durable write, fire-and-forget).
func (q *DurableQueue) EnqueueUser(userID string) { q.enqueue(JobKindUser, userID) }

func (q *DurableQueue) enqueue(kind JobKind, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), q.enqueueTTL)
	defer cancel()
	if err := q.store.Enqueue(ctx, kind, id); err != nil {
		// Fire-and-forget contract: we cannot fail the request path. Log loudly —
		// a dropped enqueue means a user may keep a stale set until the next event.
		q.logger.Printf("rebuild(durable): enqueue %s %s failed: %v", kind, id, err)
	}
}

// Start launches the poller pool; pollers stop when ctx is cancelled.
func (q *DurableQueue) Start(ctx context.Context) {
	for i := 0; i < q.workers; i++ {
		go q.poller(ctx)
	}
}

// poller repeatedly claims and processes a batch until ctx is done.
func (q *DurableQueue) poller(ctx context.Context) {
	t := time.NewTicker(q.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Drain what we can each tick; stop when a claim yields nothing so we
			// don't spin hot on an empty queue.
			for {
				n, err := q.claimAndProcess(ctx)
				if err != nil {
					q.logger.Printf("rebuild(durable): claim failed: %v", err)
					break
				}
				if n == 0 || ctx.Err() != nil {
					break
				}
			}
		}
	}
}

// claimAndProcess claims a batch and processes each job, returning how many were
// claimed (0 means the queue is idle).
func (q *DurableQueue) claimAndProcess(ctx context.Context) (int, error) {
	jobs, err := q.store.Claim(ctx, q.batchSize, q.lease)
	if err != nil {
		return 0, err
	}
	for _, j := range jobs {
		q.handle(ctx, j)
	}
	return len(jobs), nil
}

// handle runs one job and settles it: Complete on success, Retry on a
// transient failure, or drop (Complete) once it has exhausted its attempts so a
// poison job cannot loop forever.
func (q *DurableQueue) handle(ctx context.Context, j Job) {
	jobCtx, cancel := context.WithTimeout(ctx, q.jobTTL)
	defer cancel()

	if err := q.run(jobCtx, j); err != nil {
		if j.Attempts >= q.maxAttempts {
			q.logger.Printf("rebuild(durable): job %d (%s %s) failed %d times, dropping: %v",
				j.ID, j.Kind, j.TargetID, j.Attempts, err)
			sctx, sc := q.settleCtx()
			defer sc()
			if cerr := q.store.Complete(sctx, j.ID); cerr != nil {
				q.logger.Printf("rebuild(durable): drop job %d failed: %v", j.ID, cerr)
			}
			return
		}
		q.logger.Printf("rebuild(durable): job %d (%s %s) attempt %d failed, retrying: %v",
			j.ID, j.Kind, j.TargetID, j.Attempts, err)
		sctx, sc := q.settleCtx()
		defer sc()
		if rerr := q.store.Retry(sctx, j.ID, q.retryBackoff); rerr != nil {
			q.logger.Printf("rebuild(durable): retry job %d failed: %v", j.ID, rerr)
		}
		return
	}
	sctx, sc := q.settleCtx()
	defer sc()
	if err := q.store.Complete(sctx, j.ID); err != nil {
		q.logger.Printf("rebuild(durable): complete job %d failed: %v", j.ID, err)
	}
}

// settleCtx returns a short detached context for post-processing store writes
// (Complete/Retry) so they still run even if the parent ctx is cancelling during
// shutdown. Go 1.20 has no context.WithoutCancel, hence a fresh background ctx.
func (q *DurableQueue) settleCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), q.enqueueTTL)
}

// run performs the actual rebuild for one job. It mirrors the in-memory Queue's
// process step (kept separate to avoid disturbing the proven in-memory path).
func (q *DurableQueue) run(ctx context.Context, j Job) error {
	switch j.Kind {
	case JobKindUser:
		_, err := q.rebuilder.Build(ctx, j.TargetID)
		return err
	case JobKindNode:
		if err := q.sets.MarkDirtyByNode(ctx, j.TargetID); err != nil {
			return err
		}
		users, err := q.sets.UserIDsByNode(ctx, j.TargetID)
		if err != nil {
			return err
		}
		for _, uid := range users {
			if _, err := q.rebuilder.Build(ctx, uid); err != nil {
				return err
			}
		}
		return nil
	default:
		// Unknown kind can never succeed; report so handle() drops it eventually.
		return errUnknownKind
	}
}
