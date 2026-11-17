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
	// HeaderStatus carries a Status as JSON.
	HeaderStatus = "X-Agent-Status"
)

// Status is what an agent tells about itself with every request.
type Status struct {
	// QueueResults and QueueBytes describe the results not delivered yet,
	// QueueOldestSec how long the oldest of them has been waiting.
	QueueResults   int     `json:"queue_results"`
	QueueBytes     int64   `json:"queue_bytes"`
	QueueOldestSec float64 `json:"queue_oldest_sec"`
	// QueueDropped counts the results dropped since the agent started
	// because the queue was full.
	QueueDropped int64 `json:"queue_dropped"`
	// QueueFailing tells that results cannot be written to the queue.
	QueueFailing bool `json:"queue_failing,omitempty"`
	// Overloaded names what the agent's host is short of, if anything.
	Overloaded string `json:"overloaded,omitempty"`
	// Streams is how many streams the agent checks, Behind how many of
	// them it checks later than it should.
	Streams int `json:"streams"`
	Behind  int `json:"behind"`
	// CPUCores and CPULimit: processors used and available; MemoryBytes.
	CPUCores    float64 `json:"cpu_cores"`
	CPULimit    float64 `json:"cpu_limit"`
	MemoryBytes int64   `json:"memory_bytes"`
}

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
// those are to be sent again. Duplicates were stored by an earlier delivery
// whose answer the agent did not get; sending a result twice is harmless.
type ResultsResponse struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Rejected   int `json:"rejected"`
	Failed     int `json:"failed"`
}
