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
	// Fetches tells that the agent takes requests to fetch something for
	// the server (see FetchRequest); older agents do not.
	Fetches bool `json:"fetches,omitempty"`
}

// An agent accepts no connections, so when the server wants something
// fetched from where the agent stands — a stream watched through it — the
// agent asks for such work: it keeps a few requests to the server open,
// each answered with a FetchRequest when there is one, and posts what it
// got back. HeaderFetchResult carries a FetchResult as base64 of its JSON;
// the body of that request is the body that was fetched.
const HeaderFetchResult = "X-Fetch-Result"

// FetchRequest is one address for the agent to fetch.
type FetchRequest struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// Headers are sent only to the site of HeadersFor, the stream's own
	// address.
	Headers    map[string]string `json:"headers,omitempty"`
	HeadersFor string            `json:"headers_for,omitempty"`
	Insecure   bool              `json:"insecure,omitempty"`
	// Entry marks the stream's own address; Inside tells that it was not
	// a public one. The agent applies its own policy on private networks.
	Entry  bool `json:"entry,omitempty"`
	Inside bool `json:"inside,omitempty"`
	// RangeLength above zero asks for that many bytes from RangeOffset.
	RangeOffset int64 `json:"range_offset,omitempty"`
	RangeLength int64 `json:"range_length,omitempty"`
	MaxBody     int64 `json:"max_body"`
}

// FetchResult is what came of a FetchRequest.
type FetchResult struct {
	// Status is the HTTP status, zero when there was no answer; Error
	// says why, or what broke the answer off.
	Status       int    `json:"status"`
	Error        string `json:"error,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	ContentRange string `json:"content_range,omitempty"`
	AcceptRanges string `json:"accept_ranges,omitempty"`
	// FinalURL is the address after redirects, RemoteAddr the server
	// that answered, Inside whether the first connection went to an
	// address that is not public.
	FinalURL   string `json:"final_url,omitempty"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	Inside     bool   `json:"inside,omitempty"`
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
