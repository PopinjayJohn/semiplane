package auth

import "fmt"

// HashPasswordWithIterations exposes hashPassword to the black-box tests.
//
// It exists only in test builds, and it is a test knob rather than a package
// variable: the iteration count is a parameter, so nothing outside a test can
// reach it and no test can leak a lowered work factor into the next one.
//
// The exported tests use it to build hashes at a low work factor and then verify
// them through the real VerifyPassword, which is itself the assertion that the
// verifier takes the count from the encoded string rather than from a constant.
func HashPasswordWithIterations(password string, iterations int) (string, error) {
	if iterations <= 0 || iterations > pbkdf2MaxIterations {
		return "", fmt.Errorf("auth: test iteration count %d is out of range", iterations)
	}

	return hashPassword(password, iterations)
}
