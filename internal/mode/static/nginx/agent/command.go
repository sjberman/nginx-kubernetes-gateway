package agent

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/nginx/agent/v3/api/grpc/mpi/v1"
	"google.golang.org/grpc"
)

// commandService handles the connection and subscription to the agent.
type commandService struct {
	pb.CommandServiceServer
	requestChan  <-chan *pb.ManagementPlaneRequest
	responseChan chan<- *pb.DataPlaneResponse
}

func newCommandService(
	requestChan <-chan *pb.ManagementPlaneRequest,
	responseChan chan<- *pb.DataPlaneResponse,
) *commandService {
	return &commandService{
		requestChan:  requestChan,
		responseChan: responseChan,
	}
}

func (cs *commandService) Register(server *grpc.Server) {
	pb.RegisterCommandServiceServer(server, cs)
}

func (cs *commandService) CreateConnection(
	_ context.Context,
	req *pb.CreateConnectionRequest,
) (*pb.CreateConnectionResponse, error) {
	fmt.Println("Creating connection")

	if req == nil {
		return nil, errors.New("empty connection request")
	}

	return &pb.CreateConnectionResponse{
		Response: &pb.CommandResponse{
			Status:  pb.CommandResponse_COMMAND_STATUS_OK,
			Message: "Success",
		},
		AgentConfig: req.GetResource().GetInstances()[0].GetInstanceConfig().GetAgentConfig(),
	}, nil
}

func (cs *commandService) UpdateDataPlaneStatus(
	_ context.Context,
	req *pb.UpdateDataPlaneStatusRequest,
) (*pb.UpdateDataPlaneStatusResponse, error) {
	fmt.Println("Updating data plane status")

	if req == nil {
		return nil, errors.New("empty update data plane status request")
	}

	return &pb.UpdateDataPlaneStatusResponse{}, nil
}

func (cs *commandService) UpdateDataPlaneHealth(
	_ context.Context,
	req *pb.UpdateDataPlaneHealthRequest,
) (*pb.UpdateDataPlaneHealthResponse, error) {
	fmt.Println("Updating data plane health")

	if req == nil {
		return nil, errors.New("empty update dataplane health request")
	}

	return &pb.UpdateDataPlaneHealthResponse{}, nil
}

func (cs *commandService) Subscribe(in pb.CommandService_SubscribeServer) error {
	ctx := in.Context()

	go cs.listenForDataPlaneResponses(ctx, in)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			request := <-cs.requestChan
			fmt.Println("Sending config to agent")
			// This likely needs retry logic if we don't receive a response from agent (or bad response)
			if err := in.Send(request); err != nil {
				// Occasionally seeing "transport is closing" error, not sure why
				fmt.Printf("ERROR: %v\n", err)
			}
		}
	}
}

func (cs *commandService) listenForDataPlaneResponses(ctx context.Context, in pb.CommandService_SubscribeServer) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			dataPlaneResponse, err := in.Recv()
			fmt.Printf("Received data plane response: %v\n", dataPlaneResponse)
			if err != nil {
				fmt.Printf("Failed to receive data plane response: %v\n", err)
				return
			}
			cs.responseChan <- dataPlaneResponse
		}
	}
}
