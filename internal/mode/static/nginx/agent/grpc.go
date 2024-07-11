package agent

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/bufbuild/protovalidate-go"
	protovalidateInterceptor "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	grpcvalidator "github.com/grpc-ecosystem/go-grpc-middleware/validator"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type GRPCServer struct {
	Handler *Handler
	Port    int
}

func (g *GRPCServer) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", g.Port))
	if err != nil {
		return err
	}

	validator, _ := protovalidate.New()

	server := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			grpcvalidator.StreamServerInterceptor(),
			protovalidateInterceptor.StreamServerInterceptor(validator),
		),
		grpc.ChainUnaryInterceptor(
			grpcvalidator.UnaryServerInterceptor(),
			protovalidateInterceptor.UnaryServerInterceptor(validator),
		),
		grpc.KeepaliveEnforcementPolicy(
			keepalive.EnforcementPolicy{
				MinTime:             5 * time.Second,
				PermitWithoutStream: true,
			},
		),
		grpc.KeepaliveParams(
			keepalive.ServerParameters{
				Time:    5 * time.Second,
				Timeout: 10 * time.Second,
			},
		),
	)

	g.Handler.commandService.Register(server)
	g.Handler.fileService.Register(server)

	go func() {
		<-ctx.Done()
		fmt.Println("Shutting down GRPC Server")
		server.GracefulStop()
	}()

	return server.Serve(listener)
}

var _ manager.Runnable = &GRPCServer{}
