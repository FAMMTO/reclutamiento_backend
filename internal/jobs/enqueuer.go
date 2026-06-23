package jobs

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// RiverEnqueuer implementa facebookads.JobEnqueuer usando River sobre Postgres.
type RiverEnqueuer struct {
	client *river.Client[pgx.Tx]
}

func NewRiverEnqueuer(pool *pgxpool.Pool) (*RiverEnqueuer, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, err
	}
	return &RiverEnqueuer{client: client}, nil
}

func (e *RiverEnqueuer) EnqueuePublishAd(ctx context.Context, orgID, draftID uuid.UUID) error {
	_, err := e.client.Insert(ctx, PublishAdArgs{OrgID: orgID, DraftID: draftID}, nil)
	return err
}
