// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package bmc

import "context"

// FujitsuRedfishBMC is the Fujitsu-specific implementation of the BMC interface.
// Fujitsu iRMC exposes a standard Redfish API, so most operations delegate
// to RedfishBaseBMC.
type FujitsuRedfishBMC struct {
	*RedfishBaseBMC
}

// EnrollCertificate is not supported on Fujitsu iRMC. The Fujitsu Redfish
// schema (SecureBoot.v1_0_7) does not include SecureBootDatabases; key
// enrollment must be performed via the iRMC UI or a vendor-specific mechanism.
func (f *FujitsuRedfishBMC) EnrollCertificate(_ context.Context, _ string, _ string, _ string) error {
	return ErrNotSupported
}

// EnrollHash is not supported on Fujitsu iRMC for the same reason.
func (f *FujitsuRedfishBMC) EnrollHash(_ context.Context, _ string, _ string, _ string) error {
	return ErrNotSupported
}
