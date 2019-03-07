package service

import (
	"database/sql"
	"fmt"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	pbsvc "github.com/hwsc-org/hwsc-api-blocks/int/hwsc-user-svc/user"
	pblib "github.com/hwsc-org/hwsc-api-blocks/lib"
	authconst "github.com/hwsc-org/hwsc-lib/consts"
	"github.com/hwsc-org/hwsc-lib/logger"
	"github.com/hwsc-org/hwsc-user-svc/conf"
	"github.com/hwsc-org/hwsc-user-svc/consts"
	"github.com/ory/dockertest"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"testing"
	"time"
)

const (
	psqlVersion = "alpine"
	unitTestTag = "Unit Test -"
)

// spin up docker containers for psql
// run schema migrations
// seed test data in db if necessary
// destroy db container at end of unit test
func TestMain(m *testing.M) {
	logger.Info(unitTestTag, "Initializing Unit Test Setup")

	templateDirectory = "../tmpl"

	// uses a sensible default on windows (tcp/http) and linux/osx (socket)
	pool, err := dockertest.NewPool("")
	if err != nil {
		logger.Fatal(unitTestTag, "Could not connect to docker:", err.Error())
	}

	// pulls an image, creates a container based on it, and runs it
	resource, err := pool.Run(dbDriverName, psqlVersion,
		[]string{
			fmt.Sprintf("POSTGRES_PASSWORD=%s", conf.UserDB.Password),
			fmt.Sprintf("POSTGRES_DB=%s", conf.UserDB.Name),
		})
	if err != nil {
		logger.Fatal(unitTestTag, "Could not start resource:", err.Error())
	}

	// exponential backoff-retry, b/c the app in the container might not be ready to accept connections yet
	if err = pool.Retry(func() error {
		var err error

		// recreate connectionString because dockertest port uses special port
		connectionString = fmt.Sprintf(
			"host=%s user=%s password=%s dbname=%s sslmode=%s port=%s ",
			conf.UserDB.Host, conf.UserDB.User, conf.UserDB.Password,
			conf.UserDB.Name, conf.UserDB.SSLMode, resource.GetPort("5432/tcp"))

		postgresDB, err = sql.Open(dbDriverName, connectionString)
		if err != nil {
			return err
		}
		return postgresDB.Ping()
	}); err != nil {
		logger.Fatal(unitTestTag, "Could not connect to docker:", err.Error())
	}

	// create a postgres driver for migration
	driver, err := postgres.WithInstance(postgresDB, &postgres.Config{})
	if err != nil {
		logger.Fatal(unitTestTag, "Failed to start postgres Instance:", err.Error())
	}

	// create a migration instance
	migration, err := migrate.NewWithDatabaseInstance(
		"file://test_fixtures/psql",
		"postgres", driver,
	)
	if err != nil {
		logger.Fatal(unitTestTag, "Failed to create a migration instance:", err.Error())
	}

	// run all migration up to the most active
	if err := migration.Up(); err != nil {
		logger.Fatal(unitTestTag, "Failed to load active migration files: %s", err.Error())
	}
	// seed data if necessary

	// start the tests
	code := m.Run()

	// When unit test is done running, kill and remove the container
	// Cannot defer this b/c os.Exit doesn't care for defer
	if err := pool.Purge(resource); err != nil {
		logger.Fatal(unitTestTag, "Could not purge docker resources:", err.Error())
	}

	os.Exit(code)
}

func TestGetStatus(t *testing.T) {
	// test service state locker
	cases := []struct {
		request     *pbsvc.UserRequest
		serverState state
		expMsg      string
	}{
		{&pbsvc.UserRequest{}, available, codes.OK.String()},
		{&pbsvc.UserRequest{}, unavailable, codes.Unavailable.String()},
	}

	for _, c := range cases {
		serviceStateLocker.currentServiceState = c.serverState
		s := Service{}
		response, _ := s.GetStatus(context.TODO(), c.request)
		assert.Equal(t, c.expMsg, response.GetMessage())
	}

	serviceStateLocker.currentServiceState = available
	s := Service{}

	// test refreshDBConnection
	err := postgresDB.Close()
	assert.Nil(t, err)

	response, _ := s.GetStatus(context.TODO(), &pbsvc.UserRequest{})
	assert.Equal(t, codes.Unavailable.String(), response.GetMessage())

	// reconnect
	err = refreshDBConnection()
	assert.Nil(t, err)
}

func TestCreateUser(t *testing.T) {
	// valid
	testUser1 := unitTestUserGenerator("CreateUser-One")

	// valid
	testUser2 := unitTestUserGenerator("CreateUser-Two")

	// fail: duplicate email test
	testUser3 := &pblib.User{
		FirstName:    unitTestDefaultUser.GetFirstName(),
		LastName:     "CreateUser Fail",
		Email:        testUser1.GetEmail(),
		Password:     unitTestFailValue,
		Organization: unitTestDefaultUser.GetOrganization(),
	}

	// fail: invalid fields in userobject (it will fail on firstname)
	testUser4 := &pblib.User{
		FirstName: "",
	}

	// fail: empty password
	testUser5 := &pblib.User{
		FirstName: unitTestDefaultUser.GetFirstName(),
		LastName:  "CreateUser Fail",
		Email:     unitTestFailEmail,
		Password:  "",
	}

	// fail: blank email
	testUser7 := &pblib.User{
		FirstName: unitTestDefaultUser.GetFirstName(),
		LastName:  "CreateUser Fail",
		Email:     "",
	}

	// fail: blank organization
	testUser8 := &pblib.User{
		FirstName:    unitTestDefaultUser.GetFirstName(),
		LastName:     "CreateUser Fail",
		Email:        unitTestFailEmail,
		Password:     unitTestFailValue,
		Organization: "",
	}

	// fail: blank last name
	testUser9 := &pblib.User{
		FirstName: unitTestDefaultUser.GetFirstName(),
		LastName:  "",
	}

	cases := []struct {
		request  *pbsvc.UserRequest
		isExpErr bool
		expMsg   string
	}{
		{nil, true, "rpc error: code = InvalidArgument desc = nil request User"},
		{&pbsvc.UserRequest{}, true, "rpc error: code = InvalidArgument " +
			"desc = nil request User"},
		{&pbsvc.UserRequest{User: testUser1}, false, codes.OK.String()},
		{&pbsvc.UserRequest{User: testUser2}, false, codes.OK.String()},
		{&pbsvc.UserRequest{User: testUser3}, true, "rpc error: code = " +
			"Internal desc = pq: duplicate key value violates unique constraint \"accounts_email_key\""},
		{&pbsvc.UserRequest{User: testUser4}, true, "rpc error: code = " +
			"Internal desc = invalid User first name"},
		{&pbsvc.UserRequest{User: testUser5}, true, "rpc error: code = " +
			"Internal desc = invalid User password"},
		{&pbsvc.UserRequest{User: testUser7}, true, "rpc error: code = " +
			"Internal desc = invalid User email"},
		{&pbsvc.UserRequest{User: testUser8}, true, "rpc error: code = " +
			"Internal desc = invalid User organization"},
		{&pbsvc.UserRequest{User: testUser9}, true, "rpc error: code = " +
			"Internal desc = invalid User last name"},
	}

	for _, c := range cases {
		s := Service{}
		response, err := s.CreateUser(context.TODO(), c.request)
		if c.isExpErr {
			assert.EqualError(t, err, c.expMsg)
		} else {
			assert.Equal(t, codes.OK.String(), response.GetMessage())
		}
	}
}

func TestDeleteUser(t *testing.T) {
	// insert valid user
	response, err := unitTestInsertUser("DeleteUser-One")
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response.GetMessage())

	// generate a valid uuid to test non existent uuid in table
	uuid, err := generateUUID()
	assert.Nil(t, err)
	assert.NotNil(t, uuid)

	// existing uuid
	test1 := &pblib.User{
		Uuid: response.GetUser().GetUuid(),
	}

	// nonexistent uuid
	test2 := &pblib.User{
		Uuid: uuid,
	}

	// invalid uuid's
	test3 := &pblib.User{
		Uuid: "",
	}
	test4 := &pblib.User{
		Uuid: "01d1nba01gnzbrkbfrrvgrz2m",
	}

	cases := []struct {
		request  *pbsvc.UserRequest
		isExpErr bool
		expMsg   string
	}{
		{&pbsvc.UserRequest{User: test1}, false, codes.OK.String()},
		{&pbsvc.UserRequest{User: test2}, false, codes.OK.String()},
		{&pbsvc.UserRequest{User: test3}, true,
			"rpc error: code = InvalidArgument desc = invalid uuid"},
		{&pbsvc.UserRequest{User: test4}, true,
			"rpc error: code = InvalidArgument desc = invalid uuid"},
		{&pbsvc.UserRequest{User: nil}, true,
			"rpc error: code = InvalidArgument desc = nil request User"},
		{nil, true, "rpc error: code = InvalidArgument desc = nil request User"},
	}

	for _, c := range cases {
		s := Service{}
		response, err := s.DeleteUser(context.TODO(), c.request)
		if c.isExpErr {
			assert.EqualError(t, err, c.expMsg)
			assert.Nil(t, response)
		} else {
			assert.Equal(t, codes.OK.String(), response.GetMessage())
			assert.Nil(t, err)
		}
	}
}

func TestGetUser(t *testing.T) {
	// insert valid user
	response, err := unitTestInsertUser("GetUser-One")
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response.GetMessage())

	// generate a valid uuid to test non existent uuid in table
	uuid, err := generateUUID()
	assert.Nil(t, err)
	assert.NotNil(t, uuid)

	// exisiting uuid
	test1 := &pblib.User{
		Uuid: response.GetUser().GetUuid(),
	}

	// nonexistent uuid
	test2 := &pblib.User{
		Uuid: uuid,
	}

	cases := []struct {
		request  *pbsvc.UserRequest
		isExpErr bool
		expMsg   string
	}{
		{&pbsvc.UserRequest{User: test1}, false, ""},
		{&pbsvc.UserRequest{User: test2}, true,
			"rpc error: code = Internal desc = user is not found in database"},
		{&pbsvc.UserRequest{User: nil}, true,
			"rpc error: code = InvalidArgument desc = nil request User"},
		{nil, true, "rpc error: code = InvalidArgument desc = nil request User"},
	}

	for _, c := range cases {
		s := Service{}
		response, err := s.GetUser(context.TODO(), c.request)

		if c.isExpErr {
			assert.EqualError(t, err, c.expMsg)
			assert.Nil(t, response)
		} else {
			assert.Nil(t, err)
			assert.Equal(t, codes.OK.String(), response.GetMessage())
		}
	}
}

func TestUpdateUser(t *testing.T) {
	// insert valid user 1
	response1, err := unitTestInsertUser("UpdateUser-One")
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response1.GetMessage())

	// insert valid user 2
	response2, err := unitTestInsertUser("UpdateUser-Two")
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response2.GetMessage())

	err = deleteEmailTokenRow(response2.GetUser().GetUuid())
	assert.Nil(t, err)

	nonExistingUUID, err := generateUUID()
	assert.Nil(t, err)

	// valid response1
	// prospective email is NULL
	// modified_date set
	updateUser := &pblib.User{
		LastName:     response1.GetUser().GetLastName() + " UPDATED",
		Password:     "newPassword",
		Organization: response1.GetUser().GetOrganization() + " UPDATED",
		Uuid:         response1.GetUser().GetUuid(),
	}

	// valid response2
	// test prospective_email is set
	// modified_date set
	newEmail := unitTestEmailGenerator()
	updateUser2 := &pblib.User{
		LastName: response1.GetUser().GetLastName() + " UPDATED",
		Email:    newEmail,
		Uuid:     response2.GetUser().GetUuid(),
	}

	// invalid using response2
	// test duplicated email
	updateUser8 := &pblib.User{
		Email: response1.GetUser().GetEmail(),
		Uuid:  response2.GetUser().GetUuid(),
	}

	// invalid using response1
	// test duplicated prospective email
	updateUser9 := &pblib.User{
		Email: newEmail,
		Uuid:  response1.GetUser().GetUuid(),
	}

	// fail - invalid uuid
	updateUser3 := &pblib.User{
		LastName: unitTestFailValue,
		Uuid:     "0000xsnjg0mqjhbf4qx",
	}

	// fail - non-existent uuid (uuid is in valid format)
	updateUser4 := &pblib.User{
		LastName: unitTestFailValue,
		Uuid:     nonExistingUUID,
	}

	// fail - invalid email format
	updateUser5 := &pblib.User{
		LastName: unitTestFailValue,
		Email:    "a",
		Uuid:     response2.GetUser().GetUuid(),
	}

	// fail - invalid first name
	updateUser6 := &pblib.User{
		FirstName: "@@@",
		Uuid:      response2.GetUser().GetUuid(),
	}

	// fail - invalid last name
	updateUser7 := &pblib.User{
		LastName: "@@@",
		Uuid:     response2.GetUser().GetUuid(),
	}

	cases := []struct {
		request  *pbsvc.UserRequest
		isExpErr bool
		expMsg   string
	}{
		{&pbsvc.UserRequest{User: updateUser}, false, ""},
		{&pbsvc.UserRequest{User: updateUser2}, false, ""},
		{nil, true, "rpc error: code = InvalidArgument desc = nil request User"},
		{&pbsvc.UserRequest{User: updateUser3}, true,
			"rpc error: code = InvalidArgument desc = invalid uuid"},
		{&pbsvc.UserRequest{User: updateUser4}, true,
			"rpc error: code = Internal desc = user is not found in database"},
		{&pbsvc.UserRequest{User: updateUser5}, true,
			"rpc error: code = Internal desc = invalid User email"},
		{&pbsvc.UserRequest{User: updateUser6}, true,
			"rpc error: code = Internal desc = invalid User first name"},
		{&pbsvc.UserRequest{User: updateUser7}, true,
			"rpc error: code = Internal desc = invalid User last name"},
		{&pbsvc.UserRequest{User: nil}, true,
			"rpc error: code = InvalidArgument desc = nil request User"},
		{&pbsvc.UserRequest{User: updateUser8}, true,
			"rpc error: code = Internal desc = email already exists"},
		{&pbsvc.UserRequest{User: updateUser9}, true,
			"rpc error: code = Internal desc = email already exists"},
	}

	for _, c := range cases {
		s := Service{}
		response, err := s.UpdateUser(context.TODO(), c.request)

		if c.isExpErr {
			assert.EqualError(t, err, c.expMsg)
			assert.Nil(t, response)
		} else {
			assert.Nil(t, err)
			assert.Equal(t, codes.OK.String(), response.GetMessage())
		}
	}
}

func TestAuthenticateUser(t *testing.T) {
	validPassword := "AuthenticateUser-One"

	response, err := unitTestInsertUser(validPassword)
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response.Message)

	createdUser := response.GetUser()
	assert.NotNil(t, createdUser)

	validEmail := createdUser.GetEmail()

	// valid user
	validUser := &pblib.User{
		Email:    validEmail,
		Password: validPassword,
	}

	// non existing email
	invalidUser2 := &pblib.User{
		Email:    unitTestFailEmail,
		Password: validPassword,
	}

	// non matching password
	invalidUser3 := &pblib.User{
		Email:    validEmail,
		Password: unitTestFailValue,
	}

	// invalid email form
	invalidUser4 := &pblib.User{
		Email:    "@",
		Password: validPassword,
	}

	// invalid password
	invalidUser5 := &pblib.User{
		Email:    validEmail,
		Password: "",
	}

	// missing email
	invalidUser6 := &pblib.User{
		Password: validPassword,
	}

	// missing password
	invalidUser7 := &pblib.User{
		Email: validEmail,
	}

	cases := []struct {
		request  *pbsvc.UserRequest
		isExpErr bool
		expMsg   string
	}{
		{&pbsvc.UserRequest{User: validUser}, false, ""},
		{nil, true, "rpc error: code = InvalidArgument desc = nil request User"},
		{&pbsvc.UserRequest{User: nil}, true,
			"rpc error: code = InvalidArgument desc = nil request User"},
		{&pbsvc.UserRequest{User: invalidUser2}, true,
			"rpc error: code = Unknown desc = email does not exist in db"},
		{&pbsvc.UserRequest{User: invalidUser3}, true,
			"rpc error: code = Unknown desc = crypto/bcrypt: hashedPassword is not the hash of the given password"},
		{&pbsvc.UserRequest{User: invalidUser4}, true,
			"rpc error: code = InvalidArgument desc = invalid User email"},
		{&pbsvc.UserRequest{User: invalidUser5}, true,
			"rpc error: code = InvalidArgument desc = invalid User password"},
		{&pbsvc.UserRequest{User: invalidUser6}, true,
			"rpc error: code = InvalidArgument desc = invalid User email"},
		{&pbsvc.UserRequest{User: invalidUser7}, true,
			"rpc error: code = InvalidArgument desc = invalid User password"},
	}

	for _, c := range cases {
		s := Service{}
		response, err := s.AuthenticateUser(context.TODO(), c.request)
		if c.isExpErr {
			assert.Nil(t, response)
			assert.EqualError(t, err, c.expMsg)
		} else {
			assert.Nil(t, err)
			assert.Equal(t, codes.OK.String(), response.Message)
		}
	}
}

func TestMakeNewSecret(t *testing.T) {
	// no need to perform a check in the db here using a DAO,
	// b/c this func is meant to be called by a client

	err := unitTestDeleteSecretTable()
	assert.Nil(t, err)

	// test for no active secret
	retrievedSecret, err := getActiveSecretRow()
	assert.EqualError(t, err, consts.ErrNoActiveSecretKeyFound.Error())
	assert.Nil(t, retrievedSecret)

	s := Service{}

	// test with no secret in table
	response, err := s.MakeNewSecret(context.TODO(), nil)
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response.Message)

	// test for the active secret
	retrievedSecret, err = getActiveSecretRow()
	assert.Nil(t, err)
	assert.NotNil(t, retrievedSecret)

	// test with a secret already in table
	response, err = s.MakeNewSecret(context.TODO(), nil)
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response.Message)

	// retrieve the newest secret
	retrievedNewestSecret, err := getActiveSecretRow()
	assert.Nil(t, err)
	assert.NotNil(t, retrievedNewestSecret)

	// test that two retrieved secrets are not equal
	assert.NotEqual(t, retrievedSecret.GetKey(), retrievedNewestSecret.GetKey())
}

func TestGetSecret(t *testing.T) {
	err := unitTestDeleteSecretTable()
	assert.Nil(t, err)

	s := Service{}

	// test secret is generated if no secret present
	response, err := s.GetSecret(context.TODO(), nil)
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), response.GetMessage())
	assert.NotEmpty(t, response.GetIdentification().GetSecret())

	// test it got inserted by retrieving the secret key
	secretKey, err := getLatestSecret(2)
	assert.Nil(t, err)
	assert.NotEmpty(t, secretKey)

	// retrieve the secret from active_secret table
	retrievedSecret, err := getActiveSecretRow()
	assert.Nil(t, err)
	assert.Equal(t, secretKey, retrievedSecret.GetKey())

	// get secret by service
	response, err = s.GetSecret(context.TODO(), nil)
	assert.Nil(t, err)
	assert.Equal(t, response.Identification.Secret.Key, retrievedSecret.Key)
	assert.Equal(t, response.Identification.Secret.CreatedTimestamp, retrievedSecret.CreatedTimestamp)
}

func TestGetAuthToken(t *testing.T) {
	lastName1 := "GetToken-One"
	lastName2 := "GetToken-Two"

	// refresh secret table
	retrievedSecret, err := unitTestDeleteInsertGetSecret()
	assert.Nil(t, err)
	assert.NotNil(t, retrievedSecret)
	currSecret = retrievedSecret

	// insert a user
	responseUser1, err := unitTestInsertUser(lastName1)
	assert.Nil(t, err)
	assert.NotEmpty(t, responseUser1)
	responseUser1.GetUser().Password = lastName1

	// insert another user to test setting of nil currSecret
	responseUser2, err := unitTestInsertUser(lastName2)
	assert.Nil(t, err)
	assert.NotEmpty(t, responseUser2)
	responseUser2.GetUser().Password = lastName2

	cases := []struct {
		request  *pbsvc.UserRequest
		isExpErr bool
		expMsg   string
	}{
		// valid
		{&pbsvc.UserRequest{User: responseUser1.GetUser()}, false, ""},
		// valid - test setting of nil currSecret to active secret retrieved from db
		{&pbsvc.UserRequest{User: responseUser2.GetUser()}, false, ""},
		// nil request object
		{nil, true, "rpc error: code = InvalidArgument desc = nil request User"},
		// nil user object
		{&pbsvc.UserRequest{User: nil}, true, "rpc error: code = InvalidArgument desc = nil request User"},
		// user contains invalid uuid
		{&pbsvc.UserRequest{
			User: &pblib.User{
				Uuid:     "invalid",
				Email:    responseUser1.GetUser().GetEmail(),
				Password: responseUser1.GetUser().GetPassword(),
			}},
			true, "rpc error: code = InvalidArgument desc = invalid uuid",
		},
		// user contains invalid email
		{&pbsvc.UserRequest{
			User: &pblib.User{
				Uuid:     responseUser1.GetUser().GetUuid(),
				Email:    "@",
				Password: responseUser1.GetUser().GetPassword(),
			}},
			true, "rpc error: code = InvalidArgument desc = invalid User email",
		},
		// user contains invalid password
		{&pbsvc.UserRequest{
			User: &pblib.User{
				Uuid:     responseUser1.GetUser().GetUuid(),
				Email:    responseUser1.GetUser().GetEmail(),
				Password: "",
			}},
			true, "rpc error: code = InvalidArgument desc = invalid User password",
		},
	}

	var existingIdentification *pblib.Identification
	for index, c := range cases {
		s := Service{}
		if index == 1 {
			// test setting of nil currSecret to active secret retrieved from db
			currSecret = nil
		}
		response, err := s.GetAuthToken(context.TODO(), c.request)

		if c.isExpErr {
			assert.EqualError(t, err, c.expMsg)
			assert.Nil(t, response)
		} else if index == 1 {
			desc := "test setting of nil currSecret to active secret retrieved from db"
			assert.Nil(t, err, desc)
			assert.Equal(t, codes.OK.String(), response.GetMessage(), desc)
			assert.Equal(t, response.GetIdentification().GetSecret().GetKey(), retrievedSecret.GetKey(), desc)
		} else {
			existingIdentification = response.GetIdentification()
			assert.Nil(t, err)
			assert.Equal(t, codes.OK.String(), response.GetMessage())
			assert.NotEmpty(t, response.GetIdentification())
			assert.NotEmpty(t, response.GetIdentification().GetSecret())
			assert.NotEmpty(t, response.GetIdentification().GetToken())
		}
	}

	// check for retrieval of same token already in db
	s := Service{}
	response, err := s.GetAuthToken(context.TODO(), &pbsvc.UserRequest{User: responseUser1.GetUser()})
	assert.Nil(t, err)
	assert.Exactly(t, existingIdentification, response.GetIdentification())
	assert.Equal(t, existingIdentification.GetToken(), response.GetIdentification().GetToken())

	secret := response.GetIdentification().GetSecret()
	assert.Equal(t, existingIdentification.GetSecret().GetKey(), secret.GetKey())
	assert.Equal(t, existingIdentification.GetSecret().GetCreatedTimestamp(), secret.GetCreatedTimestamp())
	assert.Equal(t, existingIdentification.GetSecret().GetExpirationTimestamp(), secret.GetExpirationTimestamp())
}

func TestVerifyAuthToken(t *testing.T) {
	nonExistingToken := &pblib.Identification{
		Token: "TestVerifyAuthToken-DoesNotExist",
	}

	cases := []struct {
		desc   string
		req    *pbsvc.UserRequest
		expMsg string
	}{
		{"test nil request object", nil,
			"rpc error: code = InvalidArgument desc = nil request User",
		},
		{"test nil identity object", &pbsvc.UserRequest{Identification: nil},
			"rpc error: code = InvalidArgument desc = nil request identification",
		},
		{"test non-existent token", &pbsvc.UserRequest{Identification: nonExistingToken},
			"rpc error: code = Unauthenticated desc = no matching auth token were found with given token",
		},
	}

	for _, c := range cases {
		s := Service{}

		response, err := s.VerifyAuthToken(context.TODO(), c.req)
		assert.EqualError(t, err, c.expMsg, c.desc)
		assert.Nil(t, response, c.desc)
	}

	newSecret, newToken, err := unitTestInsertNewToken()
	assert.Nil(t, err)
	assert.NotNil(t, newSecret)
	assert.NotEmpty(t, newToken)

	desc := "Test existing token"
	s := Service{}
	identity := &pblib.Identification{
		Token: newToken,
	}
	response, err := s.VerifyAuthToken(context.TODO(), &pbsvc.UserRequest{Identification: identity})
	assert.Nil(t, err, desc)
	assert.Equal(t, newToken, response.GetIdentification().GetToken(), desc)

	responseSecret := response.GetIdentification().GetSecret()
	assert.Equal(t, newSecret.GetKey(), responseSecret.GetKey(), desc)
	assert.Equal(t, newSecret.GetCreatedTimestamp(), responseSecret.GetCreatedTimestamp(), desc)
	assert.Equal(t, newSecret.GetExpirationTimestamp(), responseSecret.GetExpirationTimestamp(), desc)
}

func TestVerifyEmailToken(t *testing.T) {
	// create user 1 to emulate new user
	user1, err := unitTestInsertUser("VerifyEmailToken-NewUser")
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), user1.GetMessage())

	// create user 2 to emulate existing user (requires updating this user)
	user2, err := unitTestInsertUser("VerifyEmailToken-ExistingUser")
	assert.Nil(t, err)
	assert.Equal(t, codes.OK.String(), user2.GetMessage())
	updateData := &pblib.User{
		Email: unitTestEmailGenerator(),
		Uuid:  user2.GetUser().GetUuid(),
	}
	updatedUser2, err := updateUserRow(updateData.GetUuid(), updateData, user2.GetUser())
	assert.Nil(t, err)
	assert.Equal(t, user2.GetUser().GetUuid(), updatedUser2.GetUuid())
	assert.Equal(t, false, updatedUser2.GetIsVerified())
	assert.NotEmpty(t, updatedUser2.GetProspectiveEmail())

	// remove the existing tokens so we can manually create, insert and reference this token
	err = deleteEmailTokenRow(user1.GetUser().GetUuid())
	assert.Nil(t, err)
	err = deleteEmailTokenRow(user2.GetUser().GetUuid())
	assert.Nil(t, err)

	// generate a token we can refer to
	user1EmailToken, err := generateSecretKey(emailTokenByteSize)
	assert.Nil(t, err)
	assert.NotEmpty(t, user1EmailToken)
	user2EmailToken, err := generateSecretKey(emailTokenByteSize)
	assert.Nil(t, err)
	assert.NotEmpty(t, user2EmailToken)

	// insert this token to test against
	err = insertEmailToken(user1.GetUser().GetUuid(), user1EmailToken)
	assert.Nil(t, err)
	err = insertEmailToken(user2.GetUser().GetUuid(), user2EmailToken)
	assert.Nil(t, err)

	// define test cases to test against non expired tokens
	notExpiredCases := []struct {
		desc     string
		req      *pbsvc.UserRequest
		isExpErr bool
		expMsg   string
	}{
		{"test nil req object", nil, true, consts.ErrStatusNilRequestUser.Error()},
		{"test nil identification object", &pbsvc.UserRequest{Identification: nil}, true,
			status.Error(codes.InvalidArgument, consts.ErrNilRequestIdentification.Error()).Error(),
		},
		{"test empty token string", &pbsvc.UserRequest{Identification: &pblib.Identification{Token: ""}},
			true, status.Error(codes.InvalidArgument, authconst.ErrEmptyToken.Error()).Error(),
		},
		{"test non-existing token", &pbsvc.UserRequest{Identification: &pblib.Identification{Token: "1234"}},
			true, status.Error(codes.Internal, consts.ErrNoMatchingEmailTokenFound.Error()).Error(),
		},
		{"test valid new user", &pbsvc.UserRequest{Identification: &pblib.Identification{Token: user1EmailToken}},
			false, "",
		},
		{"test valid existing user", &pbsvc.UserRequest{Identification: &pblib.Identification{Token: user2EmailToken}},
			false, "",
		},
	}

	for _, c := range notExpiredCases {
		s := Service{}
		response, err := s.VerifyEmailToken(context.TODO(), c.req)
		if c.isExpErr {
			assert.EqualError(t, err, c.expMsg, c.desc)
			assert.Nil(t, response, c.desc)
		} else {
			assert.Nil(t, err, c.desc)
			assert.Equal(t, codes.OK.String(), response.GetMessage())
		}
	}

	// force expire the tokens for both new and existing user
	expiredTimestamp := time.Now().AddDate(0, 0, -5)

	command := `INSERT INTO user_svc.email_tokens(token, created_timestamp, expiration_timestamp, uuid)
				VALUES($1, $2, $3, $4)
				`

	_, err = postgresDB.Exec(command, user1EmailToken, time.Now(), expiredTimestamp, user1.GetUser().GetUuid())
	assert.Nil(t, err)
	_, err = postgresDB.Exec(command, user2EmailToken, time.Now(), expiredTimestamp, user2.GetUser().GetUuid())
	assert.Nil(t, err)

	expiredTestCase := []struct {
		desc       string
		req        *pbsvc.UserRequest
		deleteUser bool
	}{
		{"test expired token for new user",
			&pbsvc.UserRequest{Identification: &pblib.Identification{Token: user1EmailToken}}, true,
		},
		{"test expired token for existing user",
			&pbsvc.UserRequest{Identification: &pblib.Identification{Token: user2EmailToken}}, false,
		},
	}

	for _, c := range expiredTestCase {
		s := Service{}
		response, err := s.VerifyEmailToken(context.TODO(), c.req)
		assert.Nil(t, response, c.desc)
		assert.EqualError(t, err, status.Error(codes.DeadlineExceeded, consts.ErrExpiredEmailToken.Error()).Error(), c.desc)

		if c.deleteUser {
			retrievedUser, err := getUserRow(user1.GetUser().GetUuid())
			assert.EqualError(t, err, consts.ErrUserNotFound.Error())
			assert.Nil(t, retrievedUser, c.desc)
		} else {
			retrievedUser, err := getUserRow(user2.GetUser().GetUuid())
			assert.Nil(t, err)
			assert.Equal(t, user2.GetUser().GetUuid(), retrievedUser.GetUuid(), c.desc)
		}
	}
}
