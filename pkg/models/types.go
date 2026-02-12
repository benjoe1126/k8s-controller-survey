package models

// Repository represents a GitHub repository to analyze.
type Repository struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Stars     int    `json:"stars"`
	Source    string `json:"source"` // "cncf", "github-search", "curated"
	LocalPath string `json:"-"`      // Local clone path
}

// Reconciler represents a single Reconcile function.
type Reconciler struct {
	ID             string   `json:"id"`              // unique: repo#file#line
	Repo           string   `json:"repo"`
	File           string   `json:"file"`
	Line           int      `json:"line"`
	EndLine        int      `json:"end_line"`
	ReceiverType   string   `json:"receiver_type"`   // e.g., "CertificateController"
	ReceiverPkg    string   `json:"receiver_pkg"`    // package path

	// Scoring.
	Score          int      `json:"score"`
	Classification string   `json:"classification"` // edge_triggered, mostly_edge, mostly_sotw, sotw

	// Detected signals.
	Signals        []Signal `json:"signals"`

	// Metadata.
	WatchedTypes   []string `json:"watched_types,omitempty"`  // if discoverable
	HasFinalizer   bool     `json:"has_finalizer"`
	FullSource     string   `json:"full_source,omitempty"`    // optional: full function source
}

// Signal represents a detected pattern.
type Signal struct {
	Type        string `json:"type"`         // e.g., "list_unscoped", "get_req_scoped"
	Line        int    `json:"line"`
	Score       int    `json:"score"`
	Snippet     string `json:"snippet"`      // relevant code snippet
	Description string `json:"description"`  // human-readable explanation
}

// SignalType constants.
const (
	// Read patterns.
	SignalListUnscoped       = "list_unscoped"        // client.List with no selector from req (+3)
	SignalListNamespaceScoped = "list_namespace_scoped" // client.List with req.Namespace (+1)
	SignalListLabelScoped    = "list_label_scoped"    // client.List with labels from req (0)
	SignalListOwnerScoped    = "list_owner_scoped"    // client.List with owner ref from req (-1)
	SignalGetReqScoped       = "get_req_scoped"       // client.Get(req.NamespacedName) (-1)
	SignalGetDerived         = "get_derived"          // client.Get with key derived from req (-1)
	SignalGetUnrelated       = "get_unrelated"        // client.Get with hardcoded/config key (+1)

	// Write patterns.
	SignalLoopWrite          = "loop_write"           // for loop containing Create/Update/Delete (+3)
	SignalDiffSync           = "diff_sync"            // compute desired, diff with actual, sync (+3)
	SignalSingleWrite        = "single_write"         // single Create/Update/Delete (-1)
	SignalCreateOrUpdate     = "create_or_update"     // controllerutil.CreateOrUpdate (-1)
	SignalStatusUpdate       = "status_update"        // status subresource update (0)

	// Control flow patterns.
	SignalNotFoundEarlyReturn = "notfound_early_return" // if IsNotFound { handle delete } (-2)
	SignalNotFoundIgnore     = "notfound_ignore"      // if IsNotFound { return nil } (-1)
	SignalFinalizerHandling  = "finalizer_handling"   // finalizer add/remove pattern (-1)
	SignalBuildDesiredState  = "build_desired_state"  // build full desired state then apply (+2)

	// Setup patterns (from SetupWithManager).
	SignalOwnsResources      = "owns_resources"       // .Owns() in setup (-1)
	SignalWatchesWithHandler = "watches_with_handler" // .Watches() with EnqueueRequestForOwner (-1)
)

// Classification thresholds.
const (
	ThresholdEdgeTriggered     = -3
	ThresholdMostlyEdge        = 0
	ThresholdMostlySoTW        = 3
	// > 3 = SoTW
)
