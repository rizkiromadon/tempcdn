package nodestatus

import (
	"net/http"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/response"
)

type NodeView struct {
	NodeID          string  `json:"node_id"`
	Hostname        string  `json:"hostname"`
	Status          string  `json:"status"`
	StartedAt       string  `json:"started_at"`
	LastHeartbeatAt string  `json:"last_heartbeat_at"`
	MarkedOfflineAt *string `json:"marked_offline_at,omitempty"`

	SecondsSinceHeartbeat float64 `json:"seconds_since_heartbeat"`
}

type ListResponse struct {
	Nodes       []NodeView `json:"nodes"`
	GeneratedAt string     `json:"generated_at"`
}

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
