package agentmgr

import (
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

type GrpcServer struct {
	pb.UnimplementedAgentServiceServer
	mgr *Manager
}

func NewGrpcServer(mgr *Manager) *GrpcServer {
	return &GrpcServer{mgr: mgr}
}

func (g *GrpcServer) Connect(stream pb.AgentService_ConnectServer) error {
	ctx := stream.Context()
	sess, nodeID, err := g.mgr.OnAgentConnect(ctx, stream)
	if err != nil {
		return err
	}
	_ = sess
	return g.mgr.HandleMessages(ctx, nodeID, stream)
}
