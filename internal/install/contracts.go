// Package install coordinates local-administrator installation maintenance.
// Native adapters own canonical paths, authenticated files and service effects.
package install

import (
	"context"
	"time"
)

const operationBudget = 9 * time.Minute
const rollbackBudget = 5 * time.Minute

type ApplyOptions struct {
	Prepared         string
	TrustFile        string
	Version          string
	Repair           bool
	AllowDevelopment bool
}

type Image struct {
	Version string `json:"version"`
	Schema  int64  `json:"schema"`
	SHA256  string `json:"sha256"`
}

type Outcome string

const (
	// Pending means the independent worker's terminal outcome is not yet known
	// to the invoking client. It is never a completed coordinator outcome.
	OutcomePending      Outcome = "pending"
	OutcomeUnchanged    Outcome = "unchanged"
	OutcomeInstalled    Outcome = "installed"
	OutcomeUninstalled  Outcome = "uninstalled"
	OutcomeRolledBack   Outcome = "rolled_back"
	OutcomeManualRepair Outcome = "manual_repair"
)

type Result struct {
	Outcome     Outcome `json:"outcome"`
	OperationID string  `json:"operation_id,omitempty"`
	Image       *Image  `json:"image,omitempty"`
	FailedStage string  `json:"failed_stage,omitempty"`
}

type CheckReport struct {
	State    string `json:"state"`
	Coherent bool   `json:"coherent"`
	Pending  bool   `json:"pending"`
	Image    *Image `json:"image,omitempty"`
}

type mode string

const (
	modeApply     mode = "apply"
	modeUninstall mode = "uninstall"
)

type phase string

const (
	phasePrepared     phase = "prepared"
	phaseQuiescing    phase = "quiescing"
	phaseStopped      phase = "stopped"
	phaseReplacing    phase = "replacing"
	phasePublished    phase = "published"
	phaseVerifying    phase = "verifying"
	phaseCommitted    phase = "committed"
	phaseRollingBack  phase = "rolling_back"
	phaseRolledBack   phase = "rolled_back"
	phaseManualRepair phase = "manual_repair"
)

// transaction is the bounded coordinator journal. IntentSHA256 binds a separate
// immutable native intent containing authenticated imports, captured preimages,
// native identities, service/account bindings and prior startup policy. The
// coordinator never interprets arbitrary filesystem paths or native commands.
type transaction struct {
	Format       int    `json:"format"`
	ID           string `json:"id"`
	Mode         mode   `json:"mode"`
	Phase        phase  `json:"phase"`
	IntentSHA256 string `json:"intent_sha256"`
	Prior        *Image `json:"prior,omitempty"`
	Target       *Image `json:"target,omitempty"`
	FailedStage  string `json:"failed_stage,omitempty"`
}

// driver is held for one operation under actual native administrator authority
// and the existing privileged updater lock. No implementation may acquire a
// second competing operation lock or accept caller-selected output paths.
// Native adapters must independently enforce each method's preconditions; a
// valid coordinator journal alone is never authority to execute or delete files.
type driver interface {
	// Prepare reauthenticates inputs, inspects current receipt/inventory, captures
	// exact preimages and durably writes the immutable intent before returning.
	// It performs no service stop, published-asset change, receipt change or DB I/O.
	// An unchanged result is allowed only for a coherent healthy identical install
	// (or coherent already-uninstalled state); returned transaction is then nil.
	Prepare(context.Context, mode, ApplyOptions) (*transaction, *Result, error)
	// Load strictly reads and validates the journal and bound immutable intent.
	// Nil means absent, not an empty or malformed journal. No repair occurs here.
	Load(context.Context) (*transaction, error)
	// Record is durable compare-and-swap against the exact previous journal.
	// A failure can mean uncertain publication: callers must not infer absence.
	Record(context.Context, *transaction, transaction) error
	// Quiesce proves graceful drain/exit and actual data-lock exclusion. On Linux
	// manager replacement preserves running games; uninstall refuses active games.
	// It persists/revalidates native evidence needed after process interruption.
	Quiesce(context.Context, transaction) error
	// Replace publishes/removes only immutable-intent-owned assets and native
	// registrations. It journals each native effect before executing it and is
	// recoverable from any interruption. It never changes manager DB or worlds.
	Replace(context.Context, transaction) error
	// Publish durably CAS-publishes installed receipt and installation inventory.
	Publish(context.Context, transaction) error
	// Verify releases the data lock for permitted startup, checks exact running
	// image/readiness and coherent helpers/services; uninstall proves owned removal
	// and retained data/config/trust/account. Locks and startup fencing stay valid.
	Verify(context.Context, transaction) error
	// Rollback validates current/native/preimage identities, stops candidates,
	// restores all owned assets/registrations/receipts and verifies old startup.
	// It may not restore manager.db/worlds or start an image against wrong schema.
	// It is idempotent and leaves unknown/ambiguous state untouched with an error.
	Rollback(context.Context, transaction) error
	// Finish revalidates the terminal outcome and durably finalizes only owned
	// transient state. Unknown state is preserved. It never removes preimages
	// needed by a nonterminal journal. Failed cleanup remains explicitly pending.
	Finish(context.Context, transaction) error
}
