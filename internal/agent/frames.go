// Package agent implements the node half of the Gnodi Agent Protocol v1.
//
// The node dials the gateway and holds the connection open; the gateway pushes
// jobs down it. Nothing listens for inbound connections on this machine, which
// is what lets a node behind residential NAT serve traffic.
//
// Normative spec: docs/agent-protocol.md.
package agent

// ProtocolVersion is the only version this build speaks.
const ProtocolVersion = 1

// --- gateway → node ---

type helloFrame struct {
	T            string `json:"t"`
	Protocol     int    `json:"protocol"`
	Nonce        string `json:"nonce"`
	HeartbeatSec int    `json:"heartbeatSec"`
}

type readyFrame struct {
	T      string `json:"t"`
	NodeID string `json:"nodeId"`
	// Operator is the EVM address this node's revenue settles to, from the
	// licence record. The node cannot derive it, so the gateway supplies it.
	Operator string `json:"operator"`
}

type offerFrame struct {
	T          string    `json:"t"`
	JobID      string    `json:"jobId"`
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	MaxTokens  int       `json:"maxTokens"`
	DeadlineMs int       `json:"deadlineMs"`
}

type cancelFrame struct {
	T     string `json:"t"`
	JobID string `json:"jobId"`
}

type errorFrame struct {
	T       string `json:"t"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Message is one chat turn on the wire.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// typeOf peeks at a frame's discriminator without committing to a shape.
type typeOnly struct {
	T string `json:"t"`
}

// --- node → gateway ---

type authFrame struct {
	T           string `json:"t"`
	Protocol    int    `json:"protocol"`
	LicenseKey  string `json:"licenseKey"`
	Pubkey      string `json:"pubkey"`
	Sig         string `json:"sig"`
	NodeVersion string `json:"nodeVersion"`
}

type capabilitiesFrame struct {
	T              string   `json:"t"`
	Models         []string `json:"models"`
	MaxConcurrency int      `json:"maxConcurrency"`
	VramGb         int      `json:"vramGb,omitempty"`
	Engine         string   `json:"engine,omitempty"`
}

type acceptFrame struct {
	T     string `json:"t"`
	JobID string `json:"jobId"`
}

type rejectFrame struct {
	T      string `json:"t"`
	JobID  string `json:"jobId"`
	Reason string `json:"reason"`
}

type chunkFrame struct {
	T     string `json:"t"`
	JobID string `json:"jobId"`
	Seq   int    `json:"seq"`
	Delta string `json:"delta"`
}

type doneFrame struct {
	T                string `json:"t"`
	JobID            string `json:"jobId"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	DurationMs       int64  `json:"durationMs"`
}

type jobErrorFrame struct {
	T       string `json:"t"`
	JobID   string `json:"jobId"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type statusFrame struct {
	T         string `json:"t"`
	Accepting bool   `json:"accepting"`
	InFlight  int    `json:"inFlight"`
}
