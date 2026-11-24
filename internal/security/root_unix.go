//go:build !windows

package security

import (
	"errors"
	"syscall"
)

const openNoFollowFlag = syscall.O_NOFOLLOW

func isNoFollowErr(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}
