// Package agentproto defines the messages exchanged between the server and
// remote agents. Agents only make outgoing requests: they enroll once,
// then poll their configuration and post results.
package agentproto

import "github.com/hotid/hls-monitor/internal/probe"

// Header names an agent sends with authenticated requests.
const (
	HeaderVersion = "X-Agent-Version"
	// HeaderTime is the agent's clock in Unix milliseconds, used to measure
	// how far it is from the server's.
	HeaderTime = "X-Agent-Time"
)

// EnrollRequest exchanges a one-time enrollment token for a permanent key.
type EnrollRequest struct {
	Token   string `json:"token"`
	Version string `json:"version"`
}

// EnrollResponse carries the key the agent must keep and present from now on.
type EnrollResponse struct {
	AgentID int64  `json:"agent_id"`
	Name    string `json:"name"`
	Key     string `json:"key"`
}

// Config is what an agent has to do.
type Config struct {
	AgentID int64  `json:"agent_id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Revision identifies this exact set of targets.
	Revision string         `json:"revision"`
	Targets  []probe.Target `json:"targets"`
	// PollSec is how often to ask for the configuration again.
	PollSec int `json:"poll_sec"`
	// ServerTime is the server's clock in Unix milliseconds.
	ServerTime int64 `json:"server_time"`
}

// ResultsRequest delivers a batch of check results.
type ResultsRequest struct {
	Results []*probe.Result `json:"results"`
}

// ResultsResponse tells how the batch was handled. Results are processed in
// order. Rejected results belong to streams the agent is not, or no longer,
// assigned to; they must not be sent again. Failed counts the results at
// the end of the batch that the server could not store this time: only
// those are to be sent again.
type ResultsResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Failed   int `json:"failed"`
}
