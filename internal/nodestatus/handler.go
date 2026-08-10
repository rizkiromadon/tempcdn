package nodestatus

import (
	"net/http"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/response"
)

// NodeView is the JSON shape of one node in the GET /api/v1/nodes response.
type NodeView struct {
	NodeID          string  `json:"node_id"`
	Hostname        string  `json:"hostname"`
	Status          string  `json:"status"`
	StartedAt       string  `json:"started_at"`
	LastHeartbeatAt string  `json:"last_heartbeat_at"`
	MarkedOfflineAt *string `json:"marked_offline_at,omitempty"`
	// SecondsSinceHeartbeat is computed at response time rather than
	// stored, so a client polling this endpoint doesn't need its own
	// clock synced with the server's to tell how stale a node's
	// heartbeat currently is.
	SecondsSinceHeartbeat float64 `json:"seconds_since_heartbeat"`
}

// ListResponse is the JSON body returned by GET /api/v1/nodes.
type ListResponse struct {
	Nodes       []NodeView `json:"nodes"`
	GeneratedAt string     `json:"generated_at"`
}

// Handler serves GET /api/v1/nodes: a read-only view of every known node's
// current liveness row (see metadata.Repository.ListNodeStatus). It does
// not itself decide online/offline - that's Janitor's job on a schedule -
// this just reports whatever the database currently says.
type Handler struct {
	repository metadata.Repository
	now        func() time.Time
}

func NewHandler(repository metadata.Repository) *Handler {
	return &Handler{repository: repository, now: time.Now}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.repository.ListNodeStatus(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to list node status")
		return
	}

	now := h.now().UTC()
	views := make([]NodeView, 0, len(nodes))
	for _, node := range nodes {
		view := NodeView{
			NodeID:                node.NodeID,
			Hostname:              node.Hostname,
			Status:                node.Status,
			StartedAt:             node.StartedAt.UTC().Format(time.RFC3339),
			LastHeartbeatAt:       node.LastHeartbeatAt.UTC().Format(time.RFC3339),
			SecondsSinceHeartbeat: now.Sub(node.LastHeartbeatAt.UTC()).Seconds(),
		}
		if node.MarkedOfflineAt != nil {
			formatted := node.MarkedOfflineAt.UTC().Format(time.RFC3339)
			view.MarkedOfflineAt = &formatted
		}
		views = append(views, view)
	}

	response.JSON(w, http.StatusOK, ListResponse{
		Nodes:       views,
		GeneratedAt: now.Format(time.RFC3339),
	})
}
