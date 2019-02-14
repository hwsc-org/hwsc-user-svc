package service

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	pblib "github.com/hwsc-org/hwsc-api-blocks/lib"
	"github.com/hwsc-org/hwsc-user-svc/consts"
	"github.com/oklog/ulid"
	"golang.org/x/crypto/bcrypt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxFirstNameLength  = 32
	maxLastNameLength   = 32
	emailTokenByteSize  = 32
	easternStandardTime = "EST"
	daysInWeek          = 7
)

var (
	tokenLocker         sync.Mutex
	uuidLocker          sync.Mutex
	multiSpaceRegex     = regexp.MustCompile(`[\s\p{Zs}]{2,}`)
	nameValidCharsRegex = regexp.MustCompile(`^[[:alpha:]]+((['.\s-][[:alpha:]\s])?[[:alpha:]]*)*$`)
)

func (s *stateLocker) isStateAvailable() bool {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if s.currentServiceState != available {
		return false
	}

	return true
}

func validateUser(user *pblib.User) error {
	if user == nil {
		return consts.ErrNilRequestUser
	}

	if err := validateFirstName(user.GetFirstName()); err != nil {
		return err
	}
	if err := validateLastName(user.GetLastName()); err != nil {
		return err
	}
	if err := validateEmail(user.GetEmail()); err != nil {
		return err
	}
	if err := validatePassword(user.GetPassword()); err != nil {
		return consts.ErrInvalidPassword
	}
	if err := validateOrganization(user.GetOrganization()); err != nil {
		return err
	}
	return nil
}

func validatePassword(password string) error {
	if strings.TrimSpace(password) == "" {
		return consts.ErrInvalidPassword
	}
	return nil
}

func validateFirstName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return consts.ErrInvalidUserFirstName
	}

	name = multiSpaceRegex.ReplaceAllString(name, " ")
	if len(name) > maxFirstNameLength || !nameValidCharsRegex.MatchString(name) {
		return consts.ErrInvalidUserFirstName
	}

	return nil
}

func validateLastName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return consts.ErrInvalidUserLastName
	}

	name = multiSpaceRegex.ReplaceAllString(name, " ")
	if len(name) > maxLastNameLength || !nameValidCharsRegex.MatchString(name) {
		return consts.ErrInvalidUserLastName
	}

	return nil
}

func validateOrganization(name string) error {
	if name == "" {
		return consts.ErrInvalidUserOrganization
	}
	return nil
}

// generateUUID generates a unique user ID using ulid package based on currentTime
// returns a lower cased string type of generated ulid.ULID
func generateUUID() (string, error) {
	uuidLocker.Lock()
	defer uuidLocker.Unlock()

	t := time.Now().UTC()
	entropy := rand.New(rand.NewSource(t.UnixNano()))

	id, err := ulid.New(ulid.Timestamp(t), entropy)
	if err != nil {
		return "", err
	}

	return strings.ToLower(id.String()), nil
}

// validateUUID ensures uuid is not a zero value and matches format set by ulid package
// Returns error if zero value or invalid uuid (determined by ulid package)
func validateUUID(uuid string) error {
	if uuid == "" {
		return consts.ErrInvalidUUID
	}

	id, err := ulid.ParseStrict(strings.ToUpper(uuid))
	if err != nil {
		if err.Error() == "ulid: bad data size when unmarshaling" {
			return consts.ErrInvalidUUID
		}
		return err
	}

	if strings.ToLower(id.String()) != uuid {
		return consts.ErrInvalidUUID
	}

	return nil
}

// hashPassword hashes and salts provided password
// returns stringified hashed password
func hashPassword(password string) (string, error) {
	if password == "" || strings.TrimSpace(password) != password {
		return "", consts.ErrInvalidPassword
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return "", err
	}

	return string(hashedPassword), nil
}

// comparePassword compares hashedPassword retrieved from DB and the password from User request
// Returns nil if match, error if not match or error from bcrypt
func comparePassword(hashedPassword string, password string) error {
	if hashedPassword == "" || password == "" {
		return consts.ErrInvalidPassword
	}

	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	if err != nil {
		return err
	}

	return nil
}

// generateRandomToken generates a base64 URL-safe string
// built from securely generated random bytes
// number of bytes is determined by tokenSize
// Return error if system's secure random number generator fails
func generateToken(tokenSize int) (string, error) {
	if tokenSize <= 0 {
		return "", consts.ErrInvalidTokenSize
	}

	tokenLocker.Lock()
	defer tokenLocker.Unlock()

	randomBytes := make([]byte, tokenSize)
	_, err := cryptorand.Read(randomBytes)
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(randomBytes), nil
}

// generateSecretExpirationDate returns the expiration date for secret keys used for signing JWT
// currently sets expiration date to every Monday at 3AM UTC
// Returns error if date object is nil or error with loading location
func generateSecretExpirationDate(currentDate time.Time) (*time.Time, error) {
	if currentDate.IsZero() {
		return nil, consts.ErrInvalidTimeDate
	}

	timeZonedDate := currentDate.UTC()
	currentWeekday := int(timeZonedDate.Weekday())

	addDays := ((daysInWeek - currentWeekday) % daysInWeek) + 1

	// add number of days to current weekday to get to Monday
	modifiedDate := currentDate.AddDate(0, 0, int(addDays))

	// reset time to 3 AM
	expirationDate := time.Date(modifiedDate.Year(), modifiedDate.Month(), modifiedDate.Day(),
		3, 0, 0, 0, timeZonedDate.Location())

	return &expirationDate, nil
}
