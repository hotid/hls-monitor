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
	// Link is how the agent's connection to the server is doing.
	Link *Link `json:"link,omitempty"`
	// Fetches tells that the agent takes requests to fetch something for
	// the server (see FetchRequest); older agents do not.
	Fetches bool `json:"fetches,omitempty"`
}

// Link describes the way from an agent to the server, as the agent sees
// it on the requests it makes all the time. A stream watched through an
// agent, and every result of its checks, travels this way: when it is bad,
// what arrives late or not at all is no fault of the stream.
type Link struct {
	// RTTMs is the round trip time to the server, by the kernel's count on
	// the latest request; zero where the agent cannot read it.
	RTTMs float64 `json:"rtt_ms"`
	// Retransmits counts the TCP segments the agent had to send again
	// since it started: loss on the way to the server.
	Retransmits int64 `json:"retransmits"`
	// Segments counts the TCP segments with data the agent sent since it
	// started: what Retransmits is a share of. Zero from agents that do
	// not tell it.
	Segments int64 `json:"segments,omitempty"`
	// RequestMs is how long the latest delivery of results took.
	RequestMs float64 `json:"request_ms"`
	// Requests and Failures count the requests to the server since the
	// start and those of them that got no answer.
	Requests int64 `json:"requests"`
	Failures int64 `json:"failures"`
	// Measured is false where the kernel's figures cannot be read (the
	// agent does not run on Linux): RTTMs and Retransmits mean nothing.
	Measured bool `json:"measured"`
}

// FetchTiming is how one fetch went, in milliseconds.
type FetchTiming struct {
	DNSMs     float64 `json:"dns_ms"`
	ConnectMs float64 `json:"connect_ms"`
	TLSMs     float64 `json:"tls_ms"`
	// TTFBMs is from the request to the first byte of the answer, TotalMs
	// to the last one.
	TTFBMs  float64 `json:"ttfb_ms"`
	TotalMs float64 `json:"total_ms"`
	Bytes   int64   `json:"bytes"`
	// Redirects is how many were followed; Reused tells that no new
	// connection was needed.
	Redirects int  `json:"redirects,omitempty"`
	Reused    bool `json:"reused,omitempty"`
	// Of the connection to the origin, by the kernel's count; Measured
	// is false where that cannot be read. OutOfOrder counts segments that
	// arrived out of order over the life of the connection: the only sign
	// of loss a receiver has, and not a certain one.
	Measured   bool    `json:"measured,omitempty"`
	RTTMs      float64 `json:"rtt_ms,omitempty"`
	OutOfOrder int64   `json:"out_of_order,omitempty"`
	// Retransmits and Segments are how many TCP segments had to be sent
	// again to the origin and how many were sent, since the previous
	// request on the same connection: the loss on the way there, counted
	// by the one who sends.
	Retransmits int64 `json:"retransmits,omitempty"`
	Segments    int64 `json:"segments,omitempty"`
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
	// Timing is how the request to the origin went.
	Timing *FetchTiming `json:"timing,omitempty"`
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
	// Slates are the pictures known to be slates, for the looks at the
	// streams.
	Slates []probe.Slate `json:"slates,omitempty"`
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
