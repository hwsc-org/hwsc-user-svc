package service

import (
	pb "github.com/hwsc-org/hwsc-api-blocks/int/hwsc-user-svc/proto"
	"github.com/hwsc-org/hwsc-logger/logger"
	"github.com/hwsc-org/hwsc-user-svc/conf"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sync"
)

// Service struct type, implements the generated (pb file) UserServiceServer interface
type Service struct{}

// state of the service
type state uint32

// stateLocker synchronizes the state of the service
type stateLocker struct {
	lock                sync.RWMutex
	currentServiceState state
}

const (
	// available - service is ready and available for read/write
	available state = 0

	// unavailable - service is locked
	unavailable state = 1
)

var (
	serviceStateLocker stateLocker
	uuidLocker         sync.Mutex
	tokenLocker        sync.Mutex
	uuidMapLocker      sync.Map

	// converts the state of the service to a string
	serviceStateMap map[state]string
)

func init() {
	serviceStateLocker = stateLocker{
		currentServiceState: available,
	}

	serviceStateMap = map[state]string{
		available:   "Available",
		unavailable: "Unavailable",
	}
}

// GetStatus gets the current status of the service
// Returns status code int and status code text, and any connection errors
func (s *Service) GetStatus(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	logger.RequestService("GetStatus")

	if ok := serviceStateLocker.isStateAvailable(); !ok {
		return responseServiceUnavailable, nil
	}

	if err := refreshDBConnection(); err != nil {
		return responseServiceUnavailable, nil
	}

	return &pb.UserResponse{
		Status:  &pb.UserResponse_Code{Code: uint32(codes.OK)},
		Message: codes.OK.String(),
	}, nil
}

// CreateUser creates a new user document and inserts it to user DB
func (s *Service) CreateUser(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	logger.RequestService("CreateUser")

	if ok := serviceStateLocker.isStateAvailable(); !ok {
		logger.Error("CreateUser: ", errServiceUnavailable.Error())
		return nil, status.Error(codes.Unavailable, errServiceUnavailable.Error())
	}

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	if err := refreshDBConnection(); err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}

	// get User Object
	user := req.GetUser()
	if user == nil {
		logger.Error(errNilRequestUser.Error())
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	// validate fields in user object
	if err := validateUser(user); err != nil {
		logger.Error("CreateUser svc:", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// generate uuid synchronously to prevent users getting the same uuid
	uuid, err := generateUUID()
	if err != nil {
		logger.Error("CreateUser generateUUID:", err.Error())
		return nil, status.Error(codes.Unknown, err.Error())
	}
	user.Uuid = uuid

	// sync.Map equivalent to map[string](&sync.RWMutex{}) = each uuid string gets its own lock
	// LoadOrStore = LOAD: get the lock for uuid or if not exist,
	// 				 STORE: make uuid key and store lock type &sync.RWMutex{}
	lock, _ := uuidMapLocker.LoadOrStore(user.GetUuid(), &sync.RWMutex{})
	lock.(*sync.RWMutex).Lock()
	defer lock.(*sync.RWMutex).Unlock()

	// hash password using bcrypt
	hashedPassword, err := hashPassword(user.GetPassword())
	if err != nil {
		logger.Error("CreateUser hashPassword:", err.Error())
		return nil, status.Error(codes.Unknown, err.Error())
	}
	user.Password = hashedPassword
	user.IsVerified = false

	// insert user into DB
	if err := insertNewUser(user); err != nil {
		logger.Error("CreateUser INSERT user_svc.accounts:", err.Error())
		return nil, status.Error(codes.Unknown, err.Error())
	}
	logger.Info("Success INSERT new user:", user.GetFirstName(), user.GetLastName(), user.GetUuid())

	// create unique email token
	token, err := generateEmailToken()
	if err != nil {
		logger.Error("CreateUser generateEmailToken:", err.Error())
		return nil, status.Error(codes.Unknown, err.Error())
	}

	// insert token into db
	if err := insertToken(user.GetUuid(), token); err != nil {
		logger.Error("CreateUser INSERT user_svc.pending_tokens:", err.Error())
		if err := deleteUser(user.GetUuid()); err != nil {
			logger.Error("CreateUser DELETE user-svc.accounts:", err.Error())
		}
		logger.Info("Deleted user: ", user.GetUuid())
		return nil, status.Error(codes.Unknown, err.Error())
	}
	logger.Info("Success INSERT token:", user.GetFirstName(), user.GetLastName(), user.GetUuid())

	// send email
	emailReq, err := newEmailRequest(nil, []string{user.GetEmail()}, conf.EmailHost.Username, subjectVerifyEmail)
	if err != nil {
		logger.Error("CreateUser newEmailRequest: ", err.Error())
		return nil, status.Error(codes.Unknown, err.Error())
	}

	if err := emailReq.sendEmail(templateVerifyEmail); err != nil {
		logger.Error("CreateUser sendEmail: ", err.Error())
		return nil, status.Error(codes.Unknown, err.Error())
	}

	return &pb.UserResponse{
		Status:  &pb.UserResponse_Code{Code: uint32(codes.OK)},
		Message: codes.OK.String(),
		User:    &pb.User{Uuid: user.GetUuid()},
	}, nil
}

// DeleteUser deletes a user document in user DB
func (s *Service) DeleteUser(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	logger.RequestService("DeleteUser")

	if ok := serviceStateLocker.isStateAvailable(); !ok {
		logger.Error("DeleteUser: ", errServiceUnavailable.Error())
		return nil, status.Error(codes.Unavailable, errServiceUnavailable.Error())
	}

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	if err := refreshDBConnection(); err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}

	// get User Object
	user := req.GetUser()
	if user == nil {
		logger.Error(errNilRequestUser.Error())
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	// validate user id
	if err := validateUUID(user.GetUuid()); err != nil {
		logger.Error("DeleteUser validating uuid: ", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	lock, _ := uuidMapLocker.LoadOrStore(user.GetUuid(), &sync.RWMutex{})
	lock.(*sync.RWMutex).Lock()
	defer lock.(*sync.RWMutex).Unlock()

	// check uuid exists
	exists, err := checkUserExists(user.GetUuid())
	if err != nil {
		// remove uuid from cache uuidMapLocker b/c Mutex was allocated to save resources/memory and prevent security issues
		uuidMapLocker.Delete(user.GetUuid())
		logger.Error("DeleteUser checkUserExists:", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	if !exists {
		logger.Error(errDoesNotExistUUID.Error())
		return nil, status.Error(codes.NotFound, errDoesNotExistUUID.Error())
	}

	// delete from db
	if err := deleteUser(user.GetUuid()); err != nil {
		logger.Error("DeleteUser DELETE user-svc.accounts:", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &pb.UserResponse{
		Status:  &pb.UserResponse_Code{Code: uint32(codes.OK)},
		Message: codes.OK.String(),
	}, nil
}

// UpdateUser updates a user document in user DB
func (s *Service) UpdateUser(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	logger.RequestService("UpdateUser")

	if ok := serviceStateLocker.isStateAvailable(); !ok {
		logger.Error("UpdateUser: ", errServiceUnavailable.Error())
		return nil, status.Error(codes.Unavailable, errServiceUnavailable.Error())
	}

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	if err := refreshDBConnection(); err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}

	// get User Object
	svcDerivedUser := req.GetUser()
	if svcDerivedUser == nil {
		logger.Error(errNilRequestUser.Error())
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	// validate user id
	if err := validateUUID(svcDerivedUser.GetUuid()); err != nil {
		logger.Error("UpdateUser validating uuid: ", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	lock, _ := uuidMapLocker.LoadOrStore(svcDerivedUser.GetUuid(), &sync.RWMutex{})
	lock.(*sync.RWMutex).Lock()
	defer lock.(*sync.RWMutex).Unlock()

	// check uuid exists
	exists, err := checkUserExists(svcDerivedUser.GetUuid())
	if err != nil {
		uuidMapLocker.Delete(svcDerivedUser.GetUuid())
		logger.Error("UpdateUser checkUserExists:", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	if !exists {
		logger.Error(errDoesNotExistUUID.Error())
		return nil, status.Error(codes.NotFound, errDoesNotExistUUID.Error())
	}

	// retrieve users row from database
	dbDerivedUser, err := getUserRow(svcDerivedUser.GetUuid())
	if err != nil {
		logger.Error("UpdateUser getUserRow:", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	if dbDerivedUser == nil {
		logger.Error("UpdateUser getUserRow:", errDoesNotExistUUID.Error())
		return nil, status.Error(codes.Internal, errDoesNotExistUUID.Error())
	}

	// update user
	if err := updateUserRow(svcDerivedUser.GetUuid(), svcDerivedUser, dbDerivedUser); err != nil {
		logger.Error("UpdateUser updating user:", err.Error())
		return nil, status.Error(codes.Unknown, err.Error())
	}

	return &pb.UserResponse{
		Status:  &pb.UserResponse_Code{Code: uint32(codes.OK)},
		Message: codes.OK.String(),
	}, nil
}

// AuthenticateUser goes through user DB collection and tries to find matching email/password
func (s *Service) AuthenticateUser(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	//TODO
	logger.RequestService("AuthenticateUser")
	return &pb.UserResponse{}, nil
}

// ListUsers returns the user DB collection
func (s *Service) ListUsers(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	//TODO
	logger.RequestService("ListUsers")
	return &pb.UserResponse{}, nil
}

// GetUser returns a user document in user DB
func (s *Service) GetUser(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	logger.RequestService("GetUser")

	if ok := serviceStateLocker.isStateAvailable(); !ok {
		logger.Error("GetUser: ", errServiceUnavailable.Error())
		return nil, status.Error(codes.Unavailable, errServiceUnavailable.Error())
	}

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	if err := refreshDBConnection(); err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}

	// get User Object
	user := req.GetUser()
	if user == nil {
		logger.Error(errNilRequestUser.Error())
		return nil, status.Error(codes.InvalidArgument, errNilRequestUser.Error())
	}

	// validate user id
	if err := validateUUID(user.GetUuid()); err != nil {
		logger.Error("GetUser validating uuid: ", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	lock, _ := uuidMapLocker.LoadOrStore(user.GetUuid(), &sync.RWMutex{})
	lock.(*sync.RWMutex).Lock()
	defer lock.(*sync.RWMutex).Unlock()

	// check uuid exists
	exists, err := checkUserExists(user.GetUuid())
	if err != nil {
		uuidMapLocker.Delete(user.GetUuid())
		logger.Error("GetUser checkUserExists:", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	if !exists {
		logger.Error(errDoesNotExistUUID.Error())
		return nil, status.Error(codes.NotFound, errDoesNotExistUUID.Error())
	}

	// retrieve users row from database
	user, err = getUserRow(user.GetUuid())
	if err != nil {
		logger.Error("GetUser getUserRow:", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	if user == nil {
		logger.Error("GetUser getUserRow:", errDoesNotExistUUID.Error())
		return nil, status.Error(codes.Internal, errDoesNotExistUUID.Error())
	}

	return &pb.UserResponse{
		Status:  &pb.UserResponse_Code{Code: uint32(codes.OK)},
		Message: codes.OK.String(),
		User:    user,
	}, nil
}

// ShareDocument updates user/s documents shared_to_me field in user DB
func (s *Service) ShareDocument(ctx context.Context, req *pb.UserRequest) (*pb.UserResponse, error) {
	//TODO
	logger.RequestService("ShareDocument")
	return &pb.UserResponse{}, nil
}
