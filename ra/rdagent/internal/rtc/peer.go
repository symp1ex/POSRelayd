package rtc

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"

	"rdagent/internal/control"
	"rdagent/internal/desktop"
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

	pc         *webrtc.PeerConnection
	video      *desktop.Stream
	iceServers []webrtc.ICEServer
	seenRemote map[string]struct{}

	control *control.Handler
}

func NewPeer(sessionID string, clientID string, sender SignalSender, iceServers []webrtc.ICEServer) *Peer {
	return &Peer{
		sessionID:  sessionID,
		clientID:   clientID,
		sender:     sender,
		iceServers: iceServers,
		seenRemote: make(map[string]struct{}),
		control:    control.NewHandler(sessionID),
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

	if p.video != nil {
		if err := p.video.Start(); err != nil {
			_ = pc.Close()
			p.pc = nil
			p.video = nil
			return fmt.Errorf("start desktop stream: %w", err)
		}
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

	if p.control != nil {
		p.control.ReleaseAll()
	}

	if p.video != nil {
		p.video.Stop()
		p.video = nil
	}

	if p.pc == nil {
		return
	}

	if err := p.pc.Close(); err != nil {
		logger.RDAgent.Warnf("PeerConnection close failed: %v", err)
	}

	p.pc = nil
	logger.RDAgent.Info("PeerConnection and desktop stream closed")
}

func (p *Peer) newPeerConnectionLocked() (*webrtc.PeerConnection, error) {
	iceServers := p.iceServers
	if len(iceServers) == 0 {
		iceServers = []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		}
	}

	cfg := webrtc.Configuration{
		ICEServers: iceServers,
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
		if dc.Label() != "control" {
			logger.RDAgent.Warnf("Unexpected DataChannel ignored: label=%s", dc.Label())
			return
		}

		p.configureDataChannel(dc, "remote-created")
	})

	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8,
			ClockRate: 90000,
		},
		"desktop",
		p.sessionID,
	)

	if err != nil {
		_ = pc.Close()
		p.pc = nil
		return nil, fmt.Errorf("create desktop video track: %w", err)
	}

	rtpSender, err := pc.AddTrack(videoTrack)
	if err != nil {
		_ = pc.Close()
		p.pc = nil
		return nil, fmt.Errorf("add desktop track: %w", err)
	}

	stream, err := desktop.NewStream(p.sessionID, videoTrack, rtpSender)
	if err != nil {
		_ = pc.Close()
		p.pc = nil
		return nil, fmt.Errorf("create desktop stream: %w", err)
	}
	p.video = stream

	logger.RDAgent.Info("PeerConnection with desktop video track created")
	return pc, nil
}

func (p *Peer) configureDataChannel(dc *webrtc.DataChannel, origin string) {
	label := dc.Label()

	logger.RDAgent.Infof("DataChannel discovered: label=%s origin=%s", label, origin)

	dc.OnOpen(func() {
		logger.RDAgent.Infof("DataChannel open: label=%s", label)

		if label == "control" {
			if err := p.control.BindSender(dc); err != nil {
				logger.RDAgent.Warnf("Clipboard watcher start failed: %v", err)
			}

			if err := dc.SendText(`{"type":"rd_agent_ready"}`); err != nil {
				logger.RDAgent.Warnf("DataChannel initial send failed: %v", err)
			}

			if err := p.control.SendClipboardSnapshot(dc); err != nil {
				logger.RDAgent.Warnf("Initial clipboard snapshot send failed: %v", err)
			}
		}
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if label != "control" {
			return
		}
		if !msg.IsString {
			logger.RDAgent.Warnf("Control DataChannel binary message ignored: bytes=%d", len(msg.Data))
			return
		}

		if err := p.control.Handle(dc, msg.Data); err != nil {
			logger.RDAgent.Warnf("Control message handling failed: %v", err)
		}
	})

	dc.OnClose(func() {
		logger.RDAgent.Infof("DataChannel closed: label=%s", label)

		if label == "control" {
			p.control.UnbindSender()
			p.control.ReleaseAll()
		}
	})

	dc.OnError(func(err error) {
		logger.RDAgent.Errorf("DataChannel error label=%s: %v", label, err)

		if label == "control" {
			p.control.UnbindSender()
			p.control.ReleaseAll()
		}
	})
}
