package main

import (
	"github.com/faraonc/hwsc-user-svc/logtag"
	"google.golang.org/grpc"
	"log"
	"net"
	pb "github.com/faraonc/hwsc-api-blocks/int/hwsc-user-svc/proto"
	svc "github.com/faraonc/hwsc-user-svc/service"
)

const (
	connectType = "tcp"
	connectHost = "localhost"
	connectPort = "50051"
	connectAddress = connectHost + ":" + connectPort
)

func main() {
	log.Println(logtag.Info, "hwsc-user-svc initiating...")

	// make TCP listener, listen for incoming client requests
	lis, err := net.Listen(connectType, connectAddress)
	if err != nil {
		log.Fatalf(logtag.Fatal,"Failed to intialize TCP listener %v\n", err)
	}

	// implement all our methods/services in service/service.go

	// build: create an instance of gRPC server
	grpcServer := grpc.NewServer()

	// register our service implementation with gRPC server
	pb.RegisterUserServiceServer(grpcServer, &svc.Service{})
	log.Println(logtag.Info, "hws-user-svc at", connectAddress, "...")

	// start gRPC server
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf(logtag.Fatal, "Failted to serve %v\n", err)
	}

}