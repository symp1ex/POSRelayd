package app

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"rdagent/internal/config"
	"rdagent/internal/identity"
	"rdagent/internal/logger"
	"rdagent/internal/protocol"
	"rdagent/internal/rtc"
	"rdagent/internal/signaling"
)

type App struct {
	cfg      config.Config
	id       *identity.Identity
	ws       *signaling.Client
	rtcPeer  *rtc.Peer
	incoming chan protocol.Message
}

func Run(ctx context.Context, cfg config.Config) error {
	id, err := identity.LoadEd25519PrivateKey(cfg.PrivateKeyFile)
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}

	wsClient := signaling.NewClient(cfg)
	if err := wsClient.Connect(ctx); err != nil {
		return err
	}
	defer wsClient.Close()

	app := &App{
		cfg:      cfg,
		id:       id,
		ws:       wsClient,
		rtcPeer:  rtc.NewPeer(cfg.SessionID, cfg.ClientID, wsClient, cfg.ICEServers, cfg),
		incoming: make(chan protocol.Message, 128),
	}

	defer app.rtcPeer.Close()

	if err := app.sendClientHello(); err != nil {
		return err
	}

	readErr := make(chan error, 1)
	go func() {
		readErr <- wsClient.ReadLoop(ctx, app.incoming)
	}()

	for {
		select {
		case <-ctx.Done():
			logger.RDAgent.Info("Shutdown signal received")
			_ = app.sendClosed("rd-agent process stopped")
			return nil

		case err := <-readErr:
			_ = app.sendClosed("websocket read loop stopped")
			return err

		case msg, ok := <-app.incoming:
			if !ok {
				_ = app.sendClosed("websocket incoming channel closed")
				return nil
			}

			if err := app.handleMessage(msg); err != nil {
				logger.RDAgent.Errorf("Message handling failed: type=%s error=%v", msg.Type, err)

				_ = app.sendError(err.Error())
				return err
			}
		}
	}
}

func (a *App) sendClientHello() error {
	return a.ws.Send(protocol.Message{
		Type:       protocol.MessageClientHello,
		ID:         a.cfg.ClientID,
		ClientID:   a.cfg.ClientID,
		Role:       protocol.RoleRDAgent,
		APIKey:     a.cfg.APIKey,
		PublicKey:  a.id.PublicKeyPEM(),
		InstanceID: a.cfg.InstanceID,
		SessionID:  a.cfg.SessionID,
		Token:      a.cfg.Token,
	})
}

func (a *App) sendRDAgentRegister() error {
	return a.ws.Send(protocol.Message{
		Type:      protocol.MessageRDAgentRegister,
		ID:        a.cfg.ClientID,
		ClientID:  a.cfg.ClientID,
		Role:      protocol.RoleRDAgent,
		SessionID: a.cfg.SessionID,
		Token:     a.cfg.Token,
	})
}

func (a *App) handleMessage(msg protocol.Message) error {
	switch msg.Type {
	case protocol.MessageHandshake:
		return a.handleHandshake(msg)

	case protocol.MessageRDReady:
		logger.RDAgent.Infof(
			"RD ready: session_id=%s client_id=%s",
			firstNonEmpty(msg.SessionID, msg.ID, a.cfg.SessionID),
			firstNonEmpty(msg.ClientID, a.cfg.ClientID),
		)
		return nil

	case protocol.MessageRDOffer:
		if msg.SDP == "" {
			return fmt.Errorf("rd_offer without sdp")
		}

		logger.RDAgent.Info("RD offer received")
		return a.rtcPeer.HandleOffer(msg.SDP)

	case protocol.MessageRDIce:
		logger.RDAgent.Debug("RD ICE received")
		return a.rtcPeer.AddRemoteICE(msg.Candidate)

	case protocol.MessageRDStop:
		logger.RDAgent.Info("RD stop received")
		a.rtcPeer.Close()
		_ = a.sendClosed("rd_stop received")
		return context.Canceled

	case protocol.MessageRDClosed:
		logger.RDAgent.Infof("RD closed received: %s", msg.Error)
		a.rtcPeer.Close()
		return context.Canceled

	case "rd_shutdown":
		logger.RDAgent.Info("RD shutdown received")
		a.rtcPeer.Close()
		return context.Canceled

	case protocol.MessageRDError:
		return fmt.Errorf("server rd_error: %s", msg.Error)

	case "error":
		return fmt.Errorf("server error: %s", msg.Error)

	default:
		logger.RDAgent.Warnf("Unknown message ignored: type=%s", msg.Type)
		return nil
	}
}

func (a *App) handleHandshake(msg protocol.Message) error {
	answer, _ := msg.Answer.(string)

	switch answer {
	case "check":
		if msg.Challenge == "" {
			return fmt.Errorf("handshake check without challenge")
		}

		challenge, err := hex.DecodeString(msg.Challenge)
		if err != nil {
			return fmt.Errorf("decode challenge: %w", err)
		}

		signature, err := a.id.Sign(challenge)
		if err != nil {
			return fmt.Errorf("sign challenge: %w", err)
		}

		signatureB64 := base64.StdEncoding.EncodeToString(signature)

		logger.RDAgent.Debug("Handshake challenge signed")

		return a.ws.Send(protocol.Message{
			Type:      protocol.MessageSign,
			ID:        a.cfg.ClientID,
			ClientID:  a.cfg.ClientID,
			SessionID: a.cfg.SessionID,
			Signature: signatureB64,
		})

	case "ok", "register":
		logger.RDAgent.Infof("Handshake accepted: answer=%s", answer)
		return a.sendRDAgentRegister()

	case "fail":
		return fmt.Errorf("handshake rejected: %s", msg.Description)

	default:
		return fmt.Errorf("unknown handshake answer: %v", msg.Answer)
	}
}

func (a *App) sendClosed(reason string) error {
	logger.RDAgent.Infof("Sending rd_closed: %s", reason)

	return a.ws.Send(protocol.Message{
		Type:      protocol.MessageRDClosed,
		ID:        a.cfg.SessionID,
		SessionID: a.cfg.SessionID,
		ClientID:  a.cfg.ClientID,
		Target:    protocol.RDTargetAdmin,
		Error:     reason,
	})
}

func (a *App) sendError(reason string) error {
	logger.RDAgent.Errorf("Sending rd_error: %s", reason)

	return a.ws.Send(protocol.Message{
		Type:      protocol.MessageRDError,
		ID:        a.cfg.SessionID,
		SessionID: a.cfg.SessionID,
		ClientID:  a.cfg.ClientID,
		Target:    protocol.RDTargetAdmin,
		Error:     reason,
	})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}
