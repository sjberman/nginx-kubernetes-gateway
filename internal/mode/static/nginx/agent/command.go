package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	pb "github.com/nginx/agent/v3/api/grpc/mpi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/nginx/agent/broadcast"
	agentgrpc "github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/nginx/agent/grpc"
	grpcContext "github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/nginx/agent/grpc/context"
	"github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/nginx/agent/meta"
)

// commandService handles the connection and subscription to the data plane agent.
type commandService struct {
	pb.CommandServiceServer
	nginxDeployments *DeploymentStore
	connTracker      *agentgrpc.ConnectionsTracker
	k8sReader        client.Reader
	// TODO(sberman): all logs are at Info level right now. Adjust appropriately.
	logger logr.Logger
}

func newCommandService(
	logger logr.Logger,
	reader client.Reader,
	depStore *DeploymentStore,
	connTracker *agentgrpc.ConnectionsTracker,
) *commandService {
	return &commandService{
		k8sReader:        reader,
		logger:           logger,
		connTracker:      connTracker,
		nginxDeployments: depStore,
	}
}

func (cs *commandService) Register(server *grpc.Server) {
	pb.RegisterCommandServiceServer(server, cs)
}

// CreateConnection registers a data plane agent with the control plane.
// The nginx InstanceID could be empty if the agent hasn't discovered its nginx instance yet.
// Once discovered, the agent will send an UpdateDataPlaneStatus request with the nginx InstanceID set.
func (cs *commandService) CreateConnection(
	ctx context.Context,
	req *pb.CreateConnectionRequest,
) (*pb.CreateConnectionResponse, error) {
	if req == nil {
		return nil, errors.New("empty connection request")
	}

	gi, ok := grpcContext.GrpcInfoFromContext(ctx)
	if !ok {
		return nil, agentgrpc.ErrStatusInvalidConnection
	}

	resource := req.GetResource()
	podName := resource.GetContainerInfo().GetHostname()
	cs.logger.Info(fmt.Sprintf("Creating connection for nginx pod: %s", podName))

	owner, err := cs.getPodOwner(podName)
	if err != nil {
		response := &pb.CreateConnectionResponse{
			Response: &pb.CommandResponse{
				Status:  pb.CommandResponse_COMMAND_STATUS_ERROR,
				Message: "error getting pod owner",
				Error:   err.Error(),
			},
		}
		return response, status.Errorf(codes.Internal, "error getting pod owner %s", err.Error())
	}

	conn := agentgrpc.Connection{
		Parent:     owner,
		PodName:    podName,
		InstanceID: getNginxInstanceID(resource.GetInstances()),
	}
	cs.connTracker.Track(gi.IPAddress, conn)

	return &pb.CreateConnectionResponse{
		Response: &pb.CommandResponse{
			Status: pb.CommandResponse_COMMAND_STATUS_OK,
		},
	}, nil
}

// Subscribe is a decoupled communication mechanism between the data plane agent and control plane.
func (cs *commandService) Subscribe(in pb.CommandService_SubscribeServer) error {
	ctx := in.Context()

	gi, ok := grpcContext.GrpcInfoFromContext(ctx)
	if !ok {
		return agentgrpc.ErrStatusInvalidConnection
	}

	cs.logger.Info(fmt.Sprintf("Received subscribe request from %q", gi.IPAddress))

	// wait for the agent to report itself and nginx
	conn, deployment, err := cs.waitForConnection(ctx, gi)
	if err != nil {
		cs.logger.Error(err, "error waiting for connection")
		return err
	}

	// apply current config before starting listen loop
	deployment.RLock()
	fileOverviews, configVersion := deployment.GetFileOverviews()
	if err = in.Send(buildRequest(fileOverviews, conn.InstanceID, configVersion)); err != nil {
		fmt.Printf("ERROR applying initial config: %v\n", err)
		// TODO(sberman): how do we write this status?
	}

	for _, action := range deployment.GetNGINXPlusActions() {
		if err := in.Send(buildPlusAPIRequest(action, conn.InstanceID)); err != nil {
			fmt.Printf("ERROR applying initial API config: %v\n", err)
			// TODO(sberman): how do we write this status?
		}
	}
	deployment.RUnlock()

	if err == nil {
		cs.logger.Info(fmt.Sprintf("Successfully configured nginx for new subscription %q", conn.PodName))
	}

	// subscribe to the deployment broadcaster to get file updates
	broadcaster := deployment.GetBroadcaster()
	channels := broadcaster.Subscribe()
	defer broadcaster.CancelSubscription(channels.ID)

	go cs.listenForDataPlaneResponse(ctx, in, channels.ResponseCh)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-channels.ListenCh:
			deployment.RLock()
			var req *pb.ManagementPlaneRequest
			switch msg.Type {
			case broadcast.ConfigApplyRequest:
				req = buildRequest(msg.FileOverviews, conn.InstanceID, msg.ConfigVersion)
			case broadcast.APIRequest:
				req = buildPlusAPIRequest(msg.NGINXPlusAction, conn.InstanceID)
			default:
				panic(fmt.Sprintf("unknown request type %d", msg.Type))
			}

			if err := in.Send(req); err != nil {
				deployment.RUnlock()
				cs.logger.Error(err, "error sending request to agent")
				channels.ResponseCh <- err

				return err
			}
			deployment.RUnlock()
		}
	}
}

// TODO(sberman): current issue: when control plane restarts, agent doesn't re-establish a CreateConnection call,
// so this fails.
func (cs *commandService) waitForConnection(
	ctx context.Context,
	gi grpcContext.GrpcInfo,
) (*agentgrpc.Connection, *Deployment, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	agentConnectErr := errors.New("timed out waiting for agent to register nginx")
	deploymentStoreErr := errors.New("timed out waiting for nginx deployment to be added to store")

	var err error
	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-timer.C:
			return nil, nil, err
		case <-ticker.C:
			if conn, ok := cs.connTracker.ConnectionIsReady(gi.IPAddress); ok {
				// connection has been established, now ensure that the deployment exists in the store
				if deployment, ok := cs.nginxDeployments.Get(conn.Parent); ok {
					return &conn, deployment, nil
				}
				err = deploymentStoreErr
				continue
			}
			err = agentConnectErr
		}
	}
}

func (cs *commandService) listenForDataPlaneResponse(
	ctx context.Context,
	in pb.CommandService_SubscribeServer,
	responseCh chan<- error,
) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			dataPlaneResponse, err := in.Recv()
			if err != nil && !strings.Contains(err.Error(), "context canceled") {
				cs.logger.Error(err, "failed to receive data plane response")
				return
			}

			res := dataPlaneResponse.GetCommandResponse()
			if res.GetStatus() != pb.CommandResponse_COMMAND_STATUS_OK {
				err := fmt.Errorf("bad response from agent: %s; error: %s", res.GetMessage(), res.GetError())
				responseCh <- err
			} else {
				responseCh <- nil
			}
		}
	}
}

func buildRequest(fileOverviews []*pb.File, instanceID, version string) *pb.ManagementPlaneRequest {
	return &pb.ManagementPlaneRequest{
		MessageMeta: &pb.MessageMeta{
			MessageId:     meta.GenerateMessageID(),
			CorrelationId: meta.GenerateMessageID(),
			Timestamp:     timestamppb.Now(),
		},
		Request: &pb.ManagementPlaneRequest_ConfigApplyRequest{
			ConfigApplyRequest: &pb.ConfigApplyRequest{
				Overview: &pb.FileOverview{
					Files: fileOverviews,
					ConfigVersion: &pb.ConfigVersion{
						InstanceId: instanceID,
						Version:    version,
					},
				},
			},
		},
	}
}

func buildPlusAPIRequest(action *pb.NGINXPlusAction, instanceID string) *pb.ManagementPlaneRequest {
	return &pb.ManagementPlaneRequest{
		MessageMeta: &pb.MessageMeta{
			MessageId:     meta.GenerateMessageID(),
			CorrelationId: meta.GenerateMessageID(),
			Timestamp:     timestamppb.Now(),
		},
		Request: &pb.ManagementPlaneRequest_ActionRequest{
			ActionRequest: &pb.APIActionRequest{
				InstanceId: instanceID,
				Action: &pb.APIActionRequest_NginxPlusAction{
					NginxPlusAction: action,
				},
			},
		},
	}
}

func (cs *commandService) getPodOwner(podName string) (types.NamespacedName, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var pods v1.PodList
	listOpts := &client.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"metadata.name": podName}),
	}
	if err := cs.k8sReader.List(ctx, &pods, listOpts); err != nil {
		return types.NamespacedName{}, fmt.Errorf("error listing pods: %w", err)
	}

	if len(pods.Items) > 1 {
		return types.NamespacedName{}, fmt.Errorf("should only be one pod with name %q", podName)
	}
	pod := pods.Items[0]

	podOwnerRefs := pod.GetOwnerReferences()
	if len(podOwnerRefs) != 1 {
		return types.NamespacedName{}, fmt.Errorf("expected one owner reference of the NGF Pod, got %d", len(podOwnerRefs))
	}

	if podOwnerRefs[0].Kind != "ReplicaSet" {
		err := fmt.Errorf("expected pod owner reference to be ReplicaSet, got %s", podOwnerRefs[0].Kind)
		return types.NamespacedName{}, err
	}

	var replicaSet appsv1.ReplicaSet
	if err := cs.k8sReader.Get(
		ctx,
		types.NamespacedName{Namespace: pod.Namespace, Name: podOwnerRefs[0].Name},
		&replicaSet,
	); err != nil {
		return types.NamespacedName{}, fmt.Errorf("failed to get NGF Pod's ReplicaSet: %w", err)
	}

	replicaOwnerRefs := replicaSet.GetOwnerReferences()
	if len(replicaOwnerRefs) != 1 {
		err := fmt.Errorf("expected one owner reference of the NGF ReplicaSet, got %d", len(replicaOwnerRefs))
		return types.NamespacedName{}, err
	}

	return types.NamespacedName{Namespace: pod.Namespace, Name: replicaOwnerRefs[0].Name}, nil
}

// UpdateDataPlaneHealth includes full health information about the data plane as reported by the agent.
// TODO(sberman): Is health monitoring the data planes something useful for us to do?
func (cs *commandService) UpdateDataPlaneHealth(
	_ context.Context,
	_ *pb.UpdateDataPlaneHealthRequest,
) (*pb.UpdateDataPlaneHealthResponse, error) {
	return &pb.UpdateDataPlaneHealthResponse{}, nil
}

// UpdateDataPlaneStatus is called by agent on startup and upon any change in agent metadata,
// instance metadata, or configurations. InstanceID may not be set on an initial CreateConnection,
// and will instead be set on a call to UpdateDataPlaneStatus once the agent discovers its nginx instance.
func (cs *commandService) UpdateDataPlaneStatus(
	ctx context.Context,
	req *pb.UpdateDataPlaneStatusRequest,
) (*pb.UpdateDataPlaneStatusResponse, error) {
	if req == nil {
		return nil, errors.New("empty UpdateDataPlaneStatus request")
	}

	gi, ok := grpcContext.GrpcInfoFromContext(ctx)
	if !ok {
		return nil, agentgrpc.ErrStatusInvalidConnection
	}

	instanceID := getNginxInstanceID(req.GetResource().GetInstances())
	if instanceID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "request does not contain nginx instanceID")
	}

	cs.connTracker.SetInstanceID(gi.IPAddress, instanceID)

	return &pb.UpdateDataPlaneStatusResponse{}, nil
}

func getNginxInstanceID(instances []*pb.Instance) string {
	for _, instance := range instances {
		instanceType := instance.GetInstanceMeta().GetInstanceType()
		if instanceType == pb.InstanceMeta_INSTANCE_TYPE_NGINX ||
			instanceType == pb.InstanceMeta_INSTANCE_TYPE_NGINX_PLUS {
			return instance.GetInstanceMeta().GetInstanceId()
		}
	}

	return ""
}
