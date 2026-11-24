//go:build windows

package security

const openNoFollowFlag = 0

func isNoFollowErr(err error) bool {
	return false
}
