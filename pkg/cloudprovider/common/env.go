package common

import (
	"fmt"
	"os"
)

// RequireEnv returns the value of the named environment variable or an error if unset.
func RequireEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s not set", name)
	}
	return v, nil
}
