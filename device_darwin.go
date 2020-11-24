// Copyright 2020 Canonical Ltd.
// Licensed under the LGPLv3 with static-linking exception.
// See LICENCE file for details.

package tpm2

import "fmt"

// OpenTPMDevice is not implemented on darwin
func OpenTPMDevice(path string) (*TctiDeviceLinux, error) {
	return nil, fmt.Errorf("not implemented on darwin")
}
