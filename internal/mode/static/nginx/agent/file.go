package agent

import (
	"context"
	"fmt"
	"sync"

	pb "github.com/nginx/agent/v3/api/grpc/mpi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fileService struct {
	pb.FileServiceServer

	files map[string][]file
	lock  *sync.Mutex
}

type file struct {
	Meta     *pb.FileMeta
	Contents []byte
}

func newFileService() *fileService {
	return &fileService{
		files: make(map[string][]file),
		lock:  &sync.Mutex{},
	}
}

func (fs *fileService) Register(server *grpc.Server) {
	pb.RegisterFileServiceServer(server, fs)
}

// Saving every version of the nginx conf could easily lead to a memory overflow.
// We likely should just save the most recent config version (and maybe the previous).
func (fs *fileService) updateFileMap(configVersion string, files []file) {
	fs.lock.Lock()
	defer fs.lock.Unlock()

	fs.files[configVersion] = files
}

func (fs *fileService) GetOverview(
	_ context.Context,
	req *pb.GetOverviewRequest,
) (*pb.GetOverviewResponse, error) {
	fmt.Println("Get overview request")

	configVersion := req.GetConfigVersion()
	fs.lock.Lock()
	defer fs.lock.Unlock()

	files := fs.files[configVersion.GetVersion()]

	if files == nil {
		fmt.Println("Config version not found")
		return nil, status.Errorf(codes.NotFound, "Config version not found")
	}

	return &pb.GetOverviewResponse{
		Overview: &pb.FileOverview{
			ConfigVersion: configVersion,
			Files:         getOverviews(files),
		},
	}, nil
}

// Not convinced that this function needs to be implemented.
// This gets called by agent when files on agent are updated, but NGF is the source
// of truth, so this may not give us any new info.
func (fs *fileService) UpdateOverview(
	_ context.Context,
	req *pb.UpdateOverviewRequest,
) (*pb.UpdateOverviewResponse, error) {
	fmt.Println("Update overview request")

	return &pb.UpdateOverviewResponse{}, nil
}

func (fs *fileService) GetFile(
	_ context.Context,
	req *pb.GetFileRequest,
) (*pb.GetFileResponse, error) {
	filename := req.GetFileMeta().GetName()
	hash := req.GetFileMeta().GetHash()
	fmt.Printf("Getting file: %s, %s\n", filename, hash)

	var contents []byte
	for _, files := range fs.files {
		for _, file := range files {
			if filename == file.Meta.GetName() && hash == file.Meta.GetHash() {
				contents = file.Contents
				break
			}
		}
	}

	if len(contents) == 0 {
		fmt.Println("File not found")
		return nil, status.Errorf(codes.NotFound, "File not found")
	}

	return &pb.GetFileResponse{
		Contents: &pb.FileContents{
			Contents: contents,
		},
	}, nil
}

func (fs *fileService) UpdateFile(
	_ context.Context,
	req *pb.UpdateFileRequest,
) (*pb.UpdateFileResponse, error) {
	fmt.Println("Update file request for: ", req.GetFile().GetFileMeta().GetName())

	return &pb.UpdateFileResponse{}, nil
}

func getOverviews(files []file) []*pb.File {
	overviews := make([]*pb.File, 0, len(files))
	for _, f := range files {
		overviews = append(overviews, &pb.File{
			FileMeta: f.Meta,
		})
	}

	return overviews
}
