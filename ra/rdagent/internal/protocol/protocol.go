package protocol

const (
	RoleRDAgent = "rd_agent"

	MessageClientHello     = "client_hello"
	MessageSign            = "sign"
	MessageHandshake       = "handshake"
	MessageRDAgentRegister = "rd_agent_register"

	MessageRDOffer  = "rd_offer"
	MessageRDAnswer = "rd_answer"
	MessageRDIce    = "rd_ice"
	MessageRDReady  = "rd_ready"
	MessageRDStop   = "rd_stop"
	MessageRDClosed = "rd_closed"
	MessageRDError  = "rd_error"

	RDTargetAdmin = "admin"
	RDTargetAgent = "agent"
)

type Message struct {
	Type       string `json:"type"`
	ID         string `json:"id,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	Role       string `json:"role,omitempty"`

	SessionID string `json:"session_id,omitempty"`
	Token     string `json:"token,omitempty"`
	Target    string `json:"target,omitempty"`

	SDP       string `json:"sdp,omitempty"`
	Candidate any    `json:"candidate,omitempty"`

	APIKey string `json:"api_key,omitempty"`
	Error  string `json:"error,omitempty"`

	PublicKey string `json:"public_key,omitempty"`
	Signature string `json:"signature,omitempty"`
	Challenge string `json:"challenge,omitempty"`

	Answer      any    `json:"answer,omitempty"`
	Description string `json:"description,omitempty"`
}
