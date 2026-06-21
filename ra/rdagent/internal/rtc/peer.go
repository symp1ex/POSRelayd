package rtc

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"

	"rdagent/internal/logger"
	"rdagent/internal/protocol"
)

type SignalSender interface {
	Send(msg protocol.Message) error
}

type Peer struct {
	sessionID string
	clientID  string
	sender    SignalSender

	mu sync.Mutex
	pc *webrtc.PeerConnection
}

func NewPeer(sessionID string, clientID string, sender SignalSender) *Peer {
	return &Peer{
		sessionID: sessionID,
		clientID:  clientID,
		sender:    sender,
	}
}

func (p *Peer) HandleOffer(sdp string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pc != nil {
		logger.RDAgent.Warn("Existing PeerConnection will be closed before applying new offer")
		_ = p.pc.Close()
		p.pc = nil
	}

	pc, err := p.newPeerConnectionLocked()
	if err != nil {
		return err
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	}

	if err := pc.SetRemoteDescription(offer); err != nil {
		return fmt.Errorf("set remote offer: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %w", err)
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local answer: %w", err)
	}

	if err := p.sender.Send(protocol.Message{
		Type:      protocol.MessageRDAnswer,
		ID:        p.sessionID,
		SessionID: p.sessionID,
		ClientID:  p.clientID,
		Target:    protocol.RDTargetAdmin,
		SDP:       answer.SDP,
	}); err != nil {
		return fmt.Errorf("send rd_answer: %w", err)
	}

	logger.RDAgent.Info("WebRTC answer sent")
	return nil
}

func (p *Peer) AddRemoteICE(candidate any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pc == nil {
		logger.RDAgent.Warn("Remote ICE ignored: PeerConnection is not created yet")
		return nil
	}

	raw, err := json.Marshal(candidate)
	if err != nil {
		return fmt.Errorf("marshal ice candidate: %w", err)
	}

	var init webrtc.ICECandidateInit
	if err := json.Unmarshal(raw, &init); err != nil {
		return fmt.Errorf("unmarshal ice candidate: %w", err)
	}

	if init.Candidate == "" {
		logger.RDAgent.Debug("Empty ICE candidate ignored")
		return nil
	}

	if err := p.pc.AddICECandidate(init); err != nil {
		return fmt.Errorf("add ice candidate: %w", err)
	}

	logger.RDAgent.Debug("Remote ICE candidate added")
	return nil
}

func (p *Peer) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pc == nil {
		return
	}

	if err := p.pc.Close(); err != nil {
		logger.RDAgent.Warnf("PeerConnection close failed: %v", err)
	}

	p.pc = nil
	logger.RDAgent.Info("PeerConnection closed")
}

func (p *Peer) newPeerConnectionLocked() (*webrtc.PeerConnection, error) {
	cfg := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{
					"stun:stun.l.google.com:19302",
				},
			},
		},
	}

	pc, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		return nil, fmt.Errorf("new peer connection: %w", err)
	}

	p.pc = pc

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			logger.RDAgent.Debug("ICE gathering complete")
			return
		}

		init := candidate.ToJSON()

		if err := p.sender.Send(protocol.Message{
			Type:      protocol.MessageRDIce,
			ID:        p.sessionID,
			SessionID: p.sessionID,
			ClientID:  p.clientID,
			Target:    protocol.RDTargetAdmin,
			Candidate: init,
		}); err != nil {
			logger.RDAgent.Errorf("Send local ICE failed: %v", err)
			return
		}

		logger.RDAgent.Debug("Local ICE candidate sent")
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.RDAgent.Infof("PeerConnection state: %s", state.String())

		switch state {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateClosed:
			_ = p.sender.Send(protocol.Message{
				Type:      protocol.MessageRDClosed,
				ID:        p.sessionID,
				SessionID: p.sessionID,
				ClientID:  p.clientID,
				Target:    protocol.RDTargetAdmin,
				Error:     "PeerConnection state: " + state.String(),
			})
		}
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		logger.RDAgent.Infof("ICE connection state: %s", state.String())
	})

	pc.OnICEGatheringStateChange(func(state webrtc.ICEGatheringState) {
		logger.RDAgent.Infof("ICE gathering state: %s", state.String())
	})

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		p.configureDataChannel(dc, "remote-created")
	})

	control, err := pc.CreateDataChannel("control", nil)
	if err != nil {
		_ = pc.Close()
		p.pc = nil
		return nil, fmt.Errorf("create control datachannel: %w", err)
	}

	p.configureDataChannel(control, "local-created")

	logger.RDAgent.Info("PeerConnection created")
	return pc, nil
}

func (p *Peer) configureDataChannel(dc *webrtc.DataChannel, origin string) {
	label := dc.Label()

	logger.RDAgent.Infof("DataChannel discovered: label=%s origin=%s", label, origin)

	dc.OnOpen(func() {
		logger.RDAgent.Infof("DataChannel open: label=%s", label)

		if label == "control" {
			if err := dc.SendText(`{"type":"rd_agent_ready"}`); err != nil {
				logger.RDAgent.Warnf("DataChannel initial send failed: %v", err)
			}
		}
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			logger.RDAgent.Debugf("DataChannel message label=%s text=%s", label, string(msg.Data))
			return
		}

		logger.RDAgent.Debugf("DataChannel binary message label=%s bytes=%d", label, len(msg.Data))
	})

	dc.OnClose(func() {
		logger.RDAgent.Infof("DataChannel closed: label=%s", label)
	})

	dc.OnError(func(err error) {
		logger.RDAgent.Errorf("DataChannel error label=%s: %v", label, err)
	})
}
