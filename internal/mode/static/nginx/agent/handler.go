package agent

import (
	"fmt"
	"time"

	pb "github.com/nginx/agent/v3/api/grpc/mpi/v1"
	filesHelper "github.com/nginx/agent/v3/pkg/files"

	ngfFile "github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/nginx/file"
)

// Think of a better name than Handler.
// Handler gets nginx configuration updates from the event handler, and
// triggers the series of operations to send that configuration to the agent/nginx instance.
type Handler struct {
	// requestChan sends requests to the CommandService, which sends on to the agent
	requestChan chan<- *pb.ManagementPlaneRequest
	// responseChan receives response from the agent via the CommandService subscription
	responseChan   <-chan *pb.DataPlaneResponse
	fileService    *fileService
	commandService *commandService

	lastWrittenFiles []*pb.File
}

func NewHandler() *Handler {
	requestChan := make(chan *pb.ManagementPlaneRequest)
	responseChan := make(chan *pb.DataPlaneResponse)

	return &Handler{
		requestChan:    requestChan,
		responseChan:   responseChan,
		fileService:    newFileService(),
		commandService: newCommandService(requestChan, responseChan),
	}
}

// Write accepts the files from the event handler and writes them to the event channel.
func (h *Handler) Write(files []ngfFile.File) {
	// We'll likely need a map of agentID -> {instanceID, channel} for every agent that connects, so that we can
	// send the config to the proper subscription. Each subscription would listen on a channel for its associated agent
	// connection. The event handler would build a config for agentX, and then call this Write function with agentX's ID
	// and files. For now, this PoC just supports one agent (so we hardcode the static ID below for the nginx instance
	// and Subscribe just listens on one static channel).
	// N1 uses middleware to get token and uuid from grpc connection headers and storing it in the context,
	// using that to map a connection/subscription to each other.
	// We would also need to send that uuid to the event handler or graph to link the agentID to its config.
	id := "e8d1bda6-397e-3b98-a179-e500ff99fbc7"

	if len(h.lastWrittenFiles) > 0 {
		configVersion := filesHelper.GenerateConfigVersion(h.lastWrittenFiles)

		deleteReq := &pb.ManagementPlaneRequest{
			Request: &pb.ManagementPlaneRequest_ConfigApplyRequest{
				ConfigApplyRequest: &pb.ConfigApplyRequest{
					ConfigVersion: &pb.ConfigVersion{
						InstanceId: string(id),
						Version:    configVersion,
					},
					Overview: &pb.FileOverview{
						Files: h.lastWrittenFiles,
						ConfigVersion: &pb.ConfigVersion{
							InstanceId: string(id),
							Version:    configVersion,
						},
					},
				},
			},
		}

		h.requestChan <- deleteReq
		select {
		case res := <-h.responseChan:
			if res.CommandResponse != nil && res.CommandResponse.Status != pb.CommandResponse_COMMAND_STATUS_OK {
				fmt.Println("Error response")
				return
			}
		case <-time.After(5 * time.Second):
			fmt.Println("timed out waiting for DELETE response")
			return
		}
	}

	agentFiles := convertFiles(files)
	fileOverviews := make([]*pb.File, 0, len(agentFiles))

	for _, f := range agentFiles {
		fileOverviews = append(fileOverviews, &pb.File{
			FileMeta: f.Meta,
			Action:   pb.File_FILE_ACTION_ADD.Enum(),
		})
	}

	configVersion := filesHelper.GenerateConfigVersion(fileOverviews)
	h.fileService.updateFileMap(configVersion, agentFiles)

	addReq := &pb.ManagementPlaneRequest{
		Request: &pb.ManagementPlaneRequest_ConfigApplyRequest{
			ConfigApplyRequest: &pb.ConfigApplyRequest{
				ConfigVersion: &pb.ConfigVersion{
					InstanceId: string(id),
					Version:    configVersion,
				},
				Overview: &pb.FileOverview{
					Files: fileOverviews,
					ConfigVersion: &pb.ConfigVersion{
						InstanceId: string(id),
						Version:    configVersion,
					},
				},
			},
		},
	}

	// This sends a config but doesn't wait for the command service to read it.
	// This is because if no agents have connected yet, the command service won't have subscribed
	// and read this message, leading to a blocking call.
	// In our real implementation, we shouldn't need this because we shouldn't be sending any configs if no
	// agents exist yet.
	select {
	case h.requestChan <- addReq:
	default:
	}

	select {
	case res := <-h.responseChan:
		if res.CommandResponse != nil && res.CommandResponse.Status != pb.CommandResponse_COMMAND_STATUS_OK {
			fmt.Println("Error response")
			return
		}
	case <-time.After(5 * time.Second):
		fmt.Println("timed out waiting for ADD response")
		return
	}

	h.lastWrittenFiles = fileOverviews
	// next update we'll delete the files we just added, and replace with the updated files
	for _, f := range h.lastWrittenFiles {
		f.Action = pb.File_FILE_ACTION_DELETE.Enum()
	}
}

// Converts our current File struct to a File struct that contains all info needed for the agent.
// In the future this won't be needed since our generator should just generate
// this File type.
func convertFiles(files []ngfFile.File) []file {
	agentFiles := make([]file, 0, len(files))

	for _, f := range files {
		agentFile := file{
			Meta: &pb.FileMeta{
				Name:        f.Path,
				Hash:        filesHelper.GenerateHash(f.Content),
				Permissions: "0644",
			},
			Contents: f.Content,
		}

		agentFiles = append(agentFiles, agentFile)
	}

	return agentFiles
}
