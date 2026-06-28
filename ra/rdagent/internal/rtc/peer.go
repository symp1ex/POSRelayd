package rtc

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

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

	pendingRemoteICE       []webrtc.ICECandidateInit
	disconnectedCloseTimer *time.Timer

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

	if p.disconnectedCloseTimer != nil {
		p.disconnectedCloseTimer.Stop()
		p.disconnectedCloseTimer = nil
	}

	if p.pc != nil {
		logger.RDAgent.Warn("Existing PeerConnection will be closed before applying new offer")
		_ = p.pc.Close()
		p.pc = nil
	}

	p.seenRemote = make(map[string]struct{})

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

	if len(p.pendingRemoteICE) > 0 {
		pending := p.pendingRemoteICE
		p.pendingRemoteICE = nil

		logger.RDAgent.Infof("Applying queued remote ICE candidates: count=%d", len(pending))

		for _, init := range pending {
			if err := p.addRemoteICELocked(init); err != nil {
				return fmt.Errorf("add queued remote ice: %w", err)
			}
		}
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

	init, err := parseICECandidate(candidate)
	if err != nil {
		return err
	}

	if init.Candidate == "" {
		logger.RDAgent.Debug("Empty ICE candidate ignored")
		return nil
	}

	if p.pc == nil {
		p.pendingRemoteICE = append(p.pendingRemoteICE, init)
		logger.RDAgent.Warnf(
			"Remote ICE queued: PeerConnection is not created yet pending=%d",
			len(p.pendingRemoteICE),
		)
		return nil
	}

	return p.addRemoteICELocked(init)
}

func parseICECandidate(candidate any) (webrtc.ICECandidateInit, error) {
	raw, err := json.Marshal(candidate)
	if err != nil {
		return webrtc.ICECandidateInit{}, fmt.Errorf("marshal ice candidate: %w", err)
	}

	var init webrtc.ICECandidateInit
	if err := json.Unmarshal(raw, &init); err != nil {
		return webrtc.ICECandidateInit{}, fmt.Errorf("unmarshal ice candidate: %w", err)
	}

	return init, nil
}

func (p *Peer) addRemoteICELocked(init webrtc.ICECandidateInit) error {
	if init.Candidate == "" {
		return nil
	}

	key := fmt.Sprintf("%s|%s|%d", init.Candidate, init.SDPMid, init.SDPMLineIndex)
	if _, ok := p.seenRemote[key]; ok {
		logger.RDAgent.Debug("Duplicate remote ICE candidate ignored")
		return nil
	}
	p.seenRemote[key] = struct{}{}

	if err := p.pc.AddICECandidate(init); err != nil {
		return fmt.Errorf("add ice candidate: %w", err)
	}

	logger.RDAgent.Debug("Remote ICE candidate added")
	return nil
}

func (p *Peer) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.disconnectedCloseTimer != nil {
		p.disconnectedCloseTimer.Stop()
		p.disconnectedCloseTimer = nil
	}

	p.pendingRemoteICE = nil
	p.seenRemote = make(map[string]struct{})

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

		logger.RDAgent.Debugf(
			"Local ICE candidate sent: sdpMid=%s sdpMLineIndex=%v candidate=%s",
			init.SDPMid,
			init.SDPMLineIndex,
			init.Candidate,
		)
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.RDAgent.Infof("PeerConnection state: %s", state.String())

		switch state {
		case webrtc.PeerConnectionStateConnected:
			p.mu.Lock()
			if p.disconnectedCloseTimer != nil {
				p.disconnectedCloseTimer.Stop()
				p.disconnectedCloseTimer = nil
			}
			p.mu.Unlock()

		case webrtc.PeerConnectionStateDisconnected:
			p.scheduleDisconnectedClose(pc, 15*time.Second)

		case webrtc.PeerConnectionStateFailed,
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

func (p *Peer) scheduleDisconnectedClose(pc *webrtc.PeerConnection, delay time.Duration) {
	p.mu.Lock()

	if p.disconnectedCloseTimer != nil {
		p.disconnectedCloseTimer.Stop()
	}

	p.disconnectedCloseTimer = time.AfterFunc(delay, func() {
		p.mu.Lock()
		currentPC := p.pc
		p.mu.Unlock()

		if currentPC != pc {
			return
		}

		state := pc.ConnectionState()
		if state != webrtc.PeerConnectionStateDisconnected {
			logger.RDAgent.Infof(
				"PeerConnection disconnected close skipped: current_state=%s",
				state.String(),
			)
			return
		}

		logger.RDAgent.Warnf(
			"PeerConnection stayed disconnected for %s, closing RD channel",
			delay,
		)

		_ = p.sender.Send(protocol.Message{
			Type:      protocol.MessageRDClosed,
			ID:        p.sessionID,
			SessionID: p.sessionID,
			ClientID:  p.clientID,
			Target:    protocol.RDTargetAdmin,
			Error:     "PeerConnection state: disconnected timeout",
		})
	})

	p.mu.Unlock()

	logger.RDAgent.Warnf(
		"PeerConnection disconnected; waiting %s before closing",
		delay,
	)
}

func (p *Peer) configureDataChannel(dc *webrtc.DataChannel, origin string) {
	label := dc.Label()

	logger.RDAgent.Infof("DataChannel discovered: label=%s origin=%s", label, origin)

	dc.OnOpen(func() {
		logger.RDAgent.Infof("DataChannel opened: label=%s", label)

		if label == "motion" {
			logger.RDAgent.Info("Motion DataChannel ready")
			return
		}

		if label == "control" {
			if err := p.control.BindSender(dc); err != nil {
				logger.RDAgent.Warnf("Clipboard watcher start failed: %v", err)
			}

			if err := dc.SendText(`{"type":"rd_agent_ready"}`); err != nil {
				logger.RDAgent.Warnf("DataChannel initial send failed: %v", err)
			}
		}
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if label != "control" && label != "motion" {
			return
		}

		if !msg.IsString {
			if len(msg.Data) > 32 {
				logger.RDAgent.Warnf("Binary input message too large ignored: label=%s bytes=%d", label, len(msg.Data))
				return
			}

			if err := p.control.HandleBinary(msg.Data); err != nil {
				logger.RDAgent.Warnf("Binary input handling failed: label=%s bytes=%d error=%v", label, len(msg.Data), err)
			}
			return
		}

		if label == "motion" && len(msg.Data) > 512 {
			logger.RDAgent.Warnf("Motion JSON message too large ignored: bytes=%d", len(msg.Data))
			return
		}

		if err := p.control.Handle(dc, msg.Data); err != nil {
			logger.RDAgent.Warnf("DataChannel JSON message handling failed: label=%s error=%v", label, err)
		}
	})

	dc.OnClose(func() {
		logger.RDAgent.Infof("DataChannel closed: label=%s", label)

		if label == "control" {
			p.control.ReleaseAll()
		}
	})

	dc.OnError(func(err error) {
		logger.RDAgent.Errorf("DataChannel error label=%s: %v", label, err)

		if label == "control" {
			p.control.ReleaseAll()
		}
	})
}
