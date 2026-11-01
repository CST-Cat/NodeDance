package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	rsyncLeaseSeconds   = 1800
	rsyncTimeoutSeconds = 900
)

func rsyncCredentials() (string, string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	return "nd", hex.EncodeToString(raw), nil
}

func rsyncAllowedHost(remoteIP string) string {
	if net.ParseIP(remoteIP) != nil {
		return remoteIP
	}
	return "*"
}

func (s *Server) rsyncPeer(ctx context.Context, nodeID string) (string, error) {
	connection, err := s.activeFileConnection(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if !connection.capabilityNegotiated(protocol.CapabilityFileRsync) {
		return "", errors.New("the agent does not support rsync transfer yet; upgrade the agent")
	}
	return connection.remoteIP, nil
}

func (s *Server) abortRsyncTarget(current *session, targetNodeID, taskID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 15*time.Second)
	defer cancel()
	_, _ = s.runFileOperation(ctx, current, targetNodeID, protocol.FileRequest{Operation: protocol.FileRsyncAbort, TaskID: taskID})
}

func (s *Server) runRsyncTransfer(ctx context.Context, current *session, sourceNodeID, sourcePath, targetNodeID, targetPath, taskID, key, mode string, size int64) error {
	sourceIP, err := s.rsyncPeer(ctx, sourceNodeID)
	if err != nil {
		return err
	}
	targetIP, err := s.rsyncPeer(ctx, targetNodeID)
	if err != nil {
		return err
	}
	sameNode := sourceNodeID == targetNodeID
	if mode == protocol.FileTransferModeRelay {
		return s.rsyncViaCore(ctx, current, sourceNodeID, sourcePath, targetNodeID, targetPath, taskID, key, sourceIP, targetIP, size)
	}
	var asset protocol.NodeAsset
	if !sameNode {
		asset, err = s.nodeAsset(ctx, targetNodeID)
		if err != nil {
			return err
		}
		if asset.TransferHost == "" || asset.TransferPort <= 0 {
			return errors.New("direct transfer is unavailable: the target node has no configured transfer endpoint")
		}
	}
	return s.rsyncDirect(ctx, current, sourceNodeID, sourcePath, targetNodeID, targetPath, taskID, asset, sameNode, sourceIP)
}

func (s *Server) rsyncDirect(ctx context.Context, current *session, sourceNodeID, sourcePath, targetNodeID, targetPath, taskID string, asset protocol.NodeAsset, sameNode bool, sourceIP string) error {
	user, password, err := rsyncCredentials()
	if err != nil {
		return err
	}
	spec := &protocol.FileRsyncSpec{User: user, Password: password, LeaseSeconds: rsyncLeaseSeconds}
	host := "127.0.0.1"
	port := 0
	if sameNode {
		spec.LocalOnly = true
	} else {
		spec.BindPort = asset.TransferBindPort
		if spec.BindPort == 0 {
			spec.BindPort = asset.TransferPort
		}
		spec.AllowedHost = rsyncAllowedHost(sourceIP)
		host = asset.TransferHost
		port = asset.TransferPort
	}
	receive, err := s.runFileOperation(ctx, current, targetNodeID, protocol.FileRequest{Operation: protocol.FileRsyncReceive, TaskID: taskID, Rsync: spec})
	if err != nil {
		return err
	}
	if sameNode {
		port = receive.Port
	}
	send := protocol.FileRequest{Operation: protocol.FileRsyncSend, Path: sourcePath, Rsync: &protocol.FileRsyncSpec{
		Host: host, Port: port, User: user, Password: password, RemoteName: path.Base(targetPath), TimeoutSeconds: rsyncTimeoutSeconds}}
	if _, err := s.runFileOperation(ctx, current, sourceNodeID, send); err != nil {
		s.abortRsyncTarget(current, targetNodeID, taskID)
		return err
	}
	if _, err := s.runFileOperation(ctx, current, targetNodeID, protocol.FileRequest{Operation: protocol.FileRsyncFinish, TaskID: taskID}); err != nil {
		s.abortRsyncTarget(current, targetNodeID, taskID)
		return err
	}
	return nil
}

func (s *Server) rsyncViaCore(ctx context.Context, current *session, sourceNodeID, sourcePath, targetNodeID, targetPath, taskID, key, sourceIP, targetIP string, size int64) error {
	user, password, err := rsyncCredentials()
	if err != nil {
		return err
	}
	remoteName := path.Base(targetPath)
	receive, err := s.rsyncRelay.openReceive(key, rsyncAllowedHost(sourceIP), user, password, rsyncLeaseSeconds)
	if err != nil {
		return err
	}
	defer s.rsyncRelay.discard(receive.port)
	send := protocol.FileRequest{Operation: protocol.FileRsyncSend, Path: sourcePath, Rsync: &protocol.FileRsyncSpec{
		Port: receive.port, User: user, Password: password, RemoteName: remoteName, TimeoutSeconds: rsyncTimeoutSeconds}}
	if _, err := s.runFileOperation(ctx, current, sourceNodeID, send); err != nil {
		return err
	}
	s.rsyncRelay.stop(receive.port)
	staged := filepath.Join(receive.stagingDir, remoteName)
	info, err := os.Stat(staged)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("relayed file is missing or incomplete at Core")
	}
	serveUser, servePassword, err := rsyncCredentials()
	if err != nil {
		return err
	}
	serve, err := s.rsyncRelay.openServe(key, receive.stagingDir, rsyncAllowedHost(targetIP), serveUser, servePassword, rsyncLeaseSeconds)
	if err != nil {
		return err
	}
	defer s.rsyncRelay.discard(serve.port)
	fetch := protocol.FileRequest{Operation: protocol.FileRsyncFetch, TaskID: taskID, Rsync: &protocol.FileRsyncSpec{
		Port: serve.port, User: serveUser, Password: servePassword, RemoteName: remoteName, TimeoutSeconds: rsyncTimeoutSeconds}}
	if _, err := s.runFileOperation(ctx, current, targetNodeID, fetch); err != nil {
		s.abortRsyncTarget(current, targetNodeID, taskID)
		return err
	}
	if _, err := s.runFileOperation(ctx, current, targetNodeID, protocol.FileRequest{Operation: protocol.FileRsyncFinish, TaskID: taskID}); err != nil {
		s.abortRsyncTarget(current, targetNodeID, taskID)
		return err
	}
	return nil
}
