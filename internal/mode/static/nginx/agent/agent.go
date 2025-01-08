package agent

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentgrpc "github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/nginx/agent/grpc"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . NginxUpdater

// NginxUpdater is an interface for updating NGINX using the NGINX agent.
type NginxUpdater interface {
	UpdateConfig(context.Context, types.NamespacedName, []File) error
	UpdateUpstreamServers()
}

// NginxUpdaterImpl implements the NginxUpdater interface.
type NginxUpdaterImpl struct {
	CommandService   *commandService
	FileService      *fileService
	nginxDeployments *DeploymentStore
	logger           logr.Logger
	plus             bool
}

// NewNginxUpdater returns a new NginxUpdaterImpl instance.
func NewNginxUpdater(
	logger logr.Logger,
	reader client.Reader,
	plus bool,
) *NginxUpdaterImpl {
	connTracker := agentgrpc.NewConnectionsTracker()
	nginxDeployments := NewDeploymentStore(connTracker)

	commandService := newCommandService(logger.WithName("commandService"), reader, nginxDeployments, connTracker)
	fileService := newFileService(logger.WithName("fileService"), nginxDeployments, connTracker)

	return &NginxUpdaterImpl{
		logger:           logger,
		plus:             plus,
		nginxDeployments: nginxDeployments,
		CommandService:   commandService,
		FileService:      fileService,
	}
}

// UpdateConfig sends the nginx configuration to the agent.
func (n *NginxUpdaterImpl) UpdateConfig(ctx context.Context, nsName types.NamespacedName, files []File) error {
	n.logger.Info("Sending nginx configuration to agent")

	deployment := n.nginxDeployments.GetOrStore(ctx, nsName)
	if deployment == nil {
		return fmt.Errorf("failed to register nginx deployment %q", nsName.Name)
	}

	// TODO(sberman): wait to send config until Deployment pods have all connected.
	// If an nginx Pod creation event triggered this update, then we should include that
	// pod name in the call to this function. Then we can wait for the DeploymentStore
	// to show that this Pod has connected, and proceed with sending the config.

	msg := deployment.SetFiles(files)

	if err := deployment.GetBroadcaster().Send(msg); err != nil {
		return fmt.Errorf("could not set nginx files: %w", err)
	}

	return nil
}

// UpdateUpstreamServers sends an APIRequest to the agent to update upstream servers using the NGINX Plus API.
// Only applicable when using NGINX Plus.
func (n *NginxUpdaterImpl) UpdateUpstreamServers() {
	if !n.plus {
		return
	}

	n.logger.Info("Updating upstream servers using NGINX Plus API")
}
