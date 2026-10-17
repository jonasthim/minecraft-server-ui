package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

// closeManagerDatabase runs after the composed shutdown has drained all work.
// The caller retains the manager data lock until this function returns. A
// failed shutdown, proof preparation or DB close never publishes a clean drain.
func closeManagerDatabase(ctx context.Context, database *sql.DB, dataDir, version string, prior error) error {
	var webReceipt *selfupdate.DrainReceipt
	var maintenanceReceipt *selfupdate.MaintenanceDrainReceipt
	var webErr, maintenanceErr error
	if prior == nil {
		webReceipt, webErr = selfupdate.PrepareDrainReceipt(ctx, database, dataDir, version)
		maintenanceReceipt, maintenanceErr = selfupdate.PrepareMaintenanceDrainReceipt(ctx, database, dataDir, version)
	}
	if err := errors.Join(prior, webErr, maintenanceErr, database.Close()); err != nil {
		return err
	}
	return errors.Join(selfupdate.PublishDrainReceipt(ctx, dataDir, webReceipt), selfupdate.PublishMaintenanceDrainReceipt(ctx, dataDir, maintenanceReceipt))
}

func mountManagerReadiness(r chi.Router, dataDir, version string, schema int64, ready func() bool) {
	r.Method("GET", "/internal/update-ready", selfupdate.NewReadiness(dataDir, version, schema, ready))
	r.Method("GET", "/internal/maintenance-ready", selfupdate.NewMaintenanceReadiness(dataDir, version, schema, ready))
}
