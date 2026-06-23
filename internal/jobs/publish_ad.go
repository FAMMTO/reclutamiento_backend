// Package jobs define los tipos y workers de los jobs en cola (River).
package jobs

import (
	"context"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// PublishAdArgs es el payload del job que publica un anuncio en Meta.
// Debe ser JSON-serializable y estable entre versiones del worker.
type PublishAdArgs struct {
	OrgID   uuid.UUID `json:"orgId"`
	DraftID uuid.UUID `json:"draftId"`
}

func (PublishAdArgs) Kind() string { return "publish_ad" }

// AdPublisher es la interfaz que inyecta facebookads.Service en el worker.
// Rompe la dependencia circular sin exponer River al dominio.
type AdPublisher interface {
	ExecutePublishAd(ctx context.Context, orgID, draftID uuid.UUID) error
}

// PublishAdWorker procesa los jobs de publicación en Meta.
// Se registra en el pool del worker; el API solo encola, no ejecuta.
type PublishAdWorker struct {
	river.WorkerDefaults[PublishAdArgs]
	publisher AdPublisher
}

func NewPublishAdWorker(publisher AdPublisher) *PublishAdWorker {
	return &PublishAdWorker{publisher: publisher}
}

func (w *PublishAdWorker) Work(ctx context.Context, job *river.Job[PublishAdArgs]) error {
	return w.publisher.ExecutePublishAd(ctx, job.Args.OrgID, job.Args.DraftID)
}
