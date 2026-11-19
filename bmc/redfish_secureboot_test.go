// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package bmc

import (
	"context"
	"net"
	"net/http"

	"github.com/stmcginnis/gofish"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ironcore-dev/metal-operator/bmc/mock/server"
)

const testSystemURI = "/redfish/v1/Systems/437XR1138R2"

var _ = Describe("SecureBoot", func() {
	var (
		ms     *server.MockServer
		bmc    *RedfishBaseBMC
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := listener.Addr().String()
		Expect(listener.Close()).To(Succeed())

		ms = server.NewMockServer(GinkgoLogr, addr)
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		go func() {
			defer GinkgoRecover()
			Expect(ms.Start(ctx)).To(Succeed())
		}()

		srvURL := "http://" + addr
		Eventually(func() error {
			resp, err := http.Get(srvURL + "/redfish/v1/")
			if err != nil {
				return err
			}
			return resp.Body.Close()
		}).Should(Succeed())

		client, err := gofish.ConnectContext(context.Background(), gofish.ClientConfig{
			Endpoint:  srvURL,
			Username:  "admin",
			Password:  "admin",
			BasicAuth: true,
		})
		Expect(err).NotTo(HaveOccurred())
		bmc = &RedfishBaseBMC{client: client}

		DeferCleanup(func() {
			ms.ResetSecureBootState(testSystemURI)
			cancel()
		})
	})

	Describe("GetSecureBootState", func() {
		It("returns the current state from the fixture", func(ctx SpecContext) {
			state, err := bmc.GetSecureBootState(ctx, testSystemURI)
			Expect(err).NotTo(HaveOccurred())
			// Fixture has SecureBootEnable: false, SecureBootMode: UserMode.
			Expect(state.Enabled).To(BeFalse())
			Expect(state.Mode).To(Equal(SecureBootModeUser))
		})
	})

	Describe("SetSecureBootEnabled", func() {
		It("enables Secure Boot via PATCH", func(ctx SpecContext) {
			Expect(bmc.SetSecureBootEnabled(ctx, testSystemURI, true)).To(Succeed())

			state := ms.GetSecureBootState(testSystemURI)
			Expect(state.Enabled).To(BeTrue())
		})

		It("disables Secure Boot via PATCH", func(ctx SpecContext) {
			Expect(bmc.SetSecureBootEnabled(ctx, testSystemURI, true)).To(Succeed())
			Expect(bmc.SetSecureBootEnabled(ctx, testSystemURI, false)).To(Succeed())

			state := ms.GetSecureBootState(testSystemURI)
			Expect(state.Enabled).To(BeFalse())
		})
	})

	Describe("ResetSecureBootKeys", func() {
		It("DeleteAllKeys sets mode to SetupMode and disables SB", func(ctx SpecContext) {
			Expect(bmc.ResetSecureBootKeys(ctx, testSystemURI, SecureBootResetDeleteAllKeys)).To(Succeed())

			state := ms.GetSecureBootState(testSystemURI)
			Expect(state.Mode).To(Equal("SetupMode"))
			Expect(state.Enabled).To(BeFalse())
		})

		It("DeletePK sets mode to SetupMode", func(ctx SpecContext) {
			Expect(bmc.ResetSecureBootKeys(ctx, testSystemURI, SecureBootResetDeletePK)).To(Succeed())

			state := ms.GetSecureBootState(testSystemURI)
			Expect(state.Mode).To(Equal("SetupMode"))
		})

		It("ResetAllKeysToDefault restores mode to UserMode", func(ctx SpecContext) {
			// First delete all keys.
			Expect(bmc.ResetSecureBootKeys(ctx, testSystemURI, SecureBootResetDeleteAllKeys)).To(Succeed())
			// Then restore defaults.
			Expect(bmc.ResetSecureBootKeys(ctx, testSystemURI, SecureBootResetResetAllKeysToDefault)).To(Succeed())

			state := ms.GetSecureBootState(testSystemURI)
			Expect(state.Mode).To(Equal("UserMode"))
		})
	})

	Describe("EnrollCertificate", func() {
		const fakePEM = "-----BEGIN CERTIFICATE-----\nMIIBxxx\n-----END CERTIFICATE-----\n"

		It("adds a certificate to the PK database", func(ctx SpecContext) {
			// First put the system in setup mode so PK enrollment is valid.
			Expect(bmc.ResetSecureBootKeys(ctx, testSystemURI, SecureBootResetDeleteAllKeys)).To(Succeed())

			Expect(bmc.EnrollCertificate(ctx, testSystemURI, "PK", fakePEM)).To(Succeed())
		})

		It("adds a certificate to the db database", func(ctx SpecContext) {
			Expect(bmc.EnrollCertificate(ctx, testSystemURI, "db", fakePEM)).To(Succeed())
		})

		It("returns an error for an unknown database", func(ctx SpecContext) {
			err := bmc.EnrollCertificate(ctx, testSystemURI, "nonexistent", fakePEM)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("EnrollHash", func() {
		const fakeHash = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

		It("adds a hash to the dbx database", func(ctx SpecContext) {
			Expect(bmc.EnrollHash(ctx, testSystemURI, "dbx", fakeHash)).To(Succeed())
		})

		It("adds a hash to the db database", func(ctx SpecContext) {
			Expect(bmc.EnrollHash(ctx, testSystemURI, "db", fakeHash)).To(Succeed())
		})

		It("returns an error for an unknown database", func(ctx SpecContext) {
			err := bmc.EnrollHash(ctx, testSystemURI, "nonexistent", fakeHash)
			Expect(err).To(HaveOccurred())
		})
	})
})
